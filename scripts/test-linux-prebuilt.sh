#!/bin/sh
# Included as run-tests.sh in the Linux amd64 test bundle. No Go install needed.
set -eu

if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
    printf '%s\n' 'This bundle requires Linux x86_64.' >&2
    exit 2
fi
mode=${1:---unit}
case "$mode" in
    --unit|--native|--bootstrap) ;;
    *) printf '%s\n' 'Usage: sh run-tests.sh [--unit|--native|--bootstrap]' >&2; exit 2 ;;
esac
if [ "$#" -gt 1 ]; then
    printf '%s\n' 'Usage: sh run-tests.sh [--unit|--native|--bootstrap]' >&2
    exit 2
fi
bundle_dir=$(CDPATH= cd -P "$(dirname "$0")" && pwd)
cd "$bundle_dir"
sha256sum -c SHA256SUMS
if [ ! -x ./phpm ] || [ ! -x ./phpm.test ]; then
    printf '%s\n' 'Run chmod +x phpm phpm.test in the extracted bundle directory first.' >&2
    exit 2
fi

run_dir=$(mktemp -d "${TMPDIR:-/tmp}/phpm-prebuilt-tests.XXXXXX")
trap 'rm -rf -- "$run_dir"' 0
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir "$run_dir/tmp"
export TMPDIR="$run_dir/tmp"
export PHPM_HOME="$run_dir/home"
export PHPM_TEST_CLI_BINARY="$bundle_dir/phpm"
unset PHPM_TEST_STATICPHP_NETWORK

if [ "$mode" = --bootstrap ]; then
    printf '%s\n' 'BOOTSTRAP MODE: download managed StaticPHP 8.4/8.5; test real upgrade/rollback and native fixtures. No system packages are installed.'
    mkdir "$run_dir/project"
    cd "$run_dir/project"
    "$PHPM_TEST_CLI_BINARY" init --php 8.4
    "$PHPM_TEST_CLI_BINARY" php install 8.4 --provider staticphp
    "$PHPM_TEST_CLI_BINARY" install
    "$PHPM_TEST_CLI_BINARY" ext install json
    php_binary=$("$PHPM_TEST_CLI_BINARY" exec -- php -r 'echo PHP_BINARY;')
    export PHPM_TEST_PHP_BINARY="$php_binary"
    "$PHPM_TEST_CLI_BINARY" info > "$run_dir/initial-info.json"
    archive_hash=$("$php_binary" -n -r '$e=json_decode(file_get_contents($argv[1]),true,512,JSON_THROW_ON_ERROR);$h=$e["runtime"]["source"]["sha256"]??"";if(!preg_match("/^[a-f0-9]{64}$/",$h)){exit(2);}echo $h;' "$run_dir/initial-info.json")
    export PHPM_TEST_STATICPHP_ARCHIVE="$PHPM_HOME/cache/$archive_hash.zip"
    unset COMPOSER_ROOT_VERSION
    if [ -f "$bundle_dir/fixtures/composer.phar" ]; then
        export PHPM_COMPOSER="$bundle_dir/fixtures/composer.phar"
        export PHPM_TEST_COMPOSER="$PHPM_COMPOSER"
        export COMPOSER="$run_dir/project/composer.json"
        printf '%s\n' '{"name":"phpm/bootstrap-test","version":"1.0.0","require":{"php":">=8.2","ext-json":"*","ext-pdo_mysql":"*","ext-redis":"*"}}' > composer.json
        "$PHPM_TEST_CLI_BINARY" ext check
        "$PHPM_TEST_CLI_BINARY" ext install --missing
        "$PHPM_TEST_CLI_BINARY" composer --version
    else
        printf '%s\n' 'No bundled Composer PHAR; bootstrap Composer smoke commands will not run.'
    fi
    before_version=$("$PHPM_TEST_CLI_BINARY" exec -- php -r 'echo PHP_VERSION;')
    sha256sum phpm.yaml phpm.lock > "$run_dir/before-upgrade.sha256"
    "$PHPM_TEST_CLI_BINARY" php upgrade 8.5 --provider staticphp
    "$PHPM_TEST_CLI_BINARY" diff
    "$PHPM_TEST_CLI_BINARY" verify --candidate
    "$PHPM_TEST_CLI_BINARY" exec --candidate -- php -v
    if [ -f "$bundle_dir/fixtures/composer.phar" ]; then
        "$PHPM_TEST_CLI_BINARY" ext check --candidate
    fi
    current_version=$("$PHPM_TEST_CLI_BINARY" exec -- php -r 'echo PHP_VERSION;')
    test "$before_version" = "$current_version"
    "$PHPM_TEST_CLI_BINARY" info --candidate > "$run_dir/candidate.json"
    candidate=$("$php_binary" -n -r '$p=json_decode(file_get_contents($argv[1]),true,512,JSON_THROW_ON_ERROR);$id=$p["candidate"]??"";if(!preg_match("/^upgrade-[a-f0-9]{64}$/",$id)){exit(2);}echo $id;' "$run_dir/candidate.json")
    "$PHPM_TEST_CLI_BINARY" activate "$candidate"
    after_version=$("$PHPM_TEST_CLI_BINARY" exec -- php -r 'echo PHP_VERSION;')
    case "$after_version" in 8.5.*) ;; *) printf '%s\n' 'Activation selected an unexpected PHP version.' >&2; exit 1 ;; esac
    "$PHPM_TEST_CLI_BINARY" history
    "$PHPM_TEST_CLI_BINARY" rollback
    restored_version=$("$PHPM_TEST_CLI_BINARY" exec -- php -r 'echo PHP_VERSION;')
    test "$before_version" = "$restored_version"
    sha256sum -c "$run_dir/before-upgrade.sha256"
    "$PHPM_TEST_CLI_BINARY" verify
    printf 'Real-version lifecycle passed: %s -> %s -> %s\n' "$before_version" "$after_version" "$restored_version"
    cd "$bundle_dir"
fi

if [ "$mode" = --unit ]; then
    unset PHPM_TEST_PHP_BINARY PHPM_TEST_REDIS_MODULE PHPM_TEST_REDIS_ALTERNATE
    printf '%s\n' 'UNIT MODE: PHP integration tests will be explicitly skipped.'
else
    if [ -n "${PHPM_TEST_PHP_BINARY:-}" ]; then
        php_binary=$(command -v "$PHPM_TEST_PHP_BINARY" || true)
    else
        php_binary=$(command -v php || command -v php-cli || true)
    fi
    if [ -z "${php_binary:-}" ] || [ ! -x "$php_binary" ]; then
        printf 'PHP CLI not found or not executable: %s\n' "${PHPM_TEST_PHP_BINARY:-php / php-cli on PATH}" >&2
        printf '%s\n' 'Install PHP CLI first, then set PHPM_TEST_PHP_BINARY to its executable path.' >&2
        exit 2
    fi
    "$php_binary" -n -r 'if (!extension_loaded("json")) {fwrite(STDERR,"Native fixtures require built-in JSON.\n"); exit(2);} echo "Composer fixture phar support: ",extension_loaded("phar") ? "yes" : "no (Composer subtest will skip)",PHP_EOL;'
    export PHPM_TEST_PHP_BINARY="$php_binary"
    "$php_binary" -n -v
fi

printf '%s\n' 'Linux test host:'
uname -srm
./phpm version
./phpm help
./phpm.test -test.v -test.count=1 -test.timeout=10m
printf '%s\n' 'Requested tests completed. Review SKIP lines; Go vet is not run by this prebuilt bundle.'
