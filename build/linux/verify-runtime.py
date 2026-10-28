#!/usr/bin/env python3
"""Exercise extracted packages as an unprivileged user without system PHP."""

from contextlib import contextmanager
import http.server
import json
import os
from pathlib import Path
import shutil
import socket
import ssl
import subprocess
import tempfile
import threading
import time

from common import extract_archive, output, run, sha256, write_json


class TLSProbeHandler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self):
        payload = b"phpm-https-ok"
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        # OpenSSL 3 rejects a missing TLS close_notify. Delimit the HTTP body
        # explicitly so clients do not depend on socket EOF to finish reading.
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


@contextmanager
def service(args, log, **kwargs):
    with log.open("w+") as stream:
        process = subprocess.Popen(list(map(str, args)), stdout=stream, stderr=stream, **kwargs)
        try:
            yield process
        finally:
            if process.poll() is None:
                process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
            stream.seek(0)
            print(stream.read(), flush=True)


def wait_for_socket(process, address, unix=False):
    for _ in range(100):
        if process.poll() is not None:
            raise ValueError("test service exited before becoming ready")
        try:
            with socket.socket(socket.AF_UNIX if unix else socket.AF_INET) as client:
                client.settimeout(0.1)
                client.connect(str(address) if unix else address)
                return
        except OSError:
            time.sleep(0.1)
    raise ValueError("test service did not become ready")


