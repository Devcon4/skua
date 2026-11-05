FROM debian:bookworm-slim@sha256:99c0b25c46d0214d5a2091092e7908907dcaecd6a962cde0ac700acee222b6f3

ARG OMP_VERSION=18.16.0
ARG OMP_ASSET=omp-linux-x64
ARG OMP_SHA256=6498ed785bab00d2926e0387e0ec02362d6d45f1c3d0d1b57c1618b7f95c9264

# omp extracts its embedded native addon into PI_NATIVES_DIR; pre-extracting both CPU variants keeps it from writing 120 MB into HOME on every start.
ENV PI_NATIVES_DIR=/usr/local/lib/omp/natives

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git gh procps openssh-client curl \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd -g 65532 agent \
 && useradd -u 65532 -g 65532 -m -d /home/agent -s /bin/sh agent

RUN set -eu; \
    cd /tmp; \
    base="https://github.com/can1357/oh-my-pi/releases/download/v${OMP_VERSION}"; \
    curl -fsSLo SHA256SUMS.txt "${base}/SHA256SUMS.txt"; \
    echo "${OMP_SHA256}  ${OMP_ASSET}" > pinned.txt; \
    grep -qxF "$(cat pinned.txt)" SHA256SUMS.txt; \
    curl -fsSLo "${OMP_ASSET}" "${base}/${OMP_ASSET}"; \
    sha256sum -c pinned.txt; \
    install -m 0755 "${OMP_ASSET}" /usr/local/bin/omp; \
    rm -f SHA256SUMS.txt pinned.txt "${OMP_ASSET}"; \
    for variant in modern baseline; do \
      HOME=/tmp/omp-home PI_NATIVE_VARIANT="${variant}" OPENAI_API_KEY=unused omp --mode rpc --no-ui </dev/null >/dev/null; \
      test -f "${PI_NATIVES_DIR}/${OMP_VERSION}/pi_natives.linux-x64-${variant}.node"; \
    done; \
    rm -rf /tmp/omp-home; \
    chmod -R a+rX,go-w /usr/local/lib/omp

USER 65532:65532
