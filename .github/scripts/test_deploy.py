# AI Assistance Disclosure:
# Tool: ChatGPT (model: GPT-6 Luna), date: 2026-11-11
# Scope: Implementation of tests for the GitHub deployment script.
# Author review: I validated correctness, edited for style, and added boundary checks.

import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import deploy


class SelectionTests(unittest.TestCase):
    def test_changes_accumulate_since_each_components_deployment(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)

            def git(*args):
                return deploy.run("git", "-C", directory, *args).strip()

            git("init", "-q")
            git("config", "user.email", "ci@example.com")
            git("config", "user.name", "CI Test")
            (root / "supplier-service").mkdir()
            (root / "supplier-service/main.go").write_text("old")
            git("add", ".")
            git("commit", "-qm", "initial")
            baseline = git("rev-parse", "HEAD")
            (root / "supplier-service/main.go").write_text("new")
            git("commit", "-qam", "supplier change")
            supplier_commit = git("rev-parse", "HEAD")
            (root / "README.md").write_text("documentation")
            git("add", ".")
            git("commit", "-qm", "docs change after supplier")
            head = git("rev-parse", "HEAD")
            states = {c: {"commit": baseline} for c in deploy.COMPONENTS}
            with patch.object(deploy, "ROOT", root):
                self.assertEqual(deploy.plan(states, head, "auto"), {"supplier-service": []})
                states["supplier-service"] = {"commit": supplier_commit}
                self.assertEqual(deploy.plan(states, head, "auto"), {})

    def test_shared_seed_docs_and_deleted_paths(self):
        self.assertTrue(deploy.affects("supplier-service", "data/csv/supplier-seed-data.csv"))
        self.assertFalse(deploy.affects("order-service", "data/csv/supplier-seed-data.csv"))
        self.assertFalse(deploy.affects("supplier-service", "supplier-service/README.md"))
        self.assertTrue(deploy.affects("user-service", "user-service/internal/deleted.go"))
        self.assertTrue(deploy.affects("frontend", "user-frontend/package-lock.json"))
        self.assertFalse(deploy.affects("frontend", "user-service/internal/main.go"))
        self.assertFalse(deploy.affects("chat-service", ".github/deploy.json"))
        self.assertFalse(deploy.affects("notification-service", ".github/scripts/deploy.py"))

    @patch.object(deploy, "changed_paths", return_value=["supplier-service/migrations/00003_new.sql"])
    def test_first_deployment_and_forced_selection(self, _changes):
        states = dict.fromkeys(deploy.COMPONENTS)
        self.assertEqual(set(deploy.plan(states, "head", "auto")), set(deploy.COMPONENTS))
        self.assertEqual(deploy.plan(states, "head", "supplier-service"), {
            "supplier-service": ["supplier-service/migrations/00003_new.sql"]})


