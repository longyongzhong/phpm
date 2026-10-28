# Security

PHPM executes native PHP and extension code during compatibility verification.
Only supply trusted artifacts. A hash learned from the same untrusted file does
not establish publisher authenticity. Official downloads currently trust the
HTTPS PHP catalog and its SHA-256 values; independent signing is not implemented.
Linux x64 automatic downloads instead use the third-party StaticPHP v3 common
preset over HTTPS and require the publisher's matching SHA-256 sidecar. Redirects
are limited to that preset's frontend route and object-storage origin. The ELF
must be static x86_64 before execution; tar links, special files, traversal and
overwrites are refused. Publisher-hosted hashes establish transport/artifact
integrity, not an independent signing identity. No package-manager or remote
installation script is run. Fixed built-in modules are part of that runtime's
trusted payload; Linux native execution remains subject to recorded host tests.
Windows extension archives are obtained from the official HTTPS release directory,
which does not consistently publish hashes. PHPM records the observed SHA-256 and
checks it on cache reuse. `ext install --sha256 HASH` requires a separately obtained
expected digest; without it, the first-download trust comes from HTTPS alone.

Project environments isolate configuration and artifact selection, not filesystem,
network, operating-system libraries, credentials or hostile application code.
Treat phpm.yaml from an untrusted project as executable-environment input and
review its INI settings before running it. Do not run PHPM as administrator/root
for normal per-user operations.

For a vulnerability, use the repository's private security advisory channel if
enabled. Do not publish exploit details or secrets in a public issue; if a private
channel is not available, request a private reporting contact from the maintainer
without including the vulnerability itself. No private contact is configured yet.
