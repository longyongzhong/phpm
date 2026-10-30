import argparse
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from offline import build_directories, hash_file, inspect_materials, NOTICES, safe_path, sign


class OfflineMaterialsTests(unittest.TestCase):
    def test_optional_debian12_inputs_require_the_complete_pair(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            for role in ("base", "target"):
                (root / "inputs" / role).mkdir(parents=True)
            self.assertEqual(len(build_directories(root)), 2)
            (root / "inputs/debian12-base").mkdir()
            with self.assertRaises(ValueError):
                build_directories(root)
            (root / "inputs/debian12-target").mkdir()
            self.assertEqual(len(build_directories(root)), 4)
            (root / "inputs/unexpected").mkdir()
            with self.assertRaises(ValueError):
                build_directories(root)

    def materials(self, root):
        names = ["clients/linux-amd64/phpm", "clients/windows-amd64/phpm.exe", "tools/linux-amd64/phpm-release",
                 "tools/windows-amd64/phpm-release.exe", "install-phpm.py", "offline-sign.py"]
        names += ["notices/" + name for name in NOTICES]
        files = {}
        for name in names:
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(b"unit fixture, not a real signing tool")
            files[name] = hash_file(path)
        request = {"schema": 1, "version": "0.1.0-rc.1", "commit": "a" * 40,
                   "repository": "test/phpm", "source_runs": [], "files": files}
        raw = json.dumps(request).encode()
        (root / "release-inputs.json").write_bytes(raw)
        return hashlib.sha256(raw).hexdigest()

    def test_check_all_files_and_reject_extra_private_file(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            digest = self.materials(root)
            self.assertEqual(inspect_materials(root, digest)["version"], "0.1.0-rc.1")
            (root / "private.seed").write_bytes(b"must not be accepted")
            with self.assertRaisesRegex(ValueError, "unexpected"):
                inspect_materials(root, digest)

    def test_request_and_binary_tampering_before_any_execution(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            digest = self.materials(root)
            with self.assertRaisesRegex(ValueError, "request SHA-256"):
                inspect_materials(root, "b" * 64)
            (root / "tools/windows-amd64/phpm-release.exe").write_bytes(b"changed")
            with patch("offline.subprocess.run") as run:
                with self.assertRaisesRegex(ValueError, "hash/size"):
                    sign(argparse.Namespace(input=root, request_sha256=digest))
                run.assert_not_called()

    def test_no_path_traversal_or_windows_drive_paths(self):
        for name in ("../private.seed", "/etc/passwd", "C:/private.seed", "a/../../private.seed", "tools\\private.seed", "tools//phpm", "tools/./phpm"):
            with self.subTest(name=name), self.assertRaises(ValueError):
                safe_path(name)

    def test_request_cannot_be_a_symlink(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            digest = self.materials(root)
            original = root / "release-inputs.json"
            moved = root / "real.json"
            original.rename(moved)
            try:
                original.symlink_to(moved)
            except OSError:
                self.skipTest("symlink creation unavailable on this host")
            with self.assertRaisesRegex(ValueError, "regular file"):
                inspect_materials(root, digest)


if __name__ == "__main__":
    unittest.main()
