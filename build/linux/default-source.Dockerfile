ARG BASE_IMAGE
FROM ${BASE_IMAGE}
ARG DEBIAN_VERSION
ENV DEBIAN_FRONTEND=noninteractive
COPY runtime-packages*.txt /tmp/
RUN case "$DEBIAN_VERSION" in \
      12) cp /tmp/runtime-packages-debian12.txt /tmp/runtime-packages.txt ;; \
      13) ;; \
      *) exit 1 ;; \
    esac \
 && apt-get update \
 && xargs -r apt-get install -y --no-install-recommends < /tmp/runtime-packages.txt \
 && rm -rf /var/lib/apt/lists/*
COPY phpm /usr/local/bin/phpm
COPY test-default-source.sh /usr/local/bin/test-default-source.sh
RUN chmod 755 /usr/local/bin/phpm /usr/local/bin/test-default-source.sh
USER 65534:65534
WORKDIR /tmp
ENTRYPOINT ["sh", "/usr/local/bin/test-default-source.sh"]