class ECSTests(unittest.TestCase):
    def service(self, count=0, arn="new", primary="new"):
        return {"desiredCount": count, "taskDefinition": arn,
                "deployments": [{"status": "PRIMARY", "taskDefinition": primary}]}

    @patch.object(deploy, "check_public")
    @patch.object(deploy, "aws", return_value={})
    def test_stopped_service_is_not_started_or_claimed_runtime_verified(self, aws, health):
        with patch.object(deploy, "service_info", return_value=self.service()):
            status = deploy.update_service("supplier-service", "new")
        self.assertIn("stopped", status)
        health.assert_not_called()
        aws.assert_called_once()
        self.assertNotIn("--desired-count", aws.call_args.args)
        self.assertNotIn("--force-new-deployment", aws.call_args.args)

    @patch.object(deploy, "check_public")
    @patch.object(deploy, "aws", return_value={})
    def test_running_service_waits_and_checks_readiness(self, aws, health):
        with patch.object(deploy, "service_info", return_value=self.service(count=2)):
            deploy.update_service("supplier-service", "new")
        self.assertEqual(aws.call_args_list[1].args[:3], ("ecs", "wait", "services-stable"))
        health.assert_called_once_with("/supplier-service/readyz")
        for call in aws.call_args_list:
            self.assertNotIn("--desired-count", call.args)

    @patch.object(deploy, "check_public")
    @patch.object(deploy, "aws", return_value={})
    def test_ecs_stable_after_automatic_rollback_is_a_failure(self, _aws, health):
        with patch.object(deploy, "service_info", side_effect=[
            self.service(count=1), self.service(count=1, arn="old", primary="old")
        ]):
            with self.assertRaisesRegex(RuntimeError, "rolled back"):
                deploy.update_service("supplier-service", "new")
        health.assert_not_called()

    def test_task_revision_preserves_settings_and_only_replaces_app_image(self):
        registry = f'{deploy.CONFIG["account"]}.dkr.ecr.{deploy.CONFIG["region"]}.amazonaws.com'
        description = {"tags": [{"key": "project", "value": "foc"}], "taskDefinition": {
            "family": "supplier-service-task", "revision": 6, "status": "ACTIVE",
            "taskDefinitionArn": "old", "registeredBy": "someone", "compatibilities": ["FARGATE"],
            "runtimePlatform": {"cpuArchitecture": "ARM64"}, "executionRoleArn": "execution",
            "taskRoleArn": "task", "networkMode": "awsvpc", "cpu": "256", "memory": "512",
            "containerDefinitions": [
                {"name": "app", "image": registry + "/supplier-service:old",
                 "environment": [{"name": "AUTH0_ISSUER", "value": "https://login.bestfoc.app/"}],
                 "secrets": [{"name": "DATABASE_URL", "valueFrom": "secret-reference"}]},
                {"name": "sidecar", "image": "other:unchanged"},
            ],
        }}
        original = copy.deepcopy(description)
        task = deploy.task_input(description, "supplier-service", "new-image")
        self.assertEqual(description, original)
        self.assertNotIn("revision", task)
        self.assertEqual(task["tags"], description["tags"])
        app = task["containerDefinitions"][0]
        self.assertEqual(app["image"], "new-image")
        self.assertEqual(app["secrets"], original["taskDefinition"]["containerDefinitions"][0]["secrets"])
        self.assertEqual(task["containerDefinitions"][1]["image"], "other:unchanged")
        self.assertEqual(task["cpu"], "256")
        with self.assertRaisesRegex(RuntimeError, "exactly one"):
            deploy.task_input(description, "order-service", "new-image")


