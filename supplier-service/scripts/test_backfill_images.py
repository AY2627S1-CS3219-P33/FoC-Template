# AI Assistance Disclosure:
# Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-06
# Scope: Implementation of test code (unit and integration tests).
# Author review: Reviewed, validated, and tested by the authors.

import json
import unittest
from unittest.mock import MagicMock, patch
from urllib.error import HTTPError

import backfill_images as backfill


class BackfillImagesTests(unittest.TestCase):
    def setUp(self):
        environment = patch.dict(backfill.os.environ, {"SUPPLIER_IMAGE_BASE_URL": "https://images.example.com/"})
        environment.start()
        self.addCleanup(environment.stop)
        self.image = "https://images.example.com/ANNA.jpeg"
        self.supplier = {
            "supplierId": "10000000-0000-4000-8000-000000000001",
            "name": "Anna's x Soup Union",
            "imageUrl": "https://old.example/anna.jpeg",
        }

    def test_seed_has_21_unique_jpeg_urls(self):
        images = backfill.seed_images()
        self.assertEqual(len(images), 21)
        self.assertEqual(len(set(images.values())), 21)
        self.assertTrue(all(url.endswith(".jpeg") for url in images.values()))
        self.assertEqual(images[self.supplier["name"]], self.image)

    def test_image_host_is_configurable_and_required(self):
        with patch.dict(backfill.os.environ, {"SUPPLIER_IMAGE_BASE_URL": "https://other.example.com"}):
            self.assertEqual(backfill.seed_images()[self.supplier["name"]], "https://other.example.com/ANNA.jpeg")
        for origin in ("", "http://images.example.com", "https://user:password@images.example.com", "https://images.example.com/path", "https://images.example.com?token"):
            with self.subTest(origin=origin), patch.dict(backfill.os.environ, {"SUPPLIER_IMAGE_BASE_URL": origin}), self.assertRaises(ValueError):
                backfill.seed_images()

    def test_matching_url_is_skipped(self):
        images = {self.supplier["name"]: self.image}
        self.assertEqual(backfill.plan_updates(images, [self.supplier]), [
            (self.supplier["supplierId"], self.supplier["name"], self.image)
        ])
        self.supplier["imageUrl"] = self.image
        self.assertEqual(backfill.plan_updates(images, [self.supplier]), [])

    def test_missing_or_duplicate_name_stops_entire_plan(self):
        images = {self.supplier["name"]: self.image}
        for items in ([], [self.supplier, self.supplier]):
            with self.subTest(items=items), self.assertRaises(ValueError):
                backfill.plan_updates(images, items)
        with self.assertRaises(ValueError):
            backfill.plan_updates({**images, "Missing": self.image}, [self.supplier])

    def test_other_suppliers_are_untouched(self):
        other = {**self.supplier, "name": "Admin-added supplier"}
        plan = backfill.plan_updates({self.supplier["name"]: self.image}, [other, self.supplier])
        self.assertEqual(len(plan), 1)
        self.assertEqual(plan[0][1], self.supplier["name"])

    @patch.object(backfill, "request_json")
    def test_pagination_and_repeated_cursor(self, request):
        request.side_effect = [
            {"items": [self.supplier], "nextCursor": "next/+"},
            {"items": [], "nextCursor": ""},
        ]
        self.assertEqual(backfill.list_suppliers("http://localhost", "token"), [self.supplier])
        self.assertIn("cursor=next%2F%2B", request.call_args.args[2])
        request.side_effect = None
        request.return_value = {"items": [], "nextCursor": "repeated"}
        with self.assertRaises(ValueError):
            backfill.list_suppliers("http://localhost", "token")

    @patch.object(backfill, "build_opener")
    def test_patch_only_sends_image_and_errors_do_not_expose_token(self, opener):
        response = MagicMock()
        response.read.return_value = json.dumps({"imageUrl": self.image})
        opener.return_value.open.return_value.__enter__.return_value = response
        backfill.request_json("http://localhost", "secret-token", "/suppliers/id", {"imageUrl": self.image})
        request = opener.return_value.open.call_args.args[0]
        self.assertEqual(request.method, "PATCH")
        self.assertEqual(json.loads(request.data), {"imageUrl": self.image})
        opener.return_value.open.side_effect = HTTPError("url", 403, "secret-token", {}, None)
        with self.assertRaises(RuntimeError) as caught:
            backfill.request_json("http://localhost", "secret-token", "/suppliers/id", {"imageUrl": self.image})
        self.assertIn("HTTP 403", str(caught.exception))
        self.assertNotIn("secret-token", str(caught.exception))

    @patch.dict(backfill.os.environ, {"SUPPLIER_API_TOKEN": "test-token"})
    @patch.object(backfill, "seed_images")
    @patch.object(backfill, "request_json")
    @patch("builtins.print")
    def test_preview_never_writes_and_apply_can_be_rerun(self, output, request, seed):
        seed.return_value = {self.supplier["name"]: self.image}
        for apply, current, expected_calls in (
            (False, "https://old.example/anna.jpeg", 1),
            (True, "https://old.example/anna.jpeg", 2),
            (True, self.image, 1),
        ):
            with self.subTest(apply=apply, current=current):
                request.reset_mock()
                request.side_effect = [
                    {"items": [{**self.supplier, "imageUrl": current}]},
                    {"imageUrl": self.image},
                ]
                with patch.object(backfill.sys, "argv", ["backfill_images.py"] + (["--apply"] if apply else [])):
                    backfill.main()
                self.assertEqual(request.call_count, expected_calls)
                if expected_calls == 2:
                    self.assertEqual(request.call_args.args[3], {"imageUrl": self.image})


if __name__ == "__main__":
    unittest.main()
