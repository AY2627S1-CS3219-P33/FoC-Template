# AI Assistance Disclosure:
# Tool: ChatGPT (model: GPT-6 Luna), date: 2026-11-11
# Scope: Implementation of the manual GitHub deployment script.
# Author review: I validated correctness, edited for style, and added boundary checks.

"""Manual GitHub deployment. AWS access uses the CLI's short-lived OIDC session."""

import copy
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
CONFIG = json.loads((ROOT / ".github/deploy.json").read_text())
COMPONENTS = (*CONFIG["services"], "frontend")
STATE_PREFIX = "deployments/state/"


def run(*args, cwd=None, capture=True):
    return subprocess.run(
        args, cwd=cwd or ROOT, check=True, text=True,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.PIPE if capture else None, timeout=1800,
    ).stdout or ""


def aws(*args):
    return json.loads(run("aws", *args, "--output", "json") or "{}")


def report(message):
    print(message, flush=True)
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as summary:
            summary.write(message + "\n\n")


def read_state(component):
    key = STATE_PREFIX + component + ".json"
    listing = aws("s3api", "list-objects-v2", "--bucket", CONFIG["bucket"], "--prefix", key)
    if not any(item["Key"] == key for item in listing.get("Contents", [])):
        return None
    with tempfile.TemporaryDirectory() as directory:
        path = Path(directory) / "state.json"
        aws("s3api", "get-object", "--bucket", CONFIG["bucket"], "--key", key, str(path))
        state = json.loads(path.read_text())
        if state.get("commit") is not None and not re.fullmatch(r"[0-9a-f]{40}", state["commit"]):
            raise ValueError("Invalid deployment-state commit")
        return state


def write_state(component, target, previous):
    # Keep just one previous release; avoid recursively nesting deployment history.
    previous = {k: v for k, v in (previous or {}).items() if k != "previous"}
    state = {**target, "previous": previous or None}
    with tempfile.TemporaryDirectory() as directory:
        path = Path(directory) / "state.json"
        path.write_text(json.dumps(state))
        aws("s3api", "put-object", "--bucket", CONFIG["bucket"],
            "--key", STATE_PREFIX + component + ".json", "--body", str(path),
            "--content-type", "application/json")


def changed_paths(state, commit):
    baseline = (state or {}).get("commit")
    if baseline:
        # An unavailable baseline is an error, never an excuse to skip a deployment.
        output = run("git", "diff", "--name-only", "--no-renames", "-z", baseline, commit)
    else:
        output = run("git", "ls-tree", "-r", "--name-only", "-z", commit)
    return output.rstrip("\0").split("\0") if output else []


def affects(component, path):
    if path.endswith(".md"):
        return False
    if component == "supplier-service" and path in {"data/csv/supplier-seed-data.csv", ".dockerignore"}:
        return True
    prefix = "user-frontend/" if component == "frontend" else component + "/"
    return path.startswith(prefix)


def plan(states, commit, selection):
    result = {}
    for component in COMPONENTS:
        if selection not in {"auto", "all", component}:
            continue
        paths = changed_paths(states[component], commit)
        if selection != "auto" or states[component] is None or any(affects(component, p) for p in paths):
            result[component] = [p for p in paths if p.startswith(component + "/migrations/") and p.endswith(".sql")]
    return result


def service_info(component):
    response = aws("ecs", "describe-services", "--cluster", CONFIG["cluster"],
                   "--services", CONFIG["services"][component]["service"])
    if response.get("failures") or len(response.get("services", [])) != 1:
        raise RuntimeError(f"Cannot describe {component}")
    service = response["services"][0]
    if service.get("deploymentController", {}).get("type", "ECS") != "ECS":
        raise RuntimeError("Only ECS rolling deployments are supported")
    return service


def task_input(description, component, image):
    task = copy.deepcopy(description["taskDefinition"])
    for key in ("taskDefinitionArn", "revision", "status", "requiresAttributes",
                "compatibilities", "registeredAt", "registeredBy", "deregisteredAt"):
        task.pop(key, None)
    # ECS injects its Service Connect proxy at runtime; only replace the app image.
    registry = f'{CONFIG["account"]}.dkr.ecr.{CONFIG["region"]}.amazonaws.com'
    candidates = [c for c in task["containerDefinitions"]
                  if c["image"].split("@")[0].split(":")[0] == f"{registry}/{component}"]
    if len(candidates) != 1:
        raise RuntimeError(f"Expected exactly one {component} container from this ECR repository")
    if task.get("runtimePlatform", {}).get("cpuArchitecture") != "ARM64":
        raise RuntimeError("Expected an ARM64 task definition")
    candidates[0]["image"] = image
    if description.get("tags"):
        task["tags"] = description["tags"]
    return task


