# Contributing

Use Go 1.23 or newer. Keep changes within the relevant adapter or environment layer.

Before submitting a change:

```sh
gofmt -w cmd internal
go test ./...
go vet ./...
go build ./cmd/phpm
```

For native PHP tests set `PHPM_TEST_PHP_BINARY` to a PHP executable. Optional
`PHPM_TEST_REDIS_MODULE` and `PHPM_TEST_REDIS_ALTERNATE` exercise different Redis
versions against the same PHP. Tests never install host dependencies or alter
system configuration. Ordinary unit tests use an in-memory HTTP transport.
Composer native tests use an installed Composer 2 PHAR, or `PHPM_TEST_COMPOSER`
to select one explicitly. The PHP fixture needs compiled-in phar; otherwise that
subtest reports a skip. `PHPM_TEST_CLI_BINARY` enables subprocess argument and
exit-code checks using a prebuilt PHPM CLI.

On a Linux machine, run the same native checks through:

```sh
sh scripts/test-linux.sh
```

The script requires existing Go >=1.23 and PHP CLI (CI uses PHP 8.4), builds a
temporary CLI, runs unit/native tests and vet, then removes that temporary build.
Composer tests require a compatible Composer 2 PHAR and compiled-in phar in the
imported PHP fixture. Set `PHPM_TEST_COMPOSER` to select the PHAR explicitly.
PHP-dependent tests are enabled by the script; optional fixture skips remain
visible in verbose output. The current batch-download fixture is Windows-only.
Upgrade lifecycle tests model two distinct payloads of the same PHP version;
they do not substitute for testing two actual PHP versions on a Linux host.

For hosts without Go, a Linux amd64 test bundle can contain the CLI, a test binary
cross-compiled with `GOOS=linux GOARCH=amd64 go test -c ./internal/phpm`, and
`scripts/test-linux-prebuilt.sh` as `run-tests.sh`, with `SHA256SUMS` covering the
files. `sh run-tests.sh --unit` runs unit checks without PHP; `--native` requires
PHP and enables integration fixtures. The prebuilt bundle does not run Go vet.

The updated prebuilt runner also supports `--bootstrap`: in a temporary home it
downloads the StaticPHP Linux x64 NTS common builds for PHP 8.4 and 8.5, installs
an environment, tests candidate isolation/activation/rollback with exact project
file hashes, then runs native fixtures using the acquired PHP. No system PHP or
package-manager operation is required. A bundled `fixtures/composer.phar` enables
Composer smoke/native checks; all bundled files must be listed in SHA256SUMS.
This mode needs HTTPS network access and writable/executable temporary storage.
`PHPM_TEST_STATICPHP_NETWORK=1 go test -run TestOptionalStaticPHPNetwork -v ./internal/phpm`
is a separate opt-in live catalog/checksum/download/static-ELF check that never
executes the downloaded PHP. The Linux bootstrap CI job is configured separately
from setup-php-based tests; configuration does not establish that CI has run.

Cover lock stability, interrupted/failed preparation, path handling and actual PHP
loading when changing install logic. Do not replace missing platform evidence with
a passing cross-compile. Report skipped integration tests and external dependencies.

Use separate conventional commits for fixes/features/docs when practical. This
workspace does not automatically create a Git repository or publish a release.
Development builds use `0.1.0-dev`; tagged releases should document the actual
platform/extension matrix and validation before changing that version.

The PHPM binary can be removed directly. Removing PHPM_HOME removes all managed
environments; first stop applications that use them and retain any needed artifacts.
There is no automated uninstall/garbage-collection command in this release.
