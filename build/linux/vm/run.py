#!/usr/bin/env python3
"""Run owned, disposable Debian QEMU + MySQL acceptance; never use an existing VM/database."""

import argparse
import hashlib
import http.server
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import socket
import ssl
import subprocess
import sys
import tempfile
import threading
import time
from urllib.parse import urlparse
import uuid

ROOT = Path(__file__).resolve().parent
LINUX = ROOT.parent
PROJECT = LINUX.parent.parent
sys.path.insert(0, str(LINUX))
from common import load_recipe, output, read_lines, run, write_json


def load_settings(path=ROOT / "recipe.json"):
    data = json.loads(path.read_text())
    image = data["image"]
    url = urlparse(image["url"])
    if (data["schema"] != 1 or url.scheme != "https" or url.netloc != "cloud.debian.org"
            or not re.fullmatch(r"/images/cloud/trixie/(\d{8}-\d+)/debian-13-genericcloud-amd64-\1\.qcow2", url.path)
            or url.query or url.fragment or not re.fullmatch(r"[a-f0-9]{128}", image["sha512"])):
        raise ValueError("VM requires an exact official Debian cloud image URL and SHA-512")
    if not re.fullmatch(r"mysql:8\.4\.\d+@sha256:[a-f0-9]{64}", data["mysql_image"]):
        raise ValueError("MySQL must be an exact digest-pinned 8.4 official image")
    if not data["mysql_image"].startswith("mysql:" + data["mysql_version"] + "@"):
        raise ValueError("MySQL image/version mismatch")
    if (data["base_php"], data["target_php"], data["redis"]) != ("8.4.27", "8.5.12", "6.3.0"):
        raise ValueError("unsupported VM upgrade fixture versions")
    return data


def port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def download(url, path, algorithm, expected):
    part = path.with_suffix(path.suffix + ".part")
    run(["curl", "--fail", "--location", "--proto", "=https", "--proto-redir", "=https",
         "--retry", "3", "--connect-timeout", "20", "--max-time", "900",
         "--max-filesize", "1073741824", "--output", part, url])
    digest = hashlib.new(algorithm)
    with part.open("rb") as stream:
        for data in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(data)
    if digest.hexdigest() != expected:
        raise ValueError("download does not match the fixed " + algorithm)
    part.rename(path)


class BundleHandler(http.server.BaseHTTPRequestHandler):
    """Serve only the accepted bundle, including strictly bounded Range requests."""
    protocol_version = "HTTP/1.1"

    def do_GET(self):
        name = self.path.removeprefix("/")
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", name) or ".." in name:
            self.send_error(404)
            return
        path = self.server.bundle / name
        if not path.is_file():
            self.send_error(404)
            return
        total = path.stat().st_size
        start, end, status = 0, total - 1, 200
        requested = self.headers.get("Range")
        if requested:
            match = re.fullmatch(r"bytes=(\d+)-(\d+)", requested)
            if not match:
                self.send_error(416)
                return
            start, end = map(int, match.groups())
            end = min(end, total - 1)
            if start < 0 or start > end or end - start >= 1024 * 1024:
                self.send_error(416)
                return
            status = 206
        self.send_response(status)
        self.send_header("Content-Length", str(end - start + 1))
        self.send_header("Accept-Ranges", "bytes")
        if status == 206:
            self.send_header("Content-Range", f"bytes {start}-{end}/{total}")
        self.end_headers()
        with path.open("rb") as source:
            source.seek(start)
            remaining = end - start + 1
            while remaining:
                data = source.read(min(remaining, 65536))
                if not data:
                    raise OSError("bundle changed during transfer")
                self.wfile.write(data)
                remaining -= len(data)

    def log_message(self, *_):
        pass


