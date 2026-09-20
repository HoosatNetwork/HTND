#!/bin/sh -ex

FLAGS=$@

go version

export PATH="$(go env GOPATH)/bin:$PATH"

go mod download
go install $FLAGS honnef.co/go/tools/cmd/staticcheck@latest

UNFORMATTED=$(find . -type f -name '*.go' -not -path './vendor/*' -exec gofmt -l {} +)
test -z "${UNFORMATTED}"

# HTN-217: every per-version parameter table (K, TargetTimePerBlock, FinalityDuration,
# DifficultyAdjustmentWindowSize, PruningMultiplier, MaxBlockMass, MaxBlockParents, MergeDepth) must
# be read through a clamped accessor, never indexed raw with GetBlockVersion()-1. A raw index has no
# bounds check, so the first node whose ambient version reaches one past a table's end dies with an
# index-out-of-range instead of reusing the last entry - which is what happens at every hard fork
# where one table was forgotten. Tests may index raw: they pin specific versions on purpose.
#
# Only the bracket form is matched, and whole-line comments are dropped: several of these tables
# carry comments that quote the banned expression to explain why it is banned, and a check that
# fires on its own documentation gets deleted rather than fixed.
RAW_BLOCK_VERSION_INDEXING=$(find . -type f -name '*.go' -not -name '*_test.go' -not -path './vendor/*' \
  -exec grep -Hn -E '\[[^]]*GetBlockVersion\(\)[[:space:]]*-[[:space:]]*1' {} + \
  | grep -v -E '^[^:]*:[0-9]+:[[:space:]]*//' || true)
if [ -n "${RAW_BLOCK_VERSION_INDEXING}" ]
then
  echo "Raw GetBlockVersion()-1 indexing found in non-test code (HTN-217)."
  echo "Use a clamped accessor instead - see Params.KForCurrentVersion and blockVersionIndexForSlice"
  echo "in domain/dagconfig/params.go, or blockversion.Index for a version you already hold:"
  echo "${RAW_BLOCK_VERSION_INDEXING}"
  exit 1
fi

staticcheck -checks SA4006,SA4008,SA4009,SA4010,SA5003,SA1004,SA1014,SA1021,SA1023,SA1024,SA1025,SA1026,SA1027,SA1028,SA2000,SA2001,SA2003,SA4000,SA4001,SA4003,SA4004,SA4011,SA4012,SA4013,SA4014,SA4015,SA4016,SA4017,SA4018,SA4019,SA4020,SA4021,SA4022,SA4023,SA5000,SA5002,SA5004,SA5005,SA5007,SA5008,SA5009,SA5010,SA5011,SA5012,SA6001,SA6002,SA9001,SA9002,SA9003,SA9004,SA9005,SA9006,ST1019 ./...

go build $FLAGS -o htnd .

if [ -n "${NO_PARALLEL}" ]
then
  go test -timeout 40m -p 1 -parallel=1 $FLAGS ./...
else
  # Cap package parallelism to keep memory under control on GHA runners
  go test -timeout 40m -p 4 $FLAGS ./...
fi