#!/bin/sh
export GOTOOLCHAIN=go1.27.1
cd "$(dirname "$0")"
go build -tags pebblegozstd -o htnd .
go build -tags pebblegozstd -o htnctl ./cmd/htnctl