def public_body(path):
    with urllib.request.urlopen(CONFIG["base_url"] + path, timeout=15) as response:
        if response.status != 200:
            raise RuntimeError("Public endpoint did not return HTTP 200")
        return response.read()


def check_public(path, expected_hash=None):
    for attempt in range(6):
        try:
            body = public_body(path)
            if expected_hash and hashlib.sha256(body).hexdigest() != expected_hash:
                raise RuntimeError("Frontend HTML does not match the deployed release")
            if not expected_hash and not isinstance(json.loads(body), dict):
                raise RuntimeError("Public API endpoint did not return a JSON object")
            return
        except (urllib.error.URLError, TimeoutError, RuntimeError, ValueError):
            if attempt == 5:
                raise
            time.sleep(10)


def update_service(component, task_arn):
    # Omit desired-count entirely: deployment must never start a stopped service.
    aws("ecs", "update-service", "--cluster", CONFIG["cluster"],
        "--service", CONFIG["services"][component]["service"], "--task-definition", task_arn)
    service = service_info(component)
    if service["desiredCount"] > 0:
        aws("ecs", "wait", "services-stable", "--cluster", CONFIG["cluster"],
            "--services", CONFIG["services"][component]["service"])
        service = service_info(component)
    primary = [d for d in service.get("deployments", []) if d["status"] == "PRIMARY"]
    if service["taskDefinition"] != task_arn or not primary or primary[0]["taskDefinition"] != task_arn:
        raise RuntimeError("ECS did not retain the requested revision; it may have rolled back")
    if primary[0].get("rolloutState") == "FAILED":
        raise RuntimeError("ECS deployment failed")
    if service["desiredCount"] == 0:
        return "updated while stopped; runtime checks skipped"
    check_public(CONFIG["services"][component]["health_path"])
    return "running; ECS stability and public endpoint checks passed"


def deploy_backend(component, commit, release, previous):
    current = service_info(component)
    description = aws("ecs", "describe-task-definition", "--task-definition",
                      current["taskDefinition"], "--include", "TAGS")
    # Validate the current definition before building or pushing anything.
    task_input(description, component, "validation-only")
    registry = f'{CONFIG["account"]}.dkr.ecr.{CONFIG["region"]}.amazonaws.com'
    password = run("aws", "ecr", "get-login-password").strip()
    subprocess.run(["docker", "login", "--username", "AWS", "--password-stdin", registry],
                   input=password, text=True, check=True, stdout=subprocess.DEVNULL)
    with tempfile.TemporaryDirectory() as directory:
        metadata = Path(directory) / "image.json"
        run("docker", "buildx", "build", "--platform", "linux/arm64", "--provenance=false",
            "--tag", f"{registry}/{component}:{release}", "--push", "--metadata-file", str(metadata),
            "--file", component + "/Dockerfile", CONFIG["services"][component]["context"], capture=False)
        digest = json.loads(metadata.read_text())["containerimage.digest"]
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
            raise ValueError("Invalid image digest")
        task = task_input(description, component, f"{registry}/{component}@{digest}")
        path = Path(directory) / "task.json"
        path.write_text(json.dumps(task))
        target = aws("ecs", "register-task-definition", "--cli-input-json", "file://" + str(path))
    arn = target["taskDefinition"]["taskDefinitionArn"]
    status = update_service(component, arn)
    write_state(component, {"commit": commit, "task_definition": arn, "image_digest": digest},
                previous or {"commit": None, "task_definition": current["taskDefinition"]})
    report(f"**{component}**: {arn} — {status}.")


def publish_frontend(source, html_hash):
    destination = f's3://{CONFIG["bucket"]}/frontend/'
    # Never delete: old pages may still reference older hashed assets, and the bucket holds supplier images.
    run("aws", "s3", "sync", source, destination, "--exclude", "index.html", "--only-show-errors")
    run("aws", "s3", "cp", source.rstrip("/") + "/index.html", destination + "index.html",
        "--cache-control", "no-cache", "--content-type", "text/html", "--only-show-errors")
    invalidation = aws("cloudfront", "create-invalidation", "--distribution-id", CONFIG["distribution"],
                       "--paths", "/", "/frontend/index.html")
    aws("cloudfront", "wait", "invalidation-completed", "--distribution-id", CONFIG["distribution"],
        "--id", invalidation["Invalidation"]["Id"])
    check_public("/", html_hash)


