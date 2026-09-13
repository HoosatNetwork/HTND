# CLAUDE.md

HTND is the Hoosat Network full node, written in Go (module `github.com/HoosatNetwork/HTND`). It is a fork of kaspad, which descends from btcd. It is a GHOSTDAG BlockDAG with Hoohash proof-of-work. Many identifiers, comments and patterns still follow kaspad upstream, so kaspad knowledge usually applies.

## Build, lint, test

```sh
go build -o htnd .                          # the node (main.go -> app.StartAppWithConfig)
go build -o /tmp/x ./cmd/<tool>             # a single tool
go install . ./cmd/...                      # everything
./build_and_test.sh                         # the CI gate: gofmt, staticcheck subset, build, go test -p 4 ./...
go test ./domain/consensus/processes/pruningmanager/ -run TestName -v
go test -tags=ci ./...                      # what CI runs; skips tests guarded by ci.SkipLongTest
```

- **Go version:** `go.mod` requires Go 1.27. The CI workflows still pin `setup-go` to 1.26 and depend on toolchain auto-download.
- **Release and Docker builds** set `GOEXPERIMENT=simd,jsonv2`. Docker also uses `CGO_ENABLED=1 -tags "deadlock pebblegozstd"`. A plain `go build` works without any of these.
- **Formatting and lint:** code must be `gofmt`-clean. `.hooks/pre-commit`, wired up through `lefthook.yml`, runs gofmt and staticcheck on staged packages. `build_and_test.sh` runs staticcheck with an explicit list of checks; `.golangci.yml` exists but CI doesn't use it.
- **Long tests:** call `ci.SkipLongTest(t, reason)` from `internal/ci`. It only skips when the `ci` build tag is set.
- **Race job:** `.github/workflows/race.yaml` runs nightly with `-race -p 2` on master and on the newest `vX.Y.Z-dev` branch. Race binaries use roughly 10x the memory, so keep package parallelism low.
- **Test output:** `NewTestConsensus` creates its data dirs under `./.tmp/` inside the package being tested. These are gitignored and can be deleted.
- **Stability tests** live in `stability-tests/`. They are separate binaries driven by `install_and_test.sh` (`SLOW=1` runs everything, which takes hours), or run a single one with `<test>/run/run.sh`.
- **Integration tests** in `testing/integration` start real in-process nodes.
- **Protobuf:** `go generate` inside a `pb`/`protowire`/`serialization` package runs `protoc` with `--go_out` and `--go-vtproto_out`. `*.pb.go` files are generated; don't hand-edit them.

## Layout

| Path | What lives there |
|---|---|
| `main.go` | Loads config, raises rlimit, sets up panic auto-report and optional pprof (`HTND_PROFILER` → 127.0.0.1:6060), then calls `app.StartAppWithConfig`. |
| `app/` | `app.go` opens the DB (pebble by default, `--dbtype=leveldb` optional). `component_manager.go` wires domain, netadapter, addressmanager, connmanager, protocol, rpc, utxoindex and autoupdate. |
| `app/appmessage/` | Transport-agnostic message types for P2P (`p2p_*`) and RPC (`rpc_*`), plus domain converters. |
| `app/protocol/` | P2P flows. The current protocol version is `flows/v8`; `register.go` maps message commands to flow funcs. Also contains handshake, IBD and block/tx relay. |
| `app/rpc/` | `rpc.go` has the `handlers` map from command to `rpchandlers.HandleX`. Handlers take `(*rpccontext.Context, *router.Router, appmessage.Message)`. |
| `domain/domain.go` | Wraps consensus, the staging consensus used for pruning-point IBD (swapped in atomically by `CommitStagingConsensus`), and the mining manager. |
| `domain/consensus/` | The consensus engine. See the next section. |
| `domain/dagconfig/` | Network `Params` (mainnet `hoosat-mainnet` P2P 42421 / RPC 42420; testnets use 42423 / 42422) and genesis blocks. Several params are slices indexed by block version. |
| `domain/miningmanager/` | Mempool, including Hoosat additions: compound-tx rate limiting and priority, and wallet freezing. Also the block template builder. |
| `domain/utxoindex/` | Optional (`--utxoindex`) address→UTXO index. It's secondary state; RPC filters its results against virtual's UTXO set. |
| `domain/exodus/` | Exodus pruning-point bundles (create/verify/diff/import). See `docs/exodus-pruning-point.md`. |
| `domain/prefixmanager/` | DB key prefixes that separate the active consensus from the staging one. |
| `infrastructure/config/` | All CLI flags (`go-flags` struct tags) and defaults. `sample-htnd.conf`. |
| `infrastructure/db/database/{pebble,ldb}` | Key-value backends behind the `database.Database` interface. |
| `infrastructure/network/` | netadapter (gRPC server and router), connmanager, addressmanager, dnsseed, rpcclient. `netadapter/server/grpcserver/protowire` holds the `.proto` files and appmessage↔protowire converters. |
| `infrastructure/logger/` | Subsystem loggers. Each package has a `log.go` with `log = logger.RegisterSubSystem("XXXX")` and `spawn = panics.GoroutineWrapperFunc(log)`. Start goroutines with `spawn`, not a bare `go`. |
| `infrastructure/autoupdate/` | GitHub release auto-updater and panic issue reporting. |
| `util/` | Addresses (bech32 `hoosat:` / `hoosattest:`), amounts, difficulty, `mstime`, `txmass`, `staging.CommitAllChanges`. |
| `cmd/` | `htnctl` (RPC CLI), `htnwallet` (wallet plus gRPC daemon), `htnminer` (CPU miner), `genkeypair`, `htnexodus`, `ldbtool`, `utxoforensics` (offline datadir forensics). |
| `tools/` | `pebble-tool` (inspect or modify a pebble DB), `pruningproof-harness`. |
| `docs/` | `script-engine.md` (txscript VM), `utxo-survey.md` (`HTND_UTXO_SURVEY*` IBD failure JSONL), `exodus-pruning-point.md`, `running-a-node-in-ubuntu.md`. |

