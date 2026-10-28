ARG BASE_IMAGE
FROM ${BASE_IMAGE} AS verify
ARG APT_SNAPSHOT
ENV DEBIAN_FRONTEND=noninteractive
COPY runtime-packages.txt /tmp/runtime-packages.txt
RUN test -n "$APT_SNAPSHOT" \
 && printf '#!/bin/sh\nexit 101\n' > /usr/sbin/policy-rc.d && chmod 755 /usr/sbin/policy-rc.d \
 && printf 'Types: deb\nURIs: http://snapshot.debian.org/archive/debian/%s/\nSuites: trixie trixie-updates\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\nCheck-Valid-Until: no\n\nTypes: deb\nURIs: http://snapshot.debian.org/archive/debian-security/%s/\nSuites: trixie-security\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\nCheck-Valid-Until: no\n' "$APT_SNAPSHOT" "$APT_SNAPSHOT" > /etc/apt/sources.list.d/debian.sources \
 && apt-get update \
 && xargs -r apt-get install -y --no-install-recommends < /tmp/runtime-packages.txt \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir /out && chown 65534:65534 /out
COPY packages/ /packages/
COPY phpm phpm.test composer.phar /verify/
RUN chmod 755 /verify/phpm /verify/phpm.test
USER 65534:65534
WORKDIR /tmp
ENV PHPM_TEST_DEBIAN_PACKAGES=/packages
ENV PHPM_TEST_CLI_BINARY=/verify/phpm
ENV PHPM_TEST_COMPOSER=/verify/composer.phar
ENV PHPM_TEST_DISTRIBUTION_OUTPUT=/out
# The test's TLS fixture is loopback-only; no external network or host PHP.
RUN --network=none /verify/phpm.test -test.run '^TestNativeSignedCatalogLifecycle$' -test.v -test.timeout 10m

FROM scratch AS artifacts
COPY --from=verify /out/ /
