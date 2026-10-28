"""Hostile archive cases for the bootstrap installer, no network or private keys."""
import importlib.util
import io
from pathlib import Path
import tarfile
import tempfile
import unittest
import zipfile

spec = importlib.util.spec_from_file_location("installer", Path(__file__).with_name("install-phpm.py"))
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)


class InstallerTests(unittest.TestCase):
    def test_ambiguous_metadata(self):
        for data in (b'{"schema":1,"schema":1}', b'{"schema":1,"extra":0}', b'{"schema":1} {}'):
            with self.assertRaises(ValueError):
                installer.document(data, ("schema",))

    def test_tar_links_paths_duplicates_and_missing_licenses(self):
        for kind in ("escape", "link", "hardlink", "duplicate", "missing"):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                archive = root / "client.tar.gz"
                with tarfile.open(archive, "w:gz") as out:
                    entry = tarfile.TarInfo("../escape" if kind == "escape" else "phpm")
                    entry.size = 1
                    if kind in ("link", "hardlink"):
                        entry.type = tarfile.SYMTYPE if kind == "link" else tarfile.LNKTYPE
                        entry.linkname = "../../escape"
                        entry.size = 0
                    out.addfile(entry, io.BytesIO(b"x"))
                    if kind == "duplicate":
                        out.addfile(entry, io.BytesIO(b"x"))
                destination = root / "out"
                destination.mkdir()
                with self.assertRaises(ValueError):
                    installer.extract(archive, destination, "linux-amd64")
                self.assertFalse((root / "escape").exists())

    def test_zip_link_and_traversal(self):
        for name, mode in (("../escape", 0o100644), ("phpm.exe", 0o120777)):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                archive = root / "client.zip"
                with zipfile.ZipFile(archive, "w") as out:
                    info = zipfile.ZipInfo(name)
                    info.create_system = 3
                    info.external_attr = mode << 16
                    out.writestr(info, b"../../escape")
                destination = root / "out"
                destination.mkdir()
                with self.assertRaises(ValueError):
                    installer.extract(archive, destination, "windows-amd64")
                self.assertFalse((root / "escape").exists())

    def test_source_rejects_unsafe_locations_and_names(self):
        for location in ("http://example.com", "https://user:secret@example.com", "https://example.com/?token=secret"):
            with self.assertRaises(ValueError):
                installer.Source(location)
        with tempfile.TemporaryDirectory() as temp:
            with self.assertRaises(ValueError):
                installer.Source(temp).read("../private.seed", 1024)


if __name__ == "__main__":
    unittest.main()
