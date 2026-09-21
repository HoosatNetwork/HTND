#!/bin/sh
set -eu
cd "$(dirname "$0")"
go build -o htnd .
./htnd --version || true
