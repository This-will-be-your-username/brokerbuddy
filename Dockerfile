# syntax=docker/dockerfile:1
# Multi-stage build: a static, CGO-free binary in a small image.
ARG GO_VERSION=1.27.3

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/brokerbuddy .

FROM alpine:3.20
# CA certificates: merchant lookups, mortgage rates and Let's Encrypt use HTTPS.
# su-exec: the entrypoint starts as root, fixes /data ownership, then drops to PUID:PGID
# (the linuxserver.io convention; defaults 1000:1000).
RUN apk add --no-cache ca-certificates tzdata su-exec && mkdir -p /data
COPY --from=build /out/brokerbuddy /usr/local/bin/brokerbuddy
COPY --chmod=755 docker/entrypoint.sh /usr/local/bin/entrypoint.sh
ENV HOME=/data PUID=1000 PGID=1000
WORKDIR /data
# Your database and its key file live here: mount a volume so they survive the container.
VOLUME ["/data"]
EXPOSE 8443
# Inside a container the app must listen on all interfaces. Publish the port on the host's
# loopback only (-p 127.0.0.1:8443:8443) unless you have set a password.
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
