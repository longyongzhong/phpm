#!/bin/sh
# Run on Linux with Go >= 1.23 and PHP CLI (CI uses PHP 8.4).
# Installs no host packages; native fixtures use temporary project/store paths.
set -eu

if [ "$(uname -s)" != Linux ]; then
    printf '%s\n' 'This test entry point requires a Linux kernel.' >&2
    exit 2
fi

project_dir=$(CDPATH= cd -P "$(dirname "$0")/.." && pwd)
cd "$project_dir"
command -v go >/dev/null 2>&1 || {
    printf '%s\n' 'Go >= 1.23 is required.' >&2
    exit 2
}
php_binary=$(command -v "${PHPM_TEST_PHP_BINARY:-php}") || {
    printf '%s\n' 'PHP CLI is required; set PHPM_TEST_PHP_BINARY if it is not on PATH.' >&2
    exit 2
}

printf '%s\n' 'Linux test host:'
uname -srm
go version
"$php_binary" -n -v
# Match the no-system-INI runtime imported by the native fixtures.
"$php_binary" -n -r 'if (!extension_loaded("json")) {fwrite(STDERR,"Native fixtures require built-in JSON.\n"); exit(2);} echo "Composer fixture phar support: ",extension_loaded("phar") ? "yes" : "no (Composer subtest will skip)",PHP_EOL;'

run_dir=$(mktemp -d "${TMPDIR:-/tmp}/phpm-linux-tests.XXXXXX")
trap 'rm -rf -- "$run_dir"' 0
trap 'exit 130' INT
trap 'exit 143' TERM
export PHPM_TEST_PHP_BINARY="$php_binary"
export PHPM_TEST_CLI_BINARY="$run_dir/phpm"

go build -o "$PHPM_TEST_CLI_BINARY" ./cmd/phpm
go test -count=1 -v ./...
go vet ./...
printf '%s\n' 'Linux test commands completed. Review SKIP lines for unavailable optional fixtures.'
