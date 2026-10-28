import http.client
import http.server
import importlib.util
import json
from pathlib import Path
import tempfile
import threading
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("vm_acceptance", ROOT / "vm/run.py")
vm = importlib.util.module_from_spec(spec)
spec.loader.exec_module(vm)


class VMRecipeTests(unittest.TestCase):
    def test_checked_in_vm_image_and_mysql_are_fixed(self):
        settings = vm.load_settings()
        self.assertIn("20261026-2643", settings["image"]["url"])
        self.assertEqual(settings["target_php"], "8.5.12")

    def test_reject_mutable_or_mismatched_inputs(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "recipe.json"
            for key, value in [("mysql_image", "mysql:8.4"), ("mysql_version", "8.4.1"), ("base_php", "8.4"),
                               ("url", "https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2"),
                               ("sha512", "abc")]:
                data = vm.load_settings()
                if key in ("url", "sha512"):
                    data["image"][key] = value
                else:
                    data[key] = value
                path.write_text(json.dumps(data))
                with self.subTest(key=key), self.assertRaises(ValueError):
                    vm.load_settings(path)

    def test_cloud_init_uses_only_fixture_keys_and_signed_package_sources(self):
        config = vm.cloud_config("fixture-client-public", "fixture-host-private", "fixture-host-public", "20261028T030000Z")
        setup = config["runcmd"][0][2]
        self.assertFalse(config["ssh_pwauth"])
        self.assertTrue(config["disable_root"])
        self.assertIn("debian-archive-keyring.gpg", setup)
        self.assertNotIn("AllowUnauthenticated", setup)
        self.assertNotIn("fixture-host-private", setup)
        self.assertIn("test ! -e /usr/bin/php", setup)


class BundleRangeTests(unittest.TestCase):
    def test_http_ranges_and_path_boundary(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            data = b"x" * (2 * 1024 * 1024)
            (root / "runtime.tar.gz").write_bytes(data)
            server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), vm.BundleHandler)
            server.bundle = root
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            connection = http.client.HTTPConnection("127.0.0.1", server.server_port, timeout=5)
            try:
                connection.request("GET", "/runtime.tar.gz", headers={"Range": "bytes=7-1048582"})
                response = connection.getresponse()
                self.assertEqual(response.status, 206)
                self.assertEqual(response.getheader("Content-Range"), f"bytes 7-1048582/{len(data)}")
                self.assertEqual(len(response.read()), 1024 * 1024)
                for path, header, status in [("/../private.key", {}, 404), ("/runtime.tar.gz?token=x", {}, 404),
                        ("/runtime.tar.gz", {"Range": "bytes=0-2097151"}, 416),
                        ("/runtime.tar.gz", {"Range": "bytes=-50"}, 416)]:
                    connection.request("GET", path, headers=header)
                    response = connection.getresponse()
                    self.assertEqual(response.status, status)
                    response.read()
            finally:
                connection.close()
                server.shutdown()
                server.server_close()
                thread.join(timeout=5)


if __name__ == "__main__":
    unittest.main()
