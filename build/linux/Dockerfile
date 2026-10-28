# Invoked by build.py: a pinned Debian amd64 image and snapshot are mandatory.
ARG BASE_IMAGE
FROM ${BASE_IMAGE} AS builder
ARG APT_SNAPSHOT
ARG BUILD_JOBS=2
ENV DEBIAN_FRONTEND=noninteractive
ENV PHPM_BUILD_JOBS=${BUILD_JOBS}
COPY build-packages.txt /tmp/build-packages.txt
RUN test -n "$APT_SNAPSHOT" \
 && printf '#!/bin/sh\nexit 101\n' > /usr/sbin/policy-rc.d && chmod 755 /usr/sbin/policy-rc.d \
 && printf 'Types: deb\nURIs: http://snapshot.debian.org/archive/debian/%s/\nSuites: trixie trixie-updates\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\nCheck-Valid-Until: no\n\nTypes: deb\nURIs: http://snapshot.debian.org/archive/debian-security/%s/\nSuites: trixie-security\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\nCheck-Valid-Until: no\n' "$APT_SNAPSHOT" "$APT_SNAPSHOT" > /etc/apt/sources.list.d/debian.sources \
 && apt-get update \
 && xargs -r apt-get install -y --no-install-recommends < /tmp/build-packages.txt \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir /work /out && chown 65534:65534 /work /out
COPY . /recipe/
USER 65534:65534
WORKDIR /work
RUN python3 /recipe/assemble.py --fetch-only
# The compiler/test steps cannot fetch implicit sources or build tools.
RUN --network=none python3 /recipe/assemble.py \
 && python3 /recipe/verify-sdk.py

FROM ${BASE_IMAGE} AS runtime-check
ARG APT_SNAPSHOT
ENV DEBIAN_FRONTEND=noninteractive
COPY runtime-packages.txt /tmp/runtime-packages.txt
# These extra packages are test tools; no PHP package, compiler or dev headers.
RUN test -n "$APT_SNAPSHOT" \
 && printf '#!/bin/sh\nexit 101\n' > /usr/sbin/policy-rc.d && chmod 755 /usr/sbin/policy-rc.d \
 && printf 'Types: deb\nURIs: http://snapshot.debian.org/archive/debian/%s/\nSuites: trixie trixie-updates\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\nCheck-Valid-Until: no\n\nTypes: deb\nURIs: http://snapshot.debian.org/archive/debian-security/%s/\nSuites: trixie-security\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\nCheck-Valid-Until: no\n' "$APT_SNAPSHOT" "$APT_SNAPSHOT" > /etc/apt/sources.list.d/debian.sources \
 && apt-get update \
 && xargs -r apt-get install -y --no-install-recommends < /tmp/runtime-packages.txt \
 && apt-get install -y --no-install-recommends python3 openssl redis-server libfcgi-bin \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir /verification && chown 65534:65534 /verification
COPY --from=builder /out/ /packages/
COPY --from=builder /work/downloads/composer.phar /fixtures/composer.phar
COPY common.py verify-runtime.py phpm /verify/
RUN chmod 755 /verify/phpm
USER 65534:65534
WORKDIR /tmp
RUN --network=none python3 /verify/verify-runtime.py

# Export is dependent on both SDK and fresh-container verification succeeding.
FROM scratch AS artifacts
COPY --from=builder /out/ /
COPY --from=runtime-check /verification/ /verification/
