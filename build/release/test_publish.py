"""Exercise draft-only API boundaries; actual upload is verified in the private rehearsal."""
import hashlib
import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("publisher", Path(__file__).with_name("publish.py"))
publisher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(publisher)


class PublisherTests(unittest.TestCase):
    def exercise(self, private=True, existing=False, bad_digest=False):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / "client.zip").write_bytes(b"unit client")
            manifest = {"files": [{"filename": "client.zip"}]}
            (root / "clients.json").write_text(json.dumps(manifest))
            (root / "clients.json.sig").write_bytes(b"unit signature")
            requests = []

            def gh(*args):
                requests.append(args)
                if args[:2] == ("api", "repos/test/phpm"):
                    return json.dumps({"full_name": "test/phpm", "private": private})
                if args[:2] == ("api", "repos/test/phpm/commits/" + "a" * 40):
                    return json.dumps({"sha": "a" * 40})
                if "--slurp" in args:
                    return json.dumps([[{"tag_name": "candidate"}]] if existing else [[]])
                if "--input" in args:
                    body = json.loads(Path(args[-1]).read_text())
                    self.assertIs(body["draft"], True)
                    self.assertIs(body["prerelease"], True)
                    self.assertEqual(body["target_commitish"], "a" * 40)
                    return json.dumps({"id": 42, "draft": True, "html_url": "https://example.test/draft"})
                if args[:2] == ("release", "upload"):
                    self.assertNotIn("--clobber", args)
                    return ""
                self.assertEqual(args, ("api", "repos/test/phpm/releases/42"))
                assets = []
                for name in ("client.zip", "clients.json", "clients.json.sig"):
                    data = (root / name).read_bytes()
                    assets.append({"name": name, "size": len(data), "digest": "sha256:" + hashlib.sha256(data).hexdigest()})
                if bad_digest:
                    assets[0]["digest"] = "sha256:" + "0" * 64
                return json.dumps({"draft": True, "assets": assets, "html_url": "https://example.test/draft"})

            arguments = ["publish.py", "--repo", "test/phpm", "--tag", "candidate", "--bundle", str(root),
                         "--root", str(root / "root.pub"), "--fingerprint", "b" * 64, "--verifier", str(root / "verifier")]
            verified = json.dumps({"version": "0.1.0-test", "commit": "a" * 40})
            with patch.object(sys, "argv", arguments), patch.object(publisher, "gh", side_effect=gh), patch.object(publisher.subprocess, "check_output", return_value=verified):
                if existing or bad_digest:
                    with self.assertRaises(ValueError):
                        publisher.main()
                else:
                    publisher.main()
            if existing:
                self.assertFalse(any("--input" in r or r[:2] == ("release", "upload") for r in requests))

    def test_draft_by_id_and_exact_asset_digests(self):
        self.exercise()

    def test_public_repository_still_creates_only_a_draft(self):
        self.exercise(private=False)

    def test_existing_release_untouched(self):
        self.exercise(existing=True)

    def test_bad_remote_digest_is_not_success(self):
        self.exercise(bad_digest=True)


if __name__ == "__main__":
    unittest.main()