def main():
    if os.geteuid() == 0 or shutil.which("php") or shutil.which("cc") or shutil.which("make"):
        raise ValueError("runtime checks require a non-root user and no system PHP/compiler")
    packages = Path("/packages")
    result = json.loads((packages / "build-result.json").read_text())
    checks = []
    with tempfile.TemporaryDirectory(prefix="phpm-runtime-check-") as temp:
        root = Path(temp)
        extracted = {}
        for item in result["artifacts"]:
            archive = packages / item["filename"]
            if sha256(archive) != item["sha256"] or archive.stat().st_size != item["size"]:
                raise ValueError("package digest/size mismatch")
            if item["kind"] in ("runtime", "redis"):
                # A path containing spaces exercises runtime/INI path handling.
                dest = root / (item["kind"] + " relocated")
                extract_archive(archive, dest)
                extracted[item["kind"]] = dest
        runtime = extracted["runtime"]
        php = runtime / "bin/php"
        identity = json.loads(output([php, "-n", "-r",
            "echo json_encode(['version'=>PHP_VERSION,'zts'=>PHP_ZTS,'debug'=>PHP_DEBUG,'size'=>PHP_INT_SIZE]);"]))
        if identity != {"version": result["php_version"], "zts": 0, "debug": 0, "size": 8}:
            raise ValueError("PHP identity mismatch")
        if sha256(php) != result["php_binary_sha256"]:
            raise ValueError("PHP binary differs from build receipt")
        for elf in [php, runtime / "sbin/php-fpm", *runtime.glob("ext/*.so"), extracted["redis"] / "redis.so"]:
            linked = output(["ldd", elf], stderr=subprocess.STDOUT)
            if "not found" in linked or "/work/" in linked or "/opt/phpm-build-prefix" in linked:
                raise ValueError(f"unresolved/build-directory library dependency: {elf}")
        checks += ["archive-integrity", "non-root-relocated-cli", "php-identity", "runtime-library-resolution"]

        project = root / "project"
        project.mkdir()
        empty_scan = root / "empty-scan"
        empty_scan.mkdir()
        env = dict(os.environ, PHPM_HOME=str(root / "home"), PHPM_COMPOSER="/fixtures/composer.phar",
                   COMPOSER_HOME=str(root / "composer-home"), COMPOSER=str(project / "composer.json"),
                   PHPRC=str(empty_scan), PHP_INI_SCAN_DIR=str(empty_scan))
        cli = Path("/verify/phpm")

        def command(*args, cwd=project):
            print("+ phpm", " ".join(map(str, args)), flush=True)
            completed = subprocess.run([str(cli), *map(str, args)], cwd=cwd, env=env,
                                       text=True, capture_output=True, timeout=120)
            print(completed.stdout, end="", flush=True)
            if completed.stderr:
                print(completed.stderr, end="", flush=True)
            completed.check_returncode()
            return completed.stdout.strip()

        def php_code(code, *args):
            return command("exec", "--", "php", "-r", code, "--", *args)

        command("php", "import", runtime)
        command("init", "--php", "8.4")
        command("install")
        for module in sorted(runtime.glob("ext/*.so")):
            command("ext", "install", module.stem)
        redis = extracted["redis"] / "redis.so"
        command("ext", "import", f"redis@{result['recipe']['redis']['version']}",
                "--file", redis, "--sha256", sha256(redis))
        # ext import populates the store; declare the imported module separately.
        selected = json.loads(command("info"))
        extensions = {item["name"]: item["version"] for item in selected["extensions"]}
        extensions["redis"] = result["recipe"]["redis"]["version"]
        # JSON is valid YAML; this is an owned fixture, not an edit of a user file.
        write_json(project / "phpm.yaml", {"schema": 1, "php": "8.4", "runtime": selected["runtime"]["id"],
                   "extensions": extensions, "ini": {"memory_limit": "192M"}})
        command("install")
        command("verify")
        command("install", "--frozen")
        if php_code("echo (new PDO('sqlite::memory:'))->query('SELECT 42')->fetchColumn();") != "42":
            raise ValueError("PDO SQLite query failed")
        if php_code("echo gethostbyname('localhost');") != "127.0.0.1":
            raise ValueError("localhost DNS resolution failed")
        checks += ["phpm-import", "bundled-extension-install", "separate-redis-import", "frozen-install", "pdo-sqlite", "localhost-dns"]
        other = root / "other-project"
        other.mkdir()
        write_json(other / "phpm.yaml", {"schema": 1, "php": "8.4", "runtime": selected["runtime"]["id"],
                   "ini": {"memory_limit": "256M"}})
        command("install", cwd=other)
        isolated = json.loads(command("exec", "--", "php", "-r",
                              "echo json_encode([extension_loaded('redis'),ini_get('memory_limit')]);", cwd=other))
        if isolated != [False, "256M"] or php_code("echo ini_get('memory_limit');") != "192M":
            raise ValueError("project extension/INI isolation failed")
        checks.append("two-project-extension-and-ini-isolation")

        socket_path = root / "redis.sock"
        with service(["redis-server", "--port", "0", "--unixsocket", socket_path,
                      "--unixsocketperm", "700", "--save", "", "--appendonly", "no"], root / "redis.log") as process:
            wait_for_socket(process, socket_path, unix=True)
            response = php_code("$r=new Redis();$r->connect($argv[1]);$r->set('phpm-test','ok');echo $r->get('phpm-test');", socket_path)
            if response != "ok":
                raise ValueError("Redis server roundtrip failed")
        checks.append("redis-server-roundtrip")

        cert, key = root / "cert.pem", root / "key.pem"
        run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
             "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost",
             "-keyout", key, "-out", cert], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

        server = http.server.HTTPServer(("127.0.0.1", 0), TLSProbeHandler)
        tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        tls.load_cert_chain(cert, key)
        server.socket = tls.wrap_socket(server.socket, server_side=True)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            url = f"https://localhost:{server.server_port}/"
            response = php_code("$c=curl_init($argv[1]);curl_setopt_array($c,[CURLOPT_RETURNTRANSFER=>true,CURLOPT_CAINFO=>$argv[2],CURLOPT_TIMEOUT=>10]);$s=curl_exec($c);if($s===false){fwrite(STDERR,curl_error($c));exit(1);}echo $s;", url, cert)
            if response != "phpm-https-ok":
                raise ValueError("cURL HTTPS response mismatch")
            response = php_code("$c=stream_context_create(['ssl'=>['cafile'=>$argv[2]]]);echo file_get_contents($argv[1],false,$c);", url, cert)
            if response != "phpm-https-ok":
                raise ValueError("OpenSSL HTTPS stream response mismatch")
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)
        checks += ["curl-https-with-ca-verification", "openssl-https-with-ca-verification"]

        write_json(project / "composer.json", {"name": "phpm/source-build-test", "version": "1.0.0",
                   "require": {"php": "^8.4", **{f"ext-{name}": "*" for name in
                               ("json", "curl", "openssl", "mbstring", "pdo_mysql", "redis", "zip")}}})
        command("ext", "check")
        command("ext", "install", "--missing")
        command("composer", "--version")
        checks.append("composer-platform-check")

        current = json.loads(command("info"))
        imported = Path(php_code("echo PHP_BINARY;")).parent.parent
        ini = Path(env["PHPM_HOME"]) / "environments" / current["id"] / "php.ini"
        request = root / "index.php"
        request.write_text("<?php echo json_encode(['sapi'=>PHP_SAPI,'redis'=>extension_loaded('redis'),'memory'=>ini_get('memory_limit')]);")
        with socket.socket() as reservation:
            reservation.bind(("127.0.0.1", 0))
            port = reservation.getsockname()[1]
        fpm_config = root / "fpm.conf"
        fpm_config.write_text(f"[global]\nerror_log = {root / 'fpm-error.log'}\ndaemonize = no\n[phpm-test]\nlisten = 127.0.0.1:{port}\npm = static\npm.max_children = 1\ncatch_workers_output = yes\n")
        with service([imported / "sbin/php-fpm", "-c", ini, "-y", fpm_config, "--nodaemonize"], root / "fpm.log", env=env) as process:
            wait_for_socket(process, ("127.0.0.1", port))
            request_env = dict(env, SCRIPT_FILENAME=str(request), SCRIPT_NAME="/index.php", REQUEST_METHOD="GET",
                               REQUEST_URI="/index.php", QUERY_STRING="", CONTENT_LENGTH="0",
                               GATEWAY_INTERFACE="CGI/1.1", SERVER_PROTOCOL="HTTP/1.1", REDIRECT_STATUS="200")
            response = output(["cgi-fcgi", "-bind", "-connect", f"127.0.0.1:{port}"], env=request_env, timeout=15)
            if json.loads(response.split("\n\n", 1)[1]) != {"sapi": "fpm-fcgi", "redis": True, "memory": "192M"}:
                raise ValueError("FPM request did not use the project extension environment")
        checks.append("non-root-fpm-request-with-project-extensions")

        # Model two payload revisions, not two distinct upstream PHP versions.
        before = {name: (project / name).read_bytes() for name in ("phpm.yaml", "phpm.lock")}
        alternate = root / "runtime-revision-b"
        shutil.copytree(runtime, alternate)
        (alternate / "revision-fixture.txt").write_text("isolated upgrade lifecycle test\n")
        command("php", "import", alternate)
        manifests = list((Path(env["PHPM_HOME"]) / "store/runtimes").glob("*/manifest.json"))
        alternate_id = next(json.loads(path.read_text())["id"] for path in manifests
                            if json.loads(path.read_text())["id"] != current["runtime"]["id"])
        command("ext", "import", f"redis@{result['recipe']['redis']['version']}",
                "--file", redis, "--runtime", alternate_id, "--sha256", sha256(redis))
        command("php", "upgrade", "--runtime", alternate_id)
        command("verify", "--candidate")
        candidate = json.loads(command("info", "--candidate"))
        command("ext", "check", "--candidate")
        if any((project / name).read_bytes() != data for name, data in before.items()):
            raise ValueError("candidate preparation changed active project files")
        command("activate", candidate["candidate"])
        if json.loads(command("info"))["runtime"]["id"] != alternate_id:
            raise ValueError("activation did not select the candidate runtime")
        command("rollback")
        command("verify")
        if any((project / name).read_bytes() != data for name, data in before.items()):
            raise ValueError("rollback did not restore exact project files")
        checks.append("same-version-payload-activation-rollback")
        write_json(Path("/verification/runtime-verification.json"), {"schema": 1, "build_id": result["build_id"],
                   "checks": checks, "limitations": ["container shares host kernel", "no actual MySQL server test",
                   "upgrade fixture uses the same PHP version", "no production service switch", "unsigned stage-A artifacts"]})
    print("Fresh Debian runtime verification passed.", flush=True)


if __name__ == "__main__":
    main()
