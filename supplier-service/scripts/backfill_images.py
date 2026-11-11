# AI Assistance Disclosure:
# Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-06
# Scope: Implementation of code.
# Author review: Reviewed, validated, and tested by the authors.

"""Preview or apply seed image URLs through the supplier API (Python stdlib only)."""

import argparse
import csv
import getpass
import io
import json
import os
from pathlib import Path
import sys
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener
from uuid import UUID


SEED = Path(__file__).resolve().parents[2] / "data/csv/supplier-seed-data.csv"


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        # Never forward the administrator token to a redirected destination.
        return None


def request_json(base, token, path, patch=None):
    body = None if patch is None else json.dumps(patch).encode()
    request = Request(
        base + path,
        data=body,
        method="GET" if patch is None else "PATCH",
        headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"},
    )
    try:
        with build_opener(NoRedirect).open(request, timeout=20) as response:
            return json.load(response)
    except HTTPError as error:
        raise RuntimeError(f"{request.method} {path}: HTTP {error.code}; stopped. Successful earlier updates remain saved; rerunning skips them.") from None
    except URLError:
        raise RuntimeError("API connection failed; check the URL and running services. Rerun to inspect any completed updates.") from None


def seed_images():
    base = os.environ.get("SUPPLIER_IMAGE_BASE_URL", "").strip().rstrip("/")
    origin = urlsplit(base)
    if origin.scheme != "https" or not origin.hostname or origin.username or origin.password or origin.path or origin.query or origin.fragment or "?" in base or "#" in base:
        raise ValueError("Set SUPPLIER_IMAGE_BASE_URL to an HTTPS origin without a path, query, fragment, or credentials.")
    contents = SEED.read_bytes()
    try:
        text = contents.decode("utf-8")
    except UnicodeDecodeError:
        text = contents.decode("cp1252")
    images = {}
    for row in csv.DictReader(io.StringIO(text)):
        name, image = row["Name"], row["ImageURL"]
        image = image.strip()
        if name in images or not image.startswith("/") or image.startswith("//") or "?" in image or "#" in image:
            raise ValueError("Seed names must be unique and images must be root-relative object paths such as /ANNA.jpeg.")
        images[name] = base + image
    if not images:
        raise ValueError("No seed images found.")
    return images


def list_suppliers(base, token):
    items, seen, cursor = [], set(), ""
    while True:
        page = request_json(base, token, "/suppliers?" + urlencode({"limit": 100, "cursor": cursor}))
        items.extend(page["items"])
        cursor = page.get("nextCursor", "")
        if not cursor:
            return items
        if cursor in seen:
            raise ValueError("API repeated a pagination cursor; stopped.")
        seen.add(cursor)


def plan_updates(images, suppliers):
    updates = []
    for name, image in images.items():
        matches = [item for item in suppliers if item["name"] == name]
        if len(matches) != 1:
            raise ValueError(f"Expected exactly one supplier named {name!r}, found {len(matches)}. No updates applied.")
        item = matches[0]
        UUID(item["supplierId"])
        if item.get("imageUrl") != image:
            updates.append((item["supplierId"], name, image))
    return updates


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--api-url", default="http://localhost:8081/supplier-service")
    parser.add_argument("--apply", action="store_true", help="apply changes; default only previews")
    args = parser.parse_args()
    base = args.api_url.rstrip("/")
    url = urlsplit(base)
    if url.username or url.password or url.query or url.fragment or not url.hostname:
        parser.error("API URL must not contain credentials, a query, or a fragment")
    if url.scheme != "https" and not (url.scheme == "http" and url.hostname in {"localhost", "127.0.0.1", "::1"}):
        parser.error("HTTPS is required except for a local API")
    images = seed_images()
    token = os.environ.get("SUPPLIER_API_TOKEN") or getpass.getpass("Supplier API administrator access token (hidden): ")
    if not token.strip():
        parser.error("An administrator access token is required")
    updates = plan_updates(images, list_suppliers(base, token))
    print(f"{len(updates)} changes; {len(images) - len(updates)} already match.")
    for supplier_id, name, image in updates:
        print(f"{'Updating' if args.apply else 'Would update'} {name}: {image}", flush=True)
        if args.apply:
            result = request_json(base, token, "/suppliers/" + quote(supplier_id, safe=""), {"imageUrl": image})
            if result.get("imageUrl") != image:
                raise RuntimeError(f"Unexpected image URL returned for {name}; stopped.")
    print("Done." if args.apply else "Preview only. Rerun with --apply to save these changes.")


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, ValueError, KeyError, OSError) as error:
        print(f"Error: {error}", file=sys.stderr)
        sys.exit(1)