def cloud_config(client_public, host_private, host_public, snapshot):
    packages = read_lines(LINUX / "runtime-packages.txt") + ["python3", "openssl", "redis-server", "libfcgi-bin", "iproute2"]
    setup = "set -eu\nexport DEBIAN_FRONTEND=noninteractive\n"
    setup += "printf '#!/bin/sh\\nexit 101\\n' > /usr/sbin/policy-rc.d\nchmod 755 /usr/sbin/policy-rc.d\n"
    sources = (f"Types: deb\nURIs: http://snapshot.debian.org/archive/debian/{snapshot}/\n"
               "Suites: trixie trixie-updates\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\nCheck-Valid-Until: no\n\n"
               f"Types: deb\nURIs: http://snapshot.debian.org/archive/debian-security/{snapshot}/\n"
               "Suites: trixie-security\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\nCheck-Valid-Until: no\n")
    setup += "cat > /etc/apt/sources.list.d/debian.sources <<'PHPM_SOURCES'\n" + sources + "PHPM_SOURCES\n"
    setup += "apt-get -o DPkg::Lock::Timeout=180 update\n"
    setup += "apt-get -o DPkg::Lock::Timeout=180 install -y --no-install-recommends " + " ".join(packages) + "\n"
    setup += "test ! -e /usr/bin/php\ntest ! -e /usr/bin/cc\ntouch /var/lib/phpm-vm-ready\n"
    return {"hostname": "phpm-ci-vm", "ssh_pwauth": False, "disable_root": True,
            "users": [{"name": "phpm-ci", "shell": "/bin/bash", "lock_passwd": True,
                       "sudo": "ALL=(ALL) NOPASSWD:ALL", "ssh_authorized_keys": [client_public]}],
            "ssh_keys": {"ed25519_private": host_private, "ed25519_public": host_public},
            "runcmd": [["bash", "-ceu", setup]]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", type=Path, required=True, help="accepted 8.4 stage-A directory")
    parser.add_argument("--target", type=Path, required=True, help="accepted 8.5 stage-A directory")
    parser.add_argument("--output", type=Path, required=True, help="new report directory")
    parser.add_argument("--soak-seconds", type=int, default=120, choices=(120, 600, 1800))
    args = parser.parse_args()
    settings, recipe = load_settings(), load_recipe(LINUX)
    if sys.platform != "linux" or os.uname().machine != "x86_64":
        raise ValueError("VM controller requires a Linux x86_64 host")
    for tool in ("go", "qemu-system-x86_64", "qemu-img", "cloud-localds", "ssh", "scp", "ssh-keygen", "openssl", "curl", "docker"):
        if not shutil.which(tool):
            raise ValueError("missing host test tool: " + tool)
    destination = args.output.resolve()
    if destination.exists() or any(c in str(destination) for c in ",\n\r"):
        raise ValueError("output must be a new directory without QEMU delimiters")
    destination.mkdir(parents=True)
    reports = [json.loads((p / "build-result.json").read_text()) for p in (args.base, args.target)]
    if [r["php_version"] for r in reports] != [settings["base_php"], settings["target_php"]]:
        raise ValueError("expected accepted PHP 8.4.27 then 8.5.12 packages")
    with tempfile.TemporaryDirectory(prefix=".phpm-vm-", dir=destination.parent) as temporary:
        work = Path(temporary).resolve()
        if work.parent != destination.parent:
            raise ValueError("VM temporary directory escaped output parent")
        payload = work / "payload"
        payload.mkdir()
        go_env = dict(os.environ, CGO_ENABLED="0", GOAMD64="v1")
        run(["go", "build", "-trimpath", "-o", payload / "phpm", "./cmd/phpm"], cwd=PROJECT, env=go_env)
        run(["go", "build", "-trimpath", "-o", work / "phpm-release", "./cmd/phpm-release"], cwd=PROJECT, env=go_env)
        run([work / "phpm-release", "keygen", "--private", work / "signing.seed", "--public", work / "signing.pub"])
        bundle = work / "bundle"
        run([work / "phpm-release", "catalog", "--key", work / "signing.seed", "--sequence", "1",
             "--output", bundle, args.base.resolve(), args.target.resolve()])
        (work / "signing.seed").unlink()
        shutil.copyfile(bundle / "signing-key.pub", payload / "signing-key.pub")
        shutil.copyfile(ROOT / "guest.py", payload / "guest.py")
        download(recipe["composer"]["url"], payload / "composer.phar", "sha256", recipe["composer"]["sha256"])
        image = work / "base.qcow2"
        download(settings["image"]["url"], image, "sha512", settings["image"]["sha512"])
        run(["qemu-img", "create", "-f", "qcow2", "-F", "qcow2", "-b", image, work / "guest.qcow2", "12G"])
        for name in ("client", "host"):
            run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", work / name])
        config = cloud_config((work / "client.pub").read_text().strip(), (work / "host").read_text(),
                              (work / "host.pub").read_text().strip(), recipe["apt_snapshot"])
        (work / "user-data").write_text("#cloud-config\n" + json.dumps(config, indent=2) + "\n")
        write_json(work / "meta-data", {"instance-id": "phpm-" + uuid.uuid4().hex, "local-hostname": "phpm-ci-vm"})
        run(["cloud-localds", work / "seed.img", work / "user-data", work / "meta-data"])
        ssh_port, tls_port = port(), port()
        (work / "known_hosts").write_text(f"[127.0.0.1]:{ssh_port} " + (work / "host.pub").read_text())
        ssh_options = ["-i", work / "client", "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes",
                       "-o", "StrictHostKeyChecking=yes", "-o", f"UserKnownHostsFile={work / 'known_hosts'}",
                       "-o", "ConnectTimeout=5", "-o", "ServerAliveInterval=10", "-o", "ServerAliveCountMax=3"]
        ssh = ["ssh", *ssh_options, "-p", str(ssh_port), "phpm-ci@127.0.0.1"]
        scp = ["scp", *ssh_options, "-P", str(ssh_port)]
        run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
             "-keyout", work / "tls.key", "-out", payload / "fixture-ca.pem", "-subj", "/CN=phpm-vm-fixture",
             "-addext", "subjectAltName=IP:10.0.2.2"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        server = http.server.ThreadingHTTPServer(("127.0.0.1", tls_port), BundleHandler)
        server.bundle = bundle
        tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        tls.load_cert_chain(payload / "fixture-ca.pem", work / "tls.key")
        server.socket = tls.wrap_socket(server.socket, server_side=True)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        mysql_name = "phpm-vm-mysql-" + uuid.uuid4().hex[:12]
        mysql_created, vm, qemu_log = False, None, None
        try:
            db_password = secrets.token_urlsafe(24)
            mysql_env = work / "mysql.env"
            mysql_env.write_text(f"MYSQL_ROOT_PASSWORD={secrets.token_urlsafe(24)}\nMYSQL_DATABASE=phpm_ci\nMYSQL_USER=phpm_ci\nMYSQL_PASSWORD={db_password}\n")
            mysql_env.chmod(0o600)
            run(["docker", "run", "--detach", "--rm", "--name", mysql_name, "--publish", "127.0.0.1::3306",
                 "--env-file", mysql_env, settings["mysql_image"]])
            mysql_created = True
            mysql_deadline = time.monotonic() + 180
            while time.monotonic() < mysql_deadline:
                ready = subprocess.run(["docker", "exec", mysql_name, "sh", "-c",
                    'MYSQL_PWD="$MYSQL_PASSWORD" mysql -h127.0.0.1 -u"$MYSQL_USER" "$MYSQL_DATABASE" -Nse "SELECT VERSION()"'],
                    text=True, capture_output=True, timeout=10)
                if ready.returncode == 0 and ready.stdout.strip().startswith(settings["mysql_version"]):
                    break
                time.sleep(2)
            else:
                raise ValueError("owned MySQL instance did not initialize at the pinned version")
            binding = output(["docker", "port", mysql_name, "3306/tcp"])
            if not re.fullmatch(r"127\.0\.0\.1:\d+", binding):
                raise ValueError("MySQL must bind only to host loopback")
            write_json(payload / "context.json", {"mysql": {"host": "10.0.2.2", "port": int(binding.split(":")[1]),
                "database": "phpm_ci", "user": "phpm_ci", "password": db_password, "version": settings["mysql_version"]},
                "catalog_url": f"https://10.0.2.2:{tls_port}/catalog.json", "base": settings["base_php"],
                "target": settings["target_php"], "redis": settings["redis"], "soak_seconds": args.soak_seconds,
                "host_boot_id": Path("/proc/sys/kernel/random/boot_id").read_text().strip()})
            (payload / "context.json").chmod(0o600)
            (destination / "vm-serial.log").touch()
            qemu_log = (destination / "qemu.log").open("w")
            vm = subprocess.Popen(["qemu-system-x86_64", "-machine", "q35", "-accel", "tcg,thread=multi",
                "-cpu", "max", "-smp", "2", "-m", "2048", "-display", "none", "-monitor", "none",
                "-serial", f"file:{destination / 'vm-serial.log'}", "-no-reboot",
                "-drive", f"file={work / 'guest.qcow2'},if=virtio,format=qcow2",
                "-drive", f"file={work / 'seed.img'},if=virtio,format=raw,readonly=on",
                "-netdev", f"user,id=net0,hostfwd=tcp:127.0.0.1:{ssh_port}-:22",
                "-device", "virtio-net-pci,netdev=net0"], stdout=qemu_log, stderr=subprocess.STDOUT)
            boot_deadline = time.monotonic() + 900
            while time.monotonic() < boot_deadline:
                if vm.poll() is not None:
                    raise ValueError("QEMU exited before guest setup completed")
                ready = subprocess.run([*ssh, "test -f /var/lib/phpm-vm-ready"], capture_output=True, timeout=12)
                if ready.returncode == 0:
                    break
                time.sleep(5)
            else:
                raise ValueError("Debian VM boot/setup exceeded its deadline; inspect vm-serial.log")
            run([*ssh, "mkdir -m 700 -p fixture"])
            run([*scp, *sorted(payload.iterdir()), "phpm-ci@127.0.0.1:fixture/"], timeout=180)
            # Leave the connected host/guest network, remove internet routing.
            run([*ssh, "sudo ip route del default && ! ip route show default | grep -q ."])
            with (destination / "guest-test.log").open("w") as log:
                completed = subprocess.run([*ssh, "python3 fixture/guest.py"], stdout=log, stderr=subprocess.STDOUT,
                                           timeout=args.soak_seconds + 900)
            if completed.returncode:
                raise ValueError("guest acceptance failed; inspect guest-test.log")
            run([*scp, "phpm-ci@127.0.0.1:fixture/report.json", destination / "guest-report.json"])
            report = json.loads((destination / "guest-report.json").read_text())
            if report["versions"] != [settings["base_php"], settings["target_php"], settings["base_php"]]:
                raise ValueError("guest did not complete the exact real-version lifecycle")
            write_json(destination / "acceptance.json", {"schema": 1, "status": "passed", "settings": settings,
                "source_builds": [{"id": r["build_id"], "php": r["php_version"]} for r in reports],
                "host_kernel": os.uname().release, "qemu": output(["qemu-system-x86_64", "--version"]).splitlines()[0],
                "guest": report, "scope": "built-in isolated acceptance; no user business project"})
            print("Independent Debian VM, real MySQL and real PHP upgrade/rollback: PASSED", flush=True)
        finally:
            if vm is not None:
                vm.terminate()
                try:
                    vm.wait(timeout=20)
                except subprocess.TimeoutExpired:
                    vm.kill()
                    vm.wait(timeout=5)
            if mysql_created:
                subprocess.run(["docker", "rm", "--force", mysql_name], check=True, stdout=subprocess.DEVNULL)
            if qemu_log is not None:
                qemu_log.close()
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(f"phpm VM acceptance: {error}", file=sys.stderr)
        sys.exit(1)
