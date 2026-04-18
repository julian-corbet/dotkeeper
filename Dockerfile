# dotkeeper container image
# Supports both one-shot commands (sync, status) and long-running daemon (start)
#
# Usage:
#   # Git sync only (no Syncthing daemon)
#   docker run -v ~/.config/dotkeeper:/config -v ~/repos:/repos ghcr.io/julian-corbet/dotkeeper sync
#
#   # Full daemon with Syncthing
#   docker run -d --name dotkeeper \
#     -v ~/.config/dotkeeper:/config \
#     -v ~/.local/share/dotkeeper:/data \
#     -v ~/repos:/repos \
#     -p 12000:12000/tcp -p 12000:12000/udp \
#     -p 11027:11027/udp \
#     ghcr.io/julian-corbet/dotkeeper start

# --- Build stage ---
FROM golang:1.26-alpine@sha256:f85330846cde1e57ca9ec309382da3b8e6ae3ab943d2739500e08c86393a21b1 AS build

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown

RUN CGO_ENABLED=0 go build -tags noassets \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /dotkeeper ./cmd/dotkeeper

# --- Runtime stage ---
FROM alpine:3.23@sha256:5b10f432ef3da1b8d4c7eb6c487f2f5a8f096bc91145e68878dd4a5019afde11

RUN apk add --no-cache git ca-certificates tzdata

COPY --from=build /dotkeeper /usr/bin/dotkeeper

# Config: machine.toml, config.toml
# Data: Syncthing identity, database
# Repos: user's repositories (mount as needed)
ENV XDG_CONFIG_HOME=/config
ENV XDG_DATA_HOME=/data
VOLUME ["/config", "/data", "/repos"]

# Syncthing ports
# 12000/tcp+udp = file sync (TCP + QUIC)
# 11027/udp     = local discovery
# 18384 is API on loopback only — not exposed
EXPOSE 12000/tcp 12000/udp 11027/udp

ENTRYPOINT ["dotkeeper"]
CMD ["status"]
