import argparse
import base64
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import github_sign


class GitHubSigningTests(unittest.TestCase):
    def args(self, root):
        return argparse.Namespace(materials=root / "materials", request_sha256="a" * 64,
            expected_commit="b" * 40, expected_repo="test/phpm", expected_version="0.1.0-rc.1",
            trust=root / "trust", sequence=1, validity="1h", output=root / "signed")

    def request(self):
        return {"commit": "b" * 40, "repository": "test/phpm", "version": "0.1.0-rc.1"}

    def test_only_publishing_key_file_and_cleanup_even_on_failure(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            args = self.args(root)
            args.trust.mkdir()
            (args.trust / "fingerprint.txt").write_text("c" * 64)
            encoded = base64.b64encode(b"\x24" * 32).decode()
            observed = []
            def signer(options):
                self.assertNotIn("PHPM_RELEASE_SEED", os.environ)
                self.assertEqual(options.key.read_text().strip(), encoded)
                self.assertNotEqual(options.key.parent, args.output)
                observed.append(options.key)
                raise ValueError("unit signing failure")
            with patch.dict(os.environ, {"PHPM_RELEASE_SEED": encoded}), patch.object(github_sign, "inspect_materials", return_value=self.request()), patch.object(github_sign, "sign", side_effect=signer):
                with self.assertRaisesRegex(ValueError, "unit signing failure"):
                    github_sign.github_sign(args)
            self.assertEqual(len(observed), 1)
            self.assertFalse(observed[0].exists())

    def test_wrong_commit_repo_or_version_refused_before_key_file_or_tool(self):
        for field, value in (("commit", "d" * 40), ("repository", "other/repo"), ("version", "9.9.9")):
            request = self.request()
            request[field] = value
            with self.subTest(field=field), patch.dict(os.environ, {"PHPM_RELEASE_SEED": "not a real seed"}), patch.object(github_sign, "inspect_materials", return_value=request), patch.object(github_sign, "sign") as signer:
                with self.assertRaisesRegex(ValueError, "materials do not match"):
                    github_sign.github_sign(self.args(Path("unit")))
                signer.assert_not_called()

    def test_missing_or_invalid_secret_is_a_failure(self):
        for encoded in ("", "invalid seed", base64.b64encode(b"too short").decode()):
            with self.subTest(encoded=bool(encoded)), patch.dict(os.environ, {"PHPM_RELEASE_SEED": encoded}), patch.object(github_sign, "inspect_materials", return_value=self.request()), patch.object(github_sign, "sign") as signer:
                with self.assertRaisesRegex(ValueError, "PHPM_RELEASE_SEED"):
                    github_sign.github_sign(self.args(Path("unit")))
                signer.assert_not_called()


if __name__ == "__main__":
    unittest.main()
