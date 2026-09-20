# HTN-229: the stratum bridge drops block submissions

**This defect is not in HTND.** It is in `htn-stratum-bridge`, a separate project (observed as image
`eritonica/htn-stratum-bridge:latest`, `htn_bridge_v1.7.0`). HTND contains no stratum code.

This document exists because the bug was diagnosed from the node side while answering "the node
isn't mining", and because the bridge repository is not present here — so the analysis is recorded
rather than guessed at in code that cannot be reviewed.

## Impact

Every occurrence costs **a found block and the miner's session**. Severity is high for a pool
operator and zero for the node.

## The node is not the problem

Measured before blaming the bridge, so it could be ruled in or out on evidence:

- 876 submitted blocks accepted in a 10-minute window (459 `StatusUTXOPendingVerification` + 417
  `StatusUTXOValid`), 280 of them in the last 5 minutes.
- **Zero** invalid.
- No disqualifications, no UTXO verification failures, no tolerated issues logged.

The node rejects nothing the bridge submits.

## Mechanism

The bridge logs `error unmarshalling event` together with the raw bytes it tried to parse. Those
bytes are `mining.submit` messages truncated at arbitrary offsets:

```
{"id":4
{"id":4,"metho
{"id":4,"method":"min
{"id":4,"method":"mining.submit",
{"id":4,"method":"mining.submit","params":["hoosat:qr3zp82q
{"id":4,"method":"mining.submit","params":["hoosat:qr3zp8...RETRO10__101","1559","dda97ddc55ddc415","3e7e8b02...d71b68
```

Cutting at *every possible* offset — mid-token, mid-hash, mid-string — is the signature of a reader
that JSON-decodes whatever a single socket `Read` returned, instead of buffering to the newline that
delimits stratum messages. TCP does not preserve message boundaries, so any `mining.submit` large
enough or unlucky enough to span a segment boundary arrives as two partial reads and neither parses.

The bridge then disconnects that exact client 0.13 ms later:

```
10:18:01.924319 error error unmarshalling event ... client_id 2228 ... raw: {truncated submit}
10:18:01.924447 info  disconnecting             ... client_id 2228
10:18:01.924525 info  removed client 2228
```

### Measured over 36 minutes (10:16:54–10:52:33)

| | |
|---|---|
| Unmarshalling errors | 101 |
| Client disconnects | 379 |

Miner uptimes in the bridge's own summary table are single-digit seconds. That is also why the 1h
and 24h hashrate columns read `0,00 H/s`: the per-worker counters restart on every reconnect.

## Proposed fix (in the bridge)

Two independent changes:

1. **Read the stratum socket with a line-oriented reader.** `bufio.Scanner`, or
   `bufio.Reader.ReadString('\n')`, decoding one complete newline-delimited message at a time.
   Stratum is a newline-delimited JSON-RPC protocol; framing must come from the newline, never from
   read boundaries. Give the scanner an explicit maximum token size so an oversized line is an error
   rather than a silent truncation.

2. **Do not drop a mining connection on a parse failure.** Log it, skip the message, keep the
   session. Even with correct framing, one malformed message from one miner should not cost that
   miner its session and its in-flight shares.

The first change alone removes both symptoms. The second is defence in depth, and is what keeps a
future framing bug from again presenting as a total hashrate collapse.

## What was deliberately not done

No code was written for the bridge. Its source is not in this repository, and guessing at the shape
of a reader loop nobody here has read would be worse than describing the defect precisely.

## Related

- **HTN-228** — the bridge's `0 H/s` / `Mining difficulty 0.000000` display. A *separate* artifact,
  caused by network difficulty sitting at the `powMax` floor, not by this bug. Both can be true at
  once, which is what made the original report confusing.
- [`docs/runbooks/stratum-zero-hashrate.md`](runbooks/stratum-zero-hashrate.md) — operator runbook
  that separates this from the two other causes of the same dashboard symptom.
