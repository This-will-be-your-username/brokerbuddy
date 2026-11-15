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
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S brokerbuddy && adduser -S -G brokerbuddy -h /data brokerbuddy \
    && mkdir -p /data && chown brokerbuddy:brokerbuddy /data
COPY --from=build /out/brokerbuddy /usr/local/bin/brokerbuddy
USER brokerbuddy
WORKDIR /data
# Your database and its key file live here: mount a volume so they survive the container.
VOLUME ["/data"]
EXPOSE 8443
# Inside a container the app must listen on all interfaces. Publish the port on the host's
# loopback only (-p 127.0.0.1:8443:8443) unless you have set a password.
ENTRYPOINT ["brokerbuddy", "-db", "/data/brokerbuddy.db", "-addr", "0.0.0.0:8443"]