Untracked or ignored local artifacts: `c5-runs/` (survey/forensics run outputs with copied datadirs), `utxoforensics`, and `cmd/htnctl/htnctl` binaries.

## Consensus architecture (`domain/consensus`)

- **`model/`** holds interfaces only: `interface_datastructures_*.go` (stores) and `interface_processes_*.go` (managers). `model/externalapi` is the public `Consensus` interface and domain types (`DomainHash`, `DomainBlock`, `UTXOEntry`, …). `model/testapi` holds `TestConsensus` and the test-only extensions.
- **`datastructures/<x>store`** implements stores backed by `database/` with `serialization/dbobjects.proto`, usually with an LRU cache in front.
- **`processes/<x>manager`** implements the logic: ghostdag, reachability, dagtraversal, difficulty, pastmediantime, blockvalidator, consensusstatemanager (virtual resolution and UTXO verification), pruningmanager, pruningproofmanager, finalitymanager, mergedepth, syncmanager, transactionvalidator, blockbuilder, coinbasemanager.
- **`factory.go`** builds every store and process and wires them into `consensus.go`, which serializes public calls under one lock.
- **Writes** go through a `model.StagingArea`. Stores `Stage*` changes into per-store shards, and `staging.CommitAllChanges` commits all shards in one DB transaction. Reads pass the same `stagingArea` so they see the staged data.
- **`ruleerrors/`** holds typed consensus rule violations. Compare them with `errors.Is`.
- **`utils/`** contains `txscript` (script VM), `utxo` (UTXO collections and diffs), `multiset` (MuHash UTXO commitment), `consensushashing`, `pow` (Hoohash; optional C library via `--use-hoohash-c-library`, default on linux/arm64 with cgo), `constants`, `testutils`, and `utxosurvey`.

### Hoosat-specific gotcha: the block-version global

`constants.GetBlockVersion()` is a **process-global** that starts at 1 and only increases as blocks arrive (`SetBlockVersion`). Many `dagconfig.Params` methods read it, including `FinalityDepth`, `PruningDepth`, `TargetTimePerBlockForCurrentVersion`, K and the DAA window. A value copied out of params when an object is **constructed** depends on when construction happened: at startup through `domain.New`, or mid-run through a staging consensus during IBD. It doesn't depend on the DAG. Version 5+ changed the finality and pruning depths, and version 6 is the "Zenith" hard fork. When touching these params, decide whether you need the value per call or frozen, and check both construction sites.

## Testing conventions

```go
testutils.ForAllNets(t, true /*skipPow*/, func(t *testing.T, cfg *consensus.Config) {
    tc, teardown, err := consensus.NewFactory().NewTestConsensus(cfg, "TestName")
    if err != nil { t.Fatalf("%+v", err) }
    defer teardown(false)
    // tc.AddBlock / BuildBlockWithParents / ValidateAndInsertBlock ...
})
```

- `HTND_TEST_MODE=true` bypasses the nearly-synced check in some RPC handlers.
- Regenerate the ghostdag fixture data with `UPDATE_GHOSTDAG_FIXTURES=1 go test ./domain/consensus/processes/ghostdagmanager -run TestUpdateGHOSTDAGFixtures -count=1`.
- `TXSCRIPT_TRACE` traces script execution.

## Adding an RPC command

Touch every layer, following an existing command such as `GetBlockCount`:

1. `protowire/rpc.proto` and `messages.proto` (the oneof payload), then regenerate.
2. `protowire/rpc_<name>.go` converters, plus the case in `protowire/wire.go`.
3. `app/appmessage/rpc_<name>.go` and the command constant in `message.go`.
4. `app/rpc/rpchandlers/<name>.go` and the entry in the `app/rpc/rpc.go` handlers map.
5. `infrastructure/network/rpcclient/rpc_<name>.go`, and `cmd/htnctl/commands.go` if it should be exposed in the CLI.

## Tunables via environment

- **Runtime:** `GOGC`, and `GOMEMLIMIT` (main.go defaults it to 8 GB).
- **Profiling:** `HTND_PROFILER`.
- **Pebble tuning:** `HTND_PEBBLE_CACHE_MB`, `HTND_MEMTABLE_SIZE_MB`, `HTND_BASE_FILE_SIZE_MB`, etc.
- **IBD UTXO survey:** `HTND_UTXO_SURVEY*` (see `docs/utxo-survey.md`).

## Commit style

- **Subject:** a plain-English sentence describing the behavior change, e.g. "Stop relayed compound transactions from expiring before they are mined". Tool-only commits use a prefix, e.g. `utxoforensics: …`.
- **Body:** explains the defect mechanism, why this fix and not an alternative, what was deliberately left alone, and any remaining tension. Wrap at about 80 columns.

## Safety notes

- `utxoforensics`, `pebble-tool`, `ldbtool` and `htnexodus import` open datadirs directly. Pebble replays its WAL on open, so run them on a **copy** of the datadir, never on a live node's directory. Opening a pebble datadir with the leveldb engine destroys it.
- Consensus changes can split the network. Prefer measuring and logging first, and include the reasoning in the commit.
