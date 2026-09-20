# Deployment examples

Reference deployment artefacts for htnd. Copy and adapt — none of these is applied automatically.

| File | What it is |
|---|---|
| [`docker-compose.yml`](docker-compose.yml) | Steady-state node. No repair flags. |
| [`docker-compose.repair.yml`](docker-compose.repair.yml) | One-shot recovery runs, profile-gated so they cannot start by accident. |
| [`htnd.service`](htnd.service) | Sample systemd unit. Owns restarts. |

## The one rule these files exist to enforce

**Repair flags never belong in a launch command.**

`--repair-block-statuses` and `--repair-missing-multisets` are one-shot recovery steps. Putting one
in a compose file or a systemd `ExecStart` means it re-runs on every restart.

That is not hypothetical. `--repair-block-statuses true` was baked into a live docker-compose launch
command instead of being run once. Because it re-marks every block that is neither invalid nor
header-only as UTXO-valid, it left blocks with no stored multiset; virtual's own selected parent was
one of them, so every `GetBlockTemplate` call failed with `Multiset <hash> does not exist in db`.
The pool saw miners connected at 0 H/s, and nothing in the logs connected that to a flag set much
earlier. Full walkthrough: [`docs/runbooks/stratum-zero-hashrate.md`](../docs/runbooks/stratum-zero-hashrate.md).

Since this branch, htnd logs a warning at **every** start while either flag is set. If you see it
during a normal run, the flag is still in your configuration and should come out.

`docker-compose.repair.yml` keeps these runs in a separate file, under a `repair` profile, with
`restart: "no"` — so a repair cannot start by accident and cannot survive into a normal
`docker compose up`.

## Restarts are the supervisor's job

htnd does **not** start its own replacement after an automatic update any more (HTN-164). It used to
`exec` a second htnd and then `os.Exit(0)`, which started the new process while the old one still
held the database lock and skipped every deferred shutdown — including closing the database.

It now asks for a graceful shutdown and stops. Something has to bring it back:

- Docker: `restart: unless-stopped` (set in `docker-compose.yml`).
- systemd: `Restart=always` (set in `htnd.service`).

**If you run htnd with auto-install enabled and no supervisor, it will stop after an update and stay
stopped.** It warns about this on every restart request.

## Shutdown timing

`app.main` allows up to 2 minutes for a graceful shutdown: it stops the component manager, then
closes the database. Killing it earlier is what leaves a datadir needing WAL recovery.

Both examples allow generously more than that — `stop_grace_period: 5m` and `TimeoutStopSec=300`.
Reduce them only if you know your node shuts down faster.

## Auto-update is off in these examples

Both examples pass `--autoupdate=false`. If you turn it on, read HTN-162 first: an update installs
only when its checksum appears in a list signed by the key given in `--autoupdate-public-key`, and
**no key ships in this repository**. Without one, auto-install refuses to run. Do not reach for
`--autoupdate-allow-unverified` to get past that on a node holding value.

## RPC exposure

`docker-compose.yml` binds RPC to `127.0.0.1` on purpose, and passes `--saferpc`, which disables
state-changing RPC calls.

The current message-size limits are large — 1 GiB for RPC, 4 GiB for P2P — and there is no
public/authenticated endpoint split yet (HTN-166, open; see
[`docs/REMEDIATION_STATUS.md`](../docs/REMEDIATION_STATUS.md)). Until that is resolved, treat an
exposed RPC port as something to put behind authentication or a trusted network, not as something to
publish.