class SafetyTests(unittest.TestCase):
    def test_deploy_targets_match_scoped_iam_resources(self):
        policy = json.loads((deploy.ROOT / ".github/aws/deploy-permissions.json").read_text())
        statements = {statement["Sid"]: statement for statement in policy["Statement"]}
        ecr = set(statements["PublishServiceImages"]["Resource"])
        task_definitions = set(statements["ReadAndTagServiceRevisions"]["Resource"])
        services = set(statements["UpdateExistingServices"]["Resource"])
        for name, config in deploy.CONFIG["services"].items():
            prefix = f'arn:aws:ecs:{deploy.CONFIG["region"]}:{deploy.CONFIG["account"]}'
            self.assertIn(
                f'arn:aws:ecr:{deploy.CONFIG["region"]}:{deploy.CONFIG["account"]}:repository/{name}', ecr
            )
            self.assertIn(f"{prefix}:task-definition/{name}-task:*", task_definitions)
            self.assertIn(f'{prefix}:service/{deploy.CONFIG["cluster"]}/{config["service"]}', services)

    def test_task_definition_reads_have_required_unscoped_permission(self):
        policy = json.loads((deploy.ROOT / ".github/aws/deploy-permissions.json").read_text())
        reads = [s for s in policy["Statement"]
                 if s["Action"] == "ecs:DescribeTaskDefinition"]
        self.assertEqual(len(reads), 1)
        self.assertEqual(reads[0]["Effect"], "Allow")
        self.assertEqual(reads[0]["Resource"], "*")
        tagging = next(s for s in policy["Statement"] if s["Sid"] == "ReadAndTagServiceRevisions")
        self.assertEqual(set(tagging["Action"]), {"ecs:ListTagsForResource", "ecs:TagResource"})
        self.assertTrue(all(r.startswith("arn:aws:ecs:") for r in tagging["Resource"]))

    @patch.object(deploy.time, "sleep")
    @patch.object(deploy, "public_body", return_value=b"<html>fallback page</html>")
    def test_html_fallback_cannot_pass_an_api_smoke_check(self, body, _sleep):
        with self.assertRaises(ValueError):
            deploy.check_public("/supplier-service/readyz")
        self.assertEqual(body.call_count, 6)

    def test_permissions_do_not_grant_supplier_image_writes_or_secret_reads(self):
        policy = json.loads((deploy.ROOT / ".github/aws/deploy-permissions.json").read_text())
        for statement in policy["Statement"]:
            actions = statement["Action"]
            actions = [actions] if isinstance(actions, str) else actions
            self.assertNotIn("s3:DeleteObject", actions)
            self.assertFalse(any(action.startswith("secretsmanager:") for action in actions))
            if "s3:PutObject" in actions:
                self.assertEqual(set(statement["Resource"]), {
                    "arn:aws:s3:::foc-supplier-images/frontend/*",
                    "arn:aws:s3:::foc-supplier-images/deployments/*"})
            if "iam:PassRole" in actions:
                self.assertTrue(all("*" not in arn for arn in statement["Resource"]))

    @patch.object(deploy, "report")
    @patch.object(deploy, "deploy_backend")
    @patch.object(deploy, "read_state", return_value=None)
    @patch.object(deploy, "plan", return_value={"supplier-service": ["supplier-service/migrations/00003.sql"]})
    @patch.object(deploy, "run", return_value="a" * 40)
    @patch.object(deploy, "aws", return_value={"Account": deploy.CONFIG["account"]})
    def test_migrations_block_before_build_or_deployment(self, _aws, _run, _plan, _state, backend, _report):
        env = {"GITHUB_REPOSITORY": deploy.CONFIG["repository"], "GITHUB_REF": "refs/heads/main",
               "MIGRATIONS_APPLIED": "false", "DEPLOY_COMPONENT": "auto", "DEPLOY_OPERATION": "deploy"}
        with patch.dict(os.environ, env):
            with self.assertRaisesRegex(RuntimeError, "migrations_applied"):
                deploy.main()
        backend.assert_not_called()

    @patch.object(deploy, "aws")
    def test_forks_and_feature_branches_cannot_deploy(self, aws):
        for repository, ref in [("someone/fork", "refs/heads/main"),
                                (deploy.CONFIG["repository"], "refs/heads/feature")]:
            with patch.dict(os.environ, {"GITHUB_REPOSITORY": repository, "GITHUB_REF": ref}):
                with self.assertRaisesRegex(RuntimeError, "team repository"):
                    deploy.main()
        aws.assert_not_called()

    def test_missing_state_is_distinguished_from_access_denied(self):
        with patch.object(deploy, "aws", return_value={}):
            self.assertIsNone(deploy.read_state("frontend"))
        with patch.object(deploy, "aws", side_effect=subprocess.CalledProcessError(1, "aws", stderr="(AccessDenied)")):
            with self.assertRaises(subprocess.CalledProcessError):
                deploy.read_state("frontend")

    @patch.object(deploy, "check_public")
    @patch.object(deploy, "aws", return_value={"Invalidation": {"Id": "invalidation"}})
    @patch.object(deploy, "run", return_value="")
    def test_frontend_upload_is_scoped_assets_first_and_never_deletes(self, run, _aws, health):
        deploy.publish_frontend("/tmp/dist/", "hash")
        assets, html = run.call_args_list
        self.assertEqual(assets.args[:3], ("aws", "s3", "sync"))
        self.assertIn("s3://foc-supplier-images/frontend/", assets.args)
        self.assertIn("--exclude", assets.args)
        self.assertEqual(html.args[2], "cp")
        self.assertIn("s3://foc-supplier-images/frontend/index.html", html.args)
        for call in run.call_args_list:
            self.assertNotIn("--delete", call.args)
        health.assert_called_once_with("/", "hash")

    @patch.object(deploy, "report")
    @patch.object(deploy, "write_state")
    @patch.object(deploy, "publish_frontend", side_effect=RuntimeError("smoke check failed"))
    @patch.object(deploy, "run", return_value="")
    def test_failed_frontend_does_not_advance_its_baseline(self, _run, _publish, state, _report):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            dist = root / "user-frontend/dist"
            dist.mkdir(parents=True)
            (dist / "index.html").write_text("release")
            with patch.object(deploy, "ROOT", root):
                with self.assertRaisesRegex(RuntimeError, "smoke check failed"):
                    deploy.deploy_frontend("commit", "release", None)
        state.assert_not_called()

    @patch.object(deploy, "report")
    @patch.object(deploy, "write_state")
    @patch.object(deploy, "update_service", return_value="stopped")
    @patch.object(deploy, "service_info")
    def test_rollback_restores_last_success_after_unrecorded_failure(self, service, update, state, _report):
        service.return_value = {"taskDefinition": "failed-attempt"}
        recorded = {"commit": "good", "task_definition": "good-arn",
                    "previous": {"commit": "older", "task_definition": "older-arn"}}
        deploy.rollback("supplier-service", recorded)
        update.assert_called_once_with("supplier-service", "good-arn")
        self.assertEqual(state.call_args.args[2], recorded["previous"])
        service.return_value = {"taskDefinition": "good-arn"}
        update.reset_mock()
        deploy.rollback("supplier-service", recorded)
        update.assert_called_once_with("supplier-service", "older-arn")


if __name__ == "__main__":
    unittest.main()
