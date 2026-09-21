# Runbook: the pool reports 0 H/s

**Symptom.** A stratum pool in front of htnd shows workers at `0.00 H/s`, often with
`Mining difficulty 0.000000`, and miners may connect and disconnect repeatedly.

This symptom has **three unrelated causes** that look identical on the pool dashboard. Two are not
node bugs at all. Work through them in this order, because the first is the only one that is
actually an outage and the only one that is fixed on the node.

---

## Step 1: is the node producing block templates at all?

This is the real failure mode, and the one that took a live network down.

```sh
docker logs htnd 2>&1 | grep -i "does not exist in db" | tail
```

**If you see `Multiset <hash> does not exist in db`:** the node cannot build a template, so the
bridge has nothing to hand miners. Every other number on the pool dashboard is downstream noise.

### Mechanism

`--repair-block-statuses` re-marks every block that is neither invalid nor header-only as
UTXO-valid. That status is taken at its word by the resolve path, so blocks re-marked this way keep
whatever stored data they had — and some of them have **no stored multiset**. When virtual's own
selected parent is one of those blocks, every `GetBlockTemplate` call fails.

The flag is a one-shot recovery step. In the live incident it had been pasted into a
docker-compose launch command, so it re-ran on every restart and kept recreating the state, with
nothing in the logs connecting the symptom to a flag set much earlier.

### Fix

Run the targeted repair **once**, with the node stopped:

```sh
docker compose -f docker-compose.yml down
docker compose -f docker-compose.yml -f docker-compose.repair.yml \
    --profile repair run --rm htnd-repair-multisets
```

Expect exactly one line:

```
RepairMissingMultisets marked N block(s) for re-verification
```

- `N > 0` — it found and fixed the problem. Start the node normally; templates should work again.
- `N == 0` — this was not your problem. Go to step 2. Do not re-run it hoping for a different
  number; it is deterministic.

Then **remove both repair flags from the launch command.** Since this branch, htnd logs a warning at
every start while either is set — if you see

```
--repair-block-statuses is set. This is a ONE-SHOT recovery step, not a setting
```

in a normal run, that is the bug that caused this outage, still present in your configuration.

`RepairMissingMultisets` only marks blocks that are missing a multiset, so it is safe to run on a
healthy node — unlike `--repair-block-statuses`, which is blunt, walks every block before the node
serves RPC, and is what creates this state in the first place. Prefer the targeted one.

---

## Step 2: is the node accepting the blocks the bridge submits?

If templates are being built, check whether submissions are landing:

```sh
docker logs htnd --since 10m 2>&1 | grep -ci "StatusUTXOValid\|UTXOPendingVerification"
docker logs htnd --since 10m 2>&1 | grep -ci "Invalid"
```

A healthy node shows many accepted blocks and **zero** invalid. Measured during the real incident:
876 accepted in a 10-minute window (459 pending-verification + 417 valid), zero invalid.

**If accepted is high and invalid is zero, the node is fine and the problem is in the bridge.** Go
to step 3. Do not change anything on the node — this is the point at which the investigation was
twice sent down the wrong path.

---

## Step 3: distinguishing the two non-node causes

### 3a. Miner sessions restarting every few seconds (HTN-229)

```sh
docker logs <bridge-container> 2>&1 | grep -c "error unmarshalling event"
docker logs <bridge-container> 2>&1 | grep -c "disconnecting"
```

If the bridge logs `error unmarshalling event` with **truncated JSON** — cut at arbitrary offsets,
including mid-token and mid-hash:

```
{"id":4
{"id":4,"metho
{"id":4,"method":"mining.submit","params":["hoosat:qr3zp82q
```

…then the bridge is JSON-decoding whatever a single socket `Read` returned instead of buffering to
the newline that delimits stratum messages. Any `mining.submit` that crosses a TCP segment boundary
fails to parse, and the bridge then disconnects that client within a fraction of a millisecond.

Each occurrence costs **a found block and the miner's session**. Measured over 36 minutes: 101
unmarshalling errors, 379 client disconnects. Per-worker hashrate counters restart on every
reconnect, which is why the 1h and 24h columns read `0.00 H/s` even though blocks are being found.

**This is a bridge defect, not an htnd defect.** HTND contains no stratum code. The fix belongs in
the bridge: read the socket with a line-oriented reader (`bufio.Scanner`, or
`bufio.Reader.ReadString('\n')`) and decode one complete newline-delimited message at a time. A
parse failure should also not, by itself, drop a mining connection. See
[`docs/bridge-HTN-229.md`](../bridge-HTN-229.md).

### 3b. Network difficulty pinned at the floor (HTN-228)

```sh
htnctl get-block-dag-info | grep -i difficulty
```

If difficulty reads `1`, the target is at `powMax` — the floor. Bridges commonly derive both
"Mining difficulty" and "Est. Network Hashrate" from network difficulty; at the floor the computed
pool difficulty underflows to `0.000000`, every worker's hashrate renders as `0.00 H/s`, and the
network estimate degenerates to its own floor (often `10 H/s`).

**Nothing is broken.** The number is an artifact of floor difficulty, not a measurement of miners.
Mining works: during the incident the bridge's own summary showed 24,409 accepted blocks over
2h28m while displaying `0.000000`.

Difficulty lifts only when block production outpaces `targetTimePerBlock`. Measured at the time:
0.83 blocks/sec against a 5 blocks/sec target, so the retarget ratio stayed above 1 and the target
stayed pinned. That is a hashrate question for the operator, not a code fix, and it resolves as
hashrate returns. It is also not instant after a difficulty-window fix: recovery is gated on the
bounded, blue-work-ranked window naturally aging old samples out, which needs real elapsed blocks.

---

## Summary

| Finding | Cause | Action |
|---|---|---|
| `Multiset ... does not exist in db` | Leftover `--repair-block-statuses` | One-shot `--repair-missing-multisets`, then remove both flags |
| Accepted > 0, invalid == 0, truncated JSON in bridge log | Bridge reads without line-buffering (HTN-229) | Fix the bridge; node needs no change |
| Difficulty == 1 | Target at `powMax` floor (HTN-228) | Nothing; display artifact, lifts with hashrate |
