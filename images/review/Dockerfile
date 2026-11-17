# The image the AI review runs: Conclave, and Pi as its agent.
#
# Both are pinned: a review that changes with a release of either would change its verdicts without
# anyone deciding it. Conclave is downloaded from its release and checked against the checksums
# published with it.

ARG NODE_IMAGE=docker.io/library/node:24-bookworm-slim
ARG YQ_IMAGE=docker.io/mikefarah/yq:4.44.3

FROM ${YQ_IMAGE} AS yq

FROM ${NODE_IMAGE} AS conclave
ARG CONCLAVE_VERSION=0.23.0
ARG TARGETARCH
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /download
RUN set -eu; \
    archive="conclave_${CONCLAVE_VERSION}_linux_${TARGETARCH}.tar.gz"; \
    base="https://github.com/Bornholm/conclave/releases/download/v${CONCLAVE_VERSION}"; \
    curl -fsSLO "${base}/${archive}"; \
    curl -fsSLO "${base}/checksums.txt"; \
    grep " ${archive}\$" checksums.txt | sha256sum -c -; \
    tar -xzf "${archive}" conclave; \
    install -m 0755 conclave /usr/local/bin/conclave

FROM ${NODE_IMAGE}
ARG PI_VERSION=1.5.0
RUN apt-get update \
 && apt-get install -y --no-install-recommends bash ca-certificates git jq openssh-client ripgrep \
 && rm -rf /var/lib/apt/lists/*
RUN npm install -g --ignore-scripts "@earendil-works/pi-coding-agent@${PI_VERSION}" \
 && npm cache clean --force
COPY --from=yq /usr/bin/yq /usr/local/bin/yq
COPY --from=conclave /usr/local/bin/conclave /usr/local/bin/conclave

# The extension that makes a Pi run answer before Conclave stops it, and Conclave's skills, in the
# agent directory the task fills with the operator's providers.
ENV HOME=/root PI_OFFLINE=1 PI_SKIP_VERSION_CHECK=1
RUN conclave agents install pi && conclave version && pi --version
