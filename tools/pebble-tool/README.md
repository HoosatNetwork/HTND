# pebble-tool — removed (HTN-177)

This directory used to contain `pebble-tool`, a 27,607,704-byte x86-64 ELF executable, committed
without any source.

It has been deleted. There is nothing to build here yet.

## Why

The binary was a read **and write** tool pointed at pebble datadirs, and it could not be rebuilt,
reviewed or reproduced from this repository:

- No Go source for it has ever existed in this repo. `git log --all -- tools/pebble-tool` shows both
  the binary and the previous version of this README arriving inside commit `7148270f0`, whose
  actual subject is an unrelated WAL setting revert.
- The previous README's `cd tools/pebble-tool && go build .` instructions described a source layout
  that was never present, so anyone following them got the committed binary rather than one they
  had built.

An unreviewable binary that modifies node databases is a supply-chain risk, and shipping it in
every clone is a reproducibility problem regardless of whether the binary is benign.

## If you need the tool back

Either recover the original source and commit it, or rewrite it deliberately with review. Do not
restore the binary. `tools/` already has `pruningproof-harness`, and `cmd/ldbtool` and
`cmd/utxoforensics` cover parts of the same ground; one of those may be the better place to add the
operation you need.

Whatever replaces it inherits the standing safety rule for every offline datadir tool: pebble
replays its WAL on open, so run it on a **copy** of a datadir, never on a live node's directory.
