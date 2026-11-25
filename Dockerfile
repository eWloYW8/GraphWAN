FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY internal/boltcompat/go.mod internal/boltcompat/
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
ARG TARGETARCH
ARG VERSION=dev
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X main.version=$VERSION" -o /out/graphwan ./cmd/graphwan

FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates iptables nftables \
    && rm -rf /var/lib/apt/lists/* \
    && install -d -o 65532 -g 65532 -m 0700 /data/server \
    && install -d -m 0700 /data/agent
COPY --from=build /out/graphwan /usr/local/bin/graphwan
USER 65532:65532
WORKDIR /data
ENTRYPOINT ["graphwan"]
CMD ["server", "--listen=:8443", "--data-dir=/data/server"]
