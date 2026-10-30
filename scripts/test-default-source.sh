#!/bin/sh
# Real default-provider acceptance, inside a fresh Debian runtime container.
set -eu
binary=${PHPM_TEST_CLI_BINARY:-/usr/local/bin/phpm}
test -x "$binary"
test "$(id -u)" != 0
for tool in php cc make; do
    if command -v "$tool" >/dev/null 2>&1; then
        printf 'Unexpected host tool: %s\n' "$tool" >&2
        exit 1
    fi
done
work=$(mktemp -d /tmp/phpm-default-source.XXXXXX)
trap 'rm -rf -- "$work"' 0
trap 'exit 130' INT
trap 'exit 143' TERM
export PHPM_HOME="$work/home"
mkdir "$work/project"
cd "$work/project"
"$binary" source list
# No manual source registration and no --source arguments in this fixture.
"$binary" php available > "$work/available.txt"
cat "$work/available.txt"
grep -F "$PHPM_EXPECTED_TARGET" "$work/available.txt"
if grep -E 'debian(12|13)-x86_64-nts' "$work/available.txt" | grep -v -F "$PHPM_EXPECTED_TARGET"; then
    printf '%s\n' 'Default catalog listed a foreign target.' >&2
    exit 1
fi
"$binary" php install 8.4
"$binary" source verify phpm
"$binary" init --php 8.4
"$binary" install
"$binary" ext install xlswriter
"$binary" ext install redis
"$binary" verify
"$binary" install --frozen
"$binary" info > "$work/current.json"
"$binary" exec -- php -r '$e=json_decode(file_get_contents($argv[1]),true,512,JSON_THROW_ON_ERROR);if($e["runtime"]["source"]["kind"]!=="signed-catalog"||$e["runtime"]["source"]["catalog"]!=="phpm"){exit(1);}echo "Built-in signed runtime binding: OK\n";' -- "$work/current.json"
cat > excel-test.php <<'PHP'
<?php
$config = ['path' => getcwd()];
$writer = new \Vtiful\Kernel\Excel($config);
$writer->fileName('check.xlsx')->header(['name', 'value'])->data([['phpm', 42]])->output();
$reader = new \Vtiful\Kernel\Excel($config);
$rows = $reader->openFile('check.xlsx')->openSheet()->getSheetData();
if ($rows != [['name', 'value'], ['phpm', 42]] || !extension_loaded('redis')) {
    throw new RuntimeException('Extension roundtrip failed');
}
echo 'Excel and Redis loading: OK', PHP_EOL;
PHP
"$binary" exec -- php excel-test.php
before=$("$binary" exec -- php -r 'echo PHP_VERSION;')
sha256sum phpm.yaml phpm.lock > "$work/before.sha256"
"$binary" php upgrade 8.5
"$binary" verify --candidate
"$binary" exec --candidate -- php excel-test.php
test "$("$binary" exec -- php -r 'echo PHP_VERSION;')" = "$before"
"$binary" info --candidate > "$work/candidate.json"
candidate=$("$binary" exec -- php -r '$e=json_decode(file_get_contents($argv[1]),true,512,JSON_THROW_ON_ERROR);$id=$e["candidate"]??"";if(!preg_match("/^upgrade-[a-f0-9]{64}$/",$id)){exit(1);}echo $id;' -- "$work/candidate.json")
"$binary" activate "$candidate"
after=$("$binary" exec -- php -r 'echo PHP_VERSION;')
case "$after" in 8.5.*) ;; *) exit 1 ;; esac
"$binary" exec -- php excel-test.php
"$binary" rollback
"$binary" verify
sha256sum -c "$work/before.sha256"
test "$("$binary" exec -- php -r 'echo PHP_VERSION;')" = "$before"
printf 'Signed default source acceptance passed: %s, PHP %s -> %s -> %s\n' "$PHPM_EXPECTED_TARGET" "$before" "$after" "$before"
