#!/usr/bin/env python3
"""Built-in VM acceptance only; never reads a user project or production database."""

from contextlib import contextmanager, ExitStack
import json
import os
from pathlib import Path
import platform
import selectors
import shutil
import socket
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parent

PROBE = r'''<?php
$cfg=json_decode(file_get_contents(__DIR__.'/test-db.json'),true,512,JSON_THROW_ON_ERROR);
$phase=PHP_SAPI==='cli'?($argv[1]??'request'):($_SERVER['QUERY_STRING']?:'fpm');
$db=$cfg['mysql'];
$pdo=new PDO('mysql:host='.$db['host'].';port='.$db['port'].';dbname='.$db['database'].';charset=utf8mb4',
    $db['user'],$db['password'],[PDO::ATTR_ERRMODE=>PDO::ERRMODE_EXCEPTION,PDO::ATTR_EMULATE_PREPARES=>false]);
$server=(string)$pdo->query('SELECT VERSION()')->fetchColumn();
if(!str_starts_with($server,$db['version']))throw new RuntimeException('Wrong database server version');
$pdo->exec('CREATE TABLE IF NOT EXISTS phpm_upgrade_probe (id INT PRIMARY KEY, payload VARCHAR(255) NOT NULL) ENGINE=InnoDB');
if($phase==='baseline'){
    $pdo->exec('DELETE FROM phpm_upgrade_probe');
    $q=$pdo->prepare('INSERT INTO phpm_upgrade_probe VALUES (?,?)');$q->execute([1,'PHP升级验收 🐘']);
}
$persisted=$pdo->query('SELECT payload FROM phpm_upgrade_probe WHERE id=1')->fetchColumn();
if($persisted!=='PHP升级验收 🐘')throw new RuntimeException('MySQL data did not survive runtime switching');
$pdo->beginTransaction();
$q=$pdo->prepare('INSERT INTO phpm_upgrade_probe VALUES (?,?)');$q->execute([999,'must roll back']);
$pdo->rollBack();
if((int)$pdo->query('SELECT COUNT(*) FROM phpm_upgrade_probe WHERE id=999')->fetchColumn()!==0)
    throw new RuntimeException('MySQL transaction rollback failed');
$redis=new Redis();$redis->connect($cfg['redis_socket']);
if($phase==='baseline')$redis->set('phpm-upgrade-persist','same-value');
if($redis->get('phpm-upgrade-persist')!=='same-value')throw new RuntimeException('Redis data did not survive');
$redis->set('phpm-roundtrip',$phase);
if($redis->get('phpm-roundtrip')!==$phase)throw new RuntimeException('Redis roundtrip failed');
echo json_encode(['php'=>PHP_VERSION,'sapi'=>PHP_SAPI,'mysql'=>$server,'transaction'=>'rolled-back',
    'redis'=>phpversion('redis'),'redis_readwrite'=>true,'utf8mb4'=>true,
    'opcache'=>extension_loaded('Zend OPcache'),'memory'=>ini_get('memory_limit')],JSON_THROW_ON_ERROR);
'''


@contextmanager
def service(args, log, env=None):
    with log.open("w") as stream:
        process = subprocess.Popen(list(map(str, args)), stdout=stream, stderr=subprocess.STDOUT, env=env)
        try:
            yield process
        finally:
            if process.poll() is None:
                process.terminate()
            try:
                process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)


def wait_socket(process, path):
    for _ in range(100):
        if process.poll() is not None:
            raise ValueError("test service exited before its socket was ready")
        try:
            with socket.socket(socket.AF_UNIX) as client:
                client.settimeout(0.2)
                client.connect(str(path))
                return
        except OSError:
            time.sleep(0.1)
    raise ValueError("test service socket did not become ready")


