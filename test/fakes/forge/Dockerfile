FROM golang:1.27.3@sha256:c292963ad0e5f51e843c726edc204a4e4c26a859ba1e4d736d91028aa3633048 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags "-s -w" \
    -o /fakeforge ./test/fakes/forge

FROM debian:bookworm-slim@sha256:99c0b25c46d0214d5a2091092e7908907dcaecd6a962cde0ac700acee222b6f3
RUN apt-get update \
 && apt-get install -y --no-install-recommends git \
 && rm -rf /var/lib/apt/lists/*
COPY --from=builder /fakeforge /usr/local/bin/fakeforge
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/fakeforge"]
