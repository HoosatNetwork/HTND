package constants

// ReferenceSupplySompi is the total circulating supply, in sompi, of the published balance snapshot
// taken at 2026-08-01 00:00 UTC - the sum of every balance in
// https://shitlist.hoosat.fi/balances-20260801-00.csv (20,523 addresses).
//
// It exists so a node can report how far its own supply has moved from a fixed, published point.
// That number is not a verdict on its own: coinbase emission adds supply continuously, so every
// healthy node's total exceeds a past snapshot and grows every second. What it is good for is
// COMPARING NODES. Two nodes that agree with the network agree with each other; a node whose UTXO
// set has gained coins that do not exist reports more than its peers at the same DAA score, and a
// node that has lost coins reports less.
//
// It is reported alongside the growth rather than only subtracted into it, so that two nodes built
// from different revisions cannot be compared against different references without anyone noticing.
const ReferenceSupplySompi uint64 = 640111049839169443

// ReferenceSupplyDescription names the snapshot the figure above came from, for anyone reading a
// GetInfo response without the source to hand.
const ReferenceSupplyDescription = "balances-20260801-00 (2026-08-01 00:00 UTC)"
