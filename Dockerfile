# -- multistage docker build: stage #1: build stage
FROM golang:1.26 AS build

RUN mkdir -p /go/src/github.com/Hoosat-Oy/HTND
WORKDIR /go/src/github.com/Hoosat-Oy/HTND

RUN apt-get update && apt-get install -y curl git openssh-client binutils gcc musl-dev

COPY go.mod .
COPY go.sum .

# Cache htnd dependencies
RUN go mod download

COPY . .

# Build the binary with CGO disabled for static linking to ensure Alpine compatibility
RUN go build -tags "deadlock pebblegozstd" -o HTND .
RUN go build -tags "deadlock pebblegozstd" -o htnwallet ./cmd/htnwallet
RUN go build -tags "deadlock pebblegozstd" -o htnminer ./cmd/htnminer
RUN go build -tags "deadlock pebblegozstd" -o htnctl ./cmd/htnctl
RUN go build -tags "deadlock pebblegozstd" -o genkeypair ./cmd/genkeypair

# --- multistage docker build: stage #2: runtime image
FROM ubuntu:24.04
WORKDIR /app

RUN apt-get update && \
  apt-get install -y --no-install-recommends ca-certificates && \
  rm -rf /var/lib/apt/lists/*

# Copy the binary from the build stage
COPY --from=build /go/src/github.com/Hoosat-Oy/HTND/HTND /app/HTND
COPY --from=build /go/src/github.com/Hoosat-Oy/HTND/htnwallet /app/htnwallet
COPY --from=build /go/src/github.com/Hoosat-Oy/HTND/htnctl /app/htnctl
COPY --from=build /go/src/github.com/Hoosat-Oy/HTND/htnminer /app/htnminer
COPY --from=build /go/src/github.com/Hoosat-Oy/HTND/genkeypair /app/genkeypair

RUN mkdir -p /nonexistent/.htnd && chown nobody:nogroup /nonexistent/.htnd && chmod 700 /nonexistent/.htnd

# Set ownership and permissions for the binary
RUN chown nobody:nogroup /app/* && chmod +x /app/*


USER nobody
ENTRYPOINT ["/app/HTND"]
CMD ["--utxoindex", "--saferpc"]