def main():
    context = json.loads((ROOT / "context.json").read_text())
    if os.geteuid() == 0 or platform.system() != "Linux" or platform.machine() != "x86_64":
        raise ValueError("guest acceptance requires non-root Linux x86_64")
    for tool in ("php", "cc", "make"):
        if shutil.which(tool):
            raise ValueError("guest unexpectedly has system PHP/compiler: " + tool)
    release = Path("/etc/os-release").read_text()
    if 'VERSION_ID="13"' not in release or '\nID=debian\n' not in release:
        raise ValueError("guest must be an actual Debian 13 OS")
    guest_boot = Path("/proc/sys/kernel/random/boot_id").read_text().strip()
    if guest_boot == context["host_boot_id"]:
        raise ValueError("guest shares the host kernel boot identity")
    virt = subprocess.check_output(["systemd-detect-virt", "--vm"], text=True).strip()
    if virt not in ("qemu", "kvm"):
        raise ValueError("expected a QEMU virtual machine, not a container")
    if subprocess.check_output(["ip", "route", "show", "default"], text=True).strip():
        raise ValueError("external default routing must be disabled before acceptance")
    if context["mysql"]["host"] != "10.0.2.2" or context["mysql"]["database"] != "phpm_ci" or context["mysql"]["user"] != "phpm_ci":
        raise ValueError("fixture may use only its owned host-side CI database")
    checks = ["independent-debian-kernel", "non-root-without-system-php-or-compiler", "internet-route-disabled"]
    samples = []
    with tempfile.TemporaryDirectory(prefix="phpm-vm-") as temp, ExitStack() as stack:
        root = Path(temp)
        project = root / "project"
        project.mkdir()
        empty = root / "empty-scan"
        empty.mkdir()
        env = dict(os.environ, PHPM_HOME=str(root / "home"), PHPM_COMPOSER=str(ROOT / "composer.phar"),
                   COMPOSER_HOME=str(root / "composer-home"), SSL_CERT_FILE=str(ROOT / "fixture-ca.pem"),
                   SSL_CERT_DIR=str(empty), PHPRC=str(empty), PHP_INI_SCAN_DIR=str(empty))
        cli = ROOT / "phpm"

        def command(*args, fail=None):
            print("+ phpm", " ".join(map(str, args)), flush=True)
            completed = subprocess.run([str(cli), *map(str, args)], cwd=project, env=env,
                                       text=True, capture_output=True, timeout=120)
            if fail is not None:
                if completed.returncode == 0 or fail not in completed.stdout + completed.stderr:
                    raise ValueError("expected refusal was not observed: " + completed.stdout + completed.stderr)
            else:
                if completed.returncode:
                    print(completed.stdout + completed.stderr, flush=True)
                completed.check_returncode()
            return completed.stdout.strip()

        def info(candidate=False):
            value = json.loads(command("info", *(["--candidate"] if candidate else [])))
            return value["environment"] if candidate else value

        command("source", "add", "vm", "--catalog", context["catalog_url"], "--key", ROOT / "signing-key.pub")
        command("php", "install", context["base"], "--source", "vm")
        command("init", "--php", "8.4")
        (project / "phpm.yaml").write_text(json.dumps({"schema": 1, "php": "8.4", "ini": {
            "memory_limit": "192M", "opcache.enable_cli": "1", "opcache.jit_buffer_size": "0"}}))
        (project / "composer.json").write_text(json.dumps({"name": "phpm/vm-upgrade-test", "version": "1.0.0",
            "require": {"php": "^8.4", **{f"ext-{name}": "*" for name in
                ("redis", "curl", "pdo_mysql", "mbstring", "gd", "zip", "zend-opcache")}}}))
        command("install")
        command("ext", "install", "--missing")
        command("verify")
        command("ext", "check")
        command("install", "--frozen")
        initial = info()
        if initial["runtime"]["php"]["version"] != context["base"]:
            raise ValueError("base PHP does not match the real accepted source build")
        checks += ["signed-php84-download", "composer-missing-extensions", "base-frozen-install"]
        before = {name: (project / name).read_bytes() for name in ("phpm.yaml", "phpm.lock")}

        def unchanged():
            if any((project / name).read_bytes() != value for name, value in before.items()):
                raise ValueError("active project files unexpectedly changed")

        command("php", "upgrade", "8.6", fail="no signed catalog build")
        unchanged()
        checks.append("unavailable-upgrade-preserves-current")
        redis_socket = root / "redis.sock"
        redis_process = stack.enter_context(service(["redis-server", "--port", "0", "--unixsocket", redis_socket,
            "--unixsocketperm", "700", "--save", "", "--appendonly", "no"], root / "redis.log"))
        wait_socket(redis_process, redis_socket)
        (project / "probe.php").write_text(PROBE, encoding="utf-8")
        (project / "test-db.json").write_text(json.dumps({"mysql": context["mysql"], "redis_socket": str(redis_socket)}))
        (project / "test-db.json").chmod(0o600)

        def validate(value, version, sapi="cli"):
            if (value["php"] != version or value["sapi"] != sapi or value["redis"] != context["redis"]
                    or not value["mysql"].startswith(context["mysql"]["version"]) or value["transaction"] != "rolled-back"
                    or not value["redis_readwrite"] or not value["utf8mb4"] or not value["opcache"] or value["memory"] != "192M"):
                raise ValueError("runtime/database/extension probe mismatch: " + json.dumps(value))
            return value

        def probe(version, phase, candidate=False):
            return validate(json.loads(command("exec", *(["--candidate"] if candidate else []), "--", "php", "probe.php", phase)), version)

        samples.append(probe(context["base"], "baseline"))
        checks += ["mysql84-prepared-statements-and-utf8mb4", "mysql-transaction-rollback", "redis-real-readwrite"]
        (project / "old-process.php").write_text("<?php while(($line=fgets(STDIN))!==false){echo PHP_VERSION,PHP_EOL;flush();}")
        old_log = stack.enter_context((root / "old-process.log").open("w"))
        old_process = subprocess.Popen([str(cli), "exec", "--", "php", "old-process.php"], cwd=project, env=env,
            text=True, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=old_log, bufsize=1)

        def stop_old_process():
            if old_process.poll() is None:
                old_process.terminate()
            try:
                old_process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                old_process.kill()
                old_process.wait(timeout=5)

        stack.callback(stop_old_process)

        def old_still_running():
            old_process.stdin.write("version\n")
            old_process.stdin.flush()
            with selectors.DefaultSelector() as selector:
                selector.register(old_process.stdout, selectors.EVENT_READ)
                if not selector.select(timeout=10):
                    raise ValueError("old PHP process stopped responding")
                if old_process.stdout.readline().strip() != context["base"]:
                    raise ValueError("running old PHP process changed version")

        old_still_running()

        def start_fpm(selected, label):
            rt = Path(env["PHPM_HOME"]) / "store/runtimes" / selected["runtime"]["id"] / "payload"
            ini = Path(env["PHPM_HOME"]) / "environments" / selected["id"] / "php.ini"
            socket_path = root / (label + ".sock")
            config = root / (label + ".conf")
            config.write_text(f"[global]\nerror_log = {root / (label + '-error.log')}\ndaemonize = no\n[phpm-ci]\nlisten = {socket_path}\npm = static\npm.max_children = 1\ncatch_workers_output = yes\n")
            process = stack.enter_context(service([rt / "sbin/php-fpm", "-c", ini, "-y", config, "--nodaemonize"], root / (label + ".log"), env))
            wait_socket(process, socket_path)
            return socket_path

        def fpm_probe(socket_path, version):
            request_env = dict(env, SCRIPT_FILENAME=str(project / "probe.php"), SCRIPT_NAME="/probe.php", REQUEST_METHOD="GET",
                REQUEST_URI="/probe.php", QUERY_STRING="fpm", CONTENT_LENGTH="0", GATEWAY_INTERFACE="CGI/1.1",
                SERVER_PROTOCOL="HTTP/1.1", REDIRECT_STATUS="200")
            result = subprocess.check_output(["cgi-fcgi", "-bind", "-connect", str(socket_path)], env=request_env, text=True, timeout=30)
            return validate(json.loads(result.split("\n\n", 1)[1]), version, "fpm-fcgi")

        old_fpm = start_fpm(initial, "php84")
        samples.append(fpm_probe(old_fpm, context["base"]))
        command("php", "upgrade", context["target"])
        command("verify", "--candidate")
        command("ext", "check", "--candidate")
        candidate_value = json.loads(command("info", "--candidate"))
        candidate = candidate_value["environment"]
        if (candidate["runtime"]["php"]["version"] != context["target"]
                or candidate["runtime"]["php"]["api"] == initial["runtime"]["php"]["api"]
                or candidate["runtime"]["source"]["kind"] != "signed-catalog"):
            raise ValueError("candidate is not a genuinely different PHP API from the signed source")
        unchanged()
        samples.append(probe(context["target"], "candidate", True))
        old_still_running()
        old_redis = next(e for e in initial["extensions"] if e["name"] == "redis")
        old_module = Path(env["PHPM_HOME"]) / "store/extensions" / old_redis["id"] / "payload" / old_redis["module"]
        command("ext", "import", "redis@" + context["redis"], "--file", old_module,
                "--runtime", candidate["runtime"]["id"], fail="API")
        unchanged()
        command("verify", "--candidate")
        target_fpm = start_fpm(candidate, "php85")
        samples.append(fpm_probe(target_fpm, context["target"]))
        if any(e["name"] == "opcache" for e in candidate["extensions"]):
            raise ValueError("PHP 8.5 OPcache must be compiled in, not an imported SO")
        checks += ["real-php84-to-php85-api-change", "candidate-keeps-current-project-files", "candidate-mysql-and-redis",
                   "wrong-api-redis-import-rejected", "opcache-shared-to-built-in", "both-fpm-versions-serve-real-database-probes"]
        command("activate", candidate_value["candidate"])
        command("verify")
        active = info()
        if active["runtime"]["id"] != candidate["runtime"]["id"]:
            raise ValueError("activation did not select the real PHP 8.5 build")
        old_still_running()
        samples.append(fpm_probe(old_fpm, context["base"]))
        checks.append("activation-does-not-change-old-cli-or-fpm-processes")
        started, iterations = time.monotonic(), 0
        while time.monotonic() - started < context["soak_seconds"]:
            probe(context["target"], "active-soak")
            fpm_probe(target_fpm, context["target"])
            iterations += 1
            time.sleep(1)
        if iterations < 5:
            raise ValueError("insufficient successful soak iterations")
        soak_elapsed = time.monotonic() - started
        checks.append("bounded-cli-and-fpm-soak-with-real-mysql-redis")
        command("rollback")
        command("verify")
        command("install", "--frozen")
        unchanged()
        restored = info()
        if restored["id"] != initial["id"]:
            raise ValueError("rollback did not restore the exact original environment")
        old_still_running()
        samples.append(probe(context["base"], "rollback"))
        samples.append(fpm_probe(old_fpm, context["base"]))
        samples.append(fpm_probe(target_fpm, context["target"]))
        checks += ["real-php85-to-php84-rollback", "byte-exact-project-pair-restored", "mysql-and-redis-data-preserved-after-rollback"]
        report = {"schema": 1, "checks": checks, "versions": [selected["runtime"]["php"]["version"] for selected in (initial, active, restored)],
            "guest_kernel": platform.release(), "guest_boot_id": guest_boot, "virtualization": virt,
            "glibc": subprocess.check_output(["getconf", "GNU_LIBC_VERSION"], text=True).strip(),
            "php_api": [initial["runtime"]["php"]["api"], candidate["runtime"]["php"]["api"]],
            "runtime_ids": [initial["runtime"]["id"], candidate["runtime"]["id"]],
            "soak": {"seconds": round(soak_elapsed, 2), "requested_seconds": context["soak_seconds"], "iterations": iterations}, "samples": samples,
            "limitations": ["built-in fixture, not a user business project", "bounded soak, not long-duration load qualification",
                            "explicit test FPM sockets, no production traffic or service-manager deployment", "test signing key"]}
        (ROOT / "report.json").write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps({"status": "passed", "checks": len(checks), "soak_iterations": iterations}), flush=True)


if __name__ == "__main__":
    main()
