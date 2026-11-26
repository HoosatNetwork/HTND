# htnwallet compatibility notes

## Listing receive addresses

For a derived single-signature wallet, the daemon's `ShowAddresses` RPC
returns three addresses per used index when `includeAll` is true. The order
is P2PK, P2PKH, then P2SH, and `includeAll` takes precedence over
`addressType`.

Clients that discovered ML-DSA-44 receive addresses through `includeAll`
must list them separately after network activation. Set `includeAll` to
false and request `ADDRESS_TYPE_MLDSA44` (4) for P2PKH or
`ADDRESS_TYPE_MLDSA44_P2SH` (6) for P2SH.

For a derived multisig wallet, `includeAll` is ignored. Legacy requests
return one P2SH address per used index. After activation, either explicit
ML-DSA-44 type lists the same ML-DSA-44 multisig P2SH addresses.

An activation refusal from `NewAddress` or `ShowAddresses` returns
gRPC status `Unknown` with an activation message. A node application
error can also return `Unknown`, so clients cannot distinguish those
cases using the status code alone.

## Spendable balances and outputs

`GetBalance.pending` includes funds that the wallet cannot currently
spend because of coin maturity or ML-DSA-44 activation. During
pre-activation, the full balance of each tracked ML-DSA-44 address is
pending. `GetUTXOs` continues to list tracked outputs for inspection.

`GetExternalSpendableUTXOs` applies the activation gate to native
ML-DSA-44 addresses and wallet-tracked ML-DSA-44 script hashes. For
an arbitrary external P2SH address, the script hash alone cannot
identify the redeem script. Its outputs are filtered by age and the
existing coinbase rules.

An explicitly chosen payment or compound destination can receive funds
before ML-DSA-44 activation. Those funds become spendable at activation,
subject to the usual maturity rules.
