# Third-party notices

PHPM's own code is licensed under MIT. This does not replace dependency licenses.

| Component | Version | License / included notice |
|---|---|---|
| Go runtime and standard library | Build toolchain dependent; local build uses Go 1.23.2 | BSD-style [Go license](third_party/go/LICENSE) |
| gopkg.in/yaml.v3 | v3.0.1 | MIT for the libyaml-derived files and Apache-2.0 for the remaining files; [upstream notice](third_party/yaml.v3/LICENSE) |
| Composer (Linux test bundle fixture only) | 2.10.3 | MIT; test bundles include its license and upstream PHAR, which contains dependency notices |

The YAML dependency's Apache-2.0 terms are available at
[included Apache License 2.0 text](third_party/yaml.v3/Apache-2.0.txt), obtained from
https://www.apache.org/licenses/LICENSE-2.0.txt. Client archives include the full
text alongside the upstream notice.
Release packages must include these notices and applicable full license texts.

PHP runtimes and native extensions downloaded/imported by users are separate
artifacts with their own licenses. No PHP/Redis binary is committed as PHPM source.
Files under ignored development caches are local test artifacts, not a release.
The Linux download adapter obtains community StaticPHP common-preset artifacts
directly from the publisher; these are not PHP official binaries. Their PHP,
extension and library notices are supplied by the publisher at
[StaticPHP common resources](https://dl.static-php.dev/v3/php-bin/common/).
