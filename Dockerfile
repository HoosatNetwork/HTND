# syntax=docker/dockerfile:1

# --- Stage 1: Build stage ---
FROM golang:1.26 AS build

ENV CGO_ENABLED=1 \
  GOEXPERIMENT=simd,jsonv2

WORKDIR /build

# Cache Go module downloads across builds
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
  go mod download

COPY . .

# Build all binaries with CGO enabled using BuildKit compilation and C caching
RUN --mount=type=cache,target=/go/pkg/mod \
  --mount=type=cache,target=/root/.cache/go-build \
  go build -trimpath -ldflags="-s -w" -tags "deadlock pebblegozstd" \
  -o /out/ . ./cmd/htnwallet ./cmd/htnminer ./cmd/htnctl ./cmd/genkeypair

# --- Stage 2: Runtime image ---
FROM ubuntu:24.04

WORKDIR /app

# Combine runtime directory and ca-certificates setup
RUN apt-get update && \
  apt-get install -y --no-install-recommends ca-certificates && \
  rm -rf /var/lib/apt/lists/* && \
  mkdir -p /nonexistent/.htnd && \
  chown nobody:nogroup /nonexistent/.htnd && \
  chmod 700 /nonexistent/.htnd

# Copy binaries with direct permission/ownership setup
COPY --from=build --chown=nobody:nogroup --chmod=755 /out/ /app/

USER nobody
ENTRYPOINT ["/app/HTND"]
CMD ["--utxoindex", "--saferpc", "--autoupdate=false", "--autoupdate-download=false", "--autoupdate-install=false", "--autoreport-issues=false"]