def deploy_frontend(commit, release, previous):
    run("npm", "ci", cwd=ROOT / "user-frontend", capture=False)
    run("npm", "run", "build", cwd=ROOT / "user-frontend", capture=False)
    dist = ROOT / "user-frontend/dist"
    html_hash = hashlib.sha256((dist / "index.html").read_bytes()).hexdigest()
    snapshot = f's3://{CONFIG["bucket"]}/deployments/frontend/{release}/'
    run("aws", "s3", "sync", str(dist) + "/", snapshot, "--only-show-errors")
    publish_frontend(str(dist) + "/", html_hash)
    write_state("frontend", {"commit": commit, "snapshot": snapshot, "html_hash": html_hash}, previous)
    report(f"**frontend**: {commit} — HTML verified through CloudFront.")


def rollback(component, state):
    if not state:
        raise RuntimeError("No tracked deployment to roll back")
    if component == "frontend":
        try:
            matches = hashlib.sha256(public_body("/")).hexdigest() == state["html_hash"]
        except (urllib.error.URLError, TimeoutError, RuntimeError):
            matches = False
    else:
        matches = service_info(component)["taskDefinition"] == state["task_definition"]
    # A failed/unrecorded rollout restores the last recorded release; otherwise use its predecessor.
    target = state.get("previous") if matches else state
    if not target:
        raise RuntimeError("No previous tracked release; the first frontend deployment has no rollback snapshot")
    if component == "frontend":
        with tempfile.TemporaryDirectory() as directory:
            run("aws", "s3", "sync", target["snapshot"], directory + "/", "--only-show-errors")
            publish_frontend(directory + "/", target["html_hash"])
        status = "HTML verified through CloudFront"
    else:
        status = update_service(component, target["task_definition"])
    write_state(component, target, state if matches else state.get("previous"))
    report(f"**{component}**: restored recorded release — {status}. Database schema was not changed.")


def main():
    if os.environ.get("GITHUB_REPOSITORY") != CONFIG["repository"] or os.environ.get("GITHUB_REF") != "refs/heads/main":
        raise RuntimeError("Deployments must run from the team repository's main branch")
    component = os.environ.get("DEPLOY_COMPONENT", "auto")
    operation = os.environ.get("DEPLOY_OPERATION", "deploy")
    if component not in {*COMPONENTS, "auto", "all"} or operation not in {"deploy", "rollback"}:
        raise ValueError("Invalid deployment selection")
    if aws("sts", "get-caller-identity")["Account"] != CONFIG["account"]:
        raise RuntimeError("Wrong AWS account")
    states = {name: read_state(name) for name in COMPONENTS}
    acknowledged = os.environ.get("MIGRATIONS_APPLIED") == "true"
    if operation == "rollback":
        if component not in COMPONENTS or not acknowledged:
            raise RuntimeError("Rollback requires one component and confirmation of database compatibility")
        rollback(component, states[component])
        return
    commit = run("git", "rev-parse", "HEAD").strip()
    selected = plan(states, commit, component)
    report("Selected components: " + (", ".join(selected) or "none; no deployable changes"))
    migrations = [p for paths in selected.values() for p in paths]
    if migrations:
        report("Migration files requiring manual review:\n" + "\n".join(f"- `{p}`" for p in migrations))
        if not acknowledged:
            raise RuntimeError("Apply/check the required migrations manually, then rerun with migrations_applied checked")
    release = f'{commit}-{os.environ["GITHUB_RUN_ID"]}-{os.environ["GITHUB_RUN_ATTEMPT"]}'
    for name in selected:
        report(f"Deploying **{name}**…")
        if name == "frontend":
            deploy_frontend(commit, release, states[name])
        else:
            deploy_backend(name, commit, release, states[name])


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as error:
        # Task definitions can contain legacy plaintext credentials. Never dump AWS payloads or command output.
        report(f"Command failed: {error.cmd[0]} {error.cmd[1]} (exit {error.returncode}). Deployment state was not advanced for this component.")
        raise SystemExit(1)
    except (RuntimeError, ValueError, KeyError, TimeoutError, subprocess.TimeoutExpired, urllib.error.URLError) as error:
        report(f"Deployment stopped: {error}")
        raise SystemExit(1)
