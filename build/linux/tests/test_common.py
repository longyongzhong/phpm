import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
from common import extract_archive, load_recipe, pack_archive, relocate_sdk_script, sha256, verify_inputs


class RecipeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        for name in ("recipe.json", "configure-flags.txt", "build-packages.txt", "runtime-packages.txt"):
            shutil.copyfile(ROOT / name, self.root / name)
        shutil.copytree(ROOT / "keys", self.root / "keys")

    def change(self, key, value):
        recipe = json.loads((self.root / "recipe.json").read_text())
        if isinstance(key, tuple):
            recipe[key[0]][key[1]] = value
        else:
            recipe[key] = value
        (self.root / "recipe.json").write_text(json.dumps(recipe))

    def test_checked_in_recipe(self):
        self.assertEqual(load_recipe(self.root)["target"], "debian13-x86_64-nts")

    def test_refuses_mutable_base_or_bad_source_identity(self):
        for key, value in [("base_image", "debian:13-slim"), (("php", "url"), "https://evil.example/php.tar.xz"),
                           (("redis", "url"), "https://pecl.php.net/get/redis-6.3.0.tgz?override=1"),
                           (("composer", "sha256"), ""), (("php", "signer"), "600A39A6"),
                           (("php", "key"), "../../outside.asc"), ("build_revision", 0),
                           ("apt_snapshot", "latest")]:
            with self.subTest(key=key, value=value):
                shutil.copyfile(ROOT / "recipe.json", self.root / "recipe.json")
                self.change(key, value)
                with self.assertRaises(ValueError):
                    load_recipe(self.root)

    def test_nts_recipe_rejects_zts_and_package_option_injection(self):
        with (self.root / "configure-flags.txt").open("a") as stream:
            stream.write("\n--enable-zts\n")
        with self.assertRaises(ValueError):
            load_recipe(self.root)
        shutil.copyfile(ROOT / "configure-flags.txt", self.root / "configure-flags.txt")
        (self.root / "runtime-packages.txt").write_text("--allow-unauthenticated\n")
        with self.assertRaises(ValueError):
            load_recipe(self.root)


class ArchiveTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def archive(self, entries):
        target = self.root / "input.tar.gz"
        with tarfile.open(target, "w:gz") as archive:
            for name, kind, data in entries:
                entry = tarfile.TarInfo(name)
                entry.type = kind
                entry.mode = 0o755
                entry.size = len(data) if kind == tarfile.REGTYPE else 0
                entry.linkname = "../escaped"
                archive.addfile(entry, io.BytesIO(data) if entry.isfile() else None)
        return target

    def test_extract_regular_files_and_execute_bits(self):
        archive = self.archive([("bin/php", tarfile.REGTYPE, b"test")])
        dest = self.root / "out"
        extract_archive(archive, dest)
        self.assertEqual((dest / "bin/php").read_bytes(), b"test")
        self.assertEqual(int((dest / "bin/php").stat().st_mtime), 0)
        if os.name != "nt":
            self.assertTrue((dest / "bin/php").stat().st_mode & 0o111)

    def test_reject_paths_links_devices_duplicates_and_expansion(self):
        bad = [[("../escaped", tarfile.REGTYPE, b"x")], [("/absolute", tarfile.REGTYPE, b"x")],
               [("C:/escaped", tarfile.REGTYPE, b"x")], [("a\\b", tarfile.REGTYPE, b"x")],
               [("link", tarfile.SYMTYPE, b"")], [("link", tarfile.LNKTYPE, b"")],
               [("device", tarfile.CHRTYPE, b"")], [("fifo", tarfile.FIFOTYPE, b"")],
               [("file", tarfile.REGTYPE, b"1"), ("file", tarfile.REGTYPE, b"2")]]
        for i, entries in enumerate(bad):
            with self.subTest(entries=entries), self.assertRaises((ValueError, FileExistsError)):
                extract_archive(self.archive(entries), self.root / f"out-{i}")
        with self.assertRaises(ValueError):
            extract_archive(self.archive([("file", tarfile.REGTYPE, b"1234")]), self.root / "oversized", max_bytes=3)
        self.assertFalse((self.root / "escaped").exists())

    def test_pack_is_independent_of_source_mtime_and_output_name(self):
        payload = self.root / "payload"
        payload.mkdir()
        file = payload / "file"
        file.write_bytes(b"same contents")
        a, b = self.root / "a.tar.gz", self.root / "b.tar.gz"
        pack_archive(payload, a, 1234567890)
        os.utime(file, (1700000000, 1700000000))
        pack_archive(payload, b, 1234567890)
        self.assertEqual(sha256(a), sha256(b))


class SDKTests(unittest.TestCase):
    def test_patch_requires_expected_upstream_prefix(self):
        with self.assertRaises(ValueError):
            relocate_sdk_script("#!/bin/sh\nprefix=/somewhere-else\n")

    def test_relocated_script_resolves_its_actual_directory(self):
        shell = os.environ.get("PHPM_TEST_SH") or shutil.which("sh")
        if not shell:
            self.skipTest("a POSIX shell is required for the relocation execution check")
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp) / "sdk moved"
            (root / "bin").mkdir(parents=True)
            text = '#!/bin/sh\nprefix="/opt/phpm-build-prefix"\ninclude_dir="/opt/phpm-build-prefix/include/php"\nprintf "%s\\n%s\\n" "$prefix" "$include_dir"\n'
            script = root / "bin/php-config"
            script.write_text(relocate_sdk_script(text))
            expected = subprocess.check_output([shell, "-c", "pwd -P"], cwd=root, text=True).strip()
            actual = subprocess.check_output([shell, "bin/php-config"], cwd=root, text=True).splitlines()
            self.assertEqual(actual, [expected, expected + "/include/php"])


class SourceVerificationTests(unittest.TestCase):
    def test_modified_archive_is_rejected_before_any_gpg_execution(self):
        with tempfile.TemporaryDirectory() as temp:
            inputs = Path(temp)
            version = load_recipe(ROOT)["php"]["version"]
            (inputs / f"php-{version}.tar.xz").write_bytes(b"modified")
            with self.assertRaisesRegex(ValueError, "missing or modified php input"):
                verify_inputs(ROOT, inputs, gpg="must-not-run", gpgv="must-not-run")

    def test_optional_real_source_signature_and_invalid_signature(self):
        inputs = os.environ.get("PHPM_TEST_SOURCE_INPUTS")
        if not inputs:
            self.skipTest("set PHPM_TEST_SOURCE_INPUTS for real official-source signature checks")
        gpg = os.environ.get("PHPM_TEST_GPG", "gpg")
        gpgv = os.environ.get("PHPM_TEST_GPGV", "gpgv")
        verify_inputs(ROOT, Path(inputs), gpg, gpgv)
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            for source in Path(inputs).glob("*"):
                if source.is_file():
                    shutil.copyfile(source, directory / source.name)
            signature = directory / (Path(load_recipe(ROOT)["php"]["url"]).name + ".asc")
            signature.write_bytes(b"invalid release signature\n")
            with self.assertRaises(subprocess.CalledProcessError):
                verify_inputs(ROOT, directory, gpg, gpgv)


if __name__ == "__main__":
    unittest.main()
