# HTND Script Engine (txscript)

This document describes how the current HTND “script engine” works, what it can do today, and how to use it from Go code.

In this codebase, the script engine is the transaction script virtual machine (VM) implemented in `domain/consensus/utils/txscript`. It is responsible for validating that a transaction input’s **signature script** (a.k.a. `SignatureScript`) satisfies the **locking script** in the referenced UTXO (`ScriptPublicKey`).

> Terminology quick map
>
> - **scriptSig / SignatureScript**: provided by the spender in each input.
> - **scriptPubKey / ScriptPublicKey**: stored in the output being spent; it defines the spending condition.
> - **script version**: `externalapi.ScriptPublicKey.Version`. Current max supported version is `constants.MaxScriptPublicKeyVersion` (currently `0`).

---

## Where the script engine is used in consensus

Consensus validates scripts as part of transaction validation. The core call site is in `transactionValidator.validateTransactionScripts`, which:

1. Looks up the referenced UTXO entry for each input.
2. Creates (or reuses) a `txscript.Engine` instance.
3. Initializes it with `(scriptPubKey, tx, inputIndex, flags, caches, sighashReusedValues)`.
4. Executes the script pair.

See: `domain/consensus/processes/transactionvalidator/transaction_in_context.go`.

HTND uses an engine pool (`enginePool`) and calls `Reset()` after each validation to reuse allocations.

---

## Engine architecture

### Engine state

`txscript.Engine` tracks:

- A list of scripts to execute (`scripts [][]parsedOpcode`).
  - For standard spends: it executes **two** scripts:
    1. input `SignatureScript` (script 0)
    2. previous output `ScriptPublicKey` (script 1)
  - For P2SH spends: a **third** script is appended (the redeem script) and executed after the first two succeed.
- Two stacks:
  - `dstack`: the main data stack
  - `astack`: the alternate stack
- Conditional execution stack (`condStack`) for `OP_IF/OP_ELSE/OP_ENDIF`.
- An operation counter `numOps` (enforced per script).
- The transaction and input index being validated (`tx`, `txIdx`).
- Signature caches (`sigCache` and `sigCacheECDSA`).
- `sigHashReusedValues`: shared structure to avoid recomputing common sighash intermediates when validating multiple inputs.

Implementation: `domain/consensus/utils/txscript/engine.go`.

### Execution model

The VM runs as follows:

- `Init(...)` parses and verifies the scripts (size limits, push-only constraints for `SignatureScript`, P2SH detection).
- `Execute()` repeatedly calls `Step()` until all scripts are finished.
- At the end, `CheckErrorCondition(true)` enforces that execution is complete and that the top stack element is `true`.

Key details:

- The **alt stack does not persist across scripts**; it is cleared when a script ends.
- It is illegal for conditionals to “straddle” script boundaries. If a script ends while `condStack` is non-empty, the engine fails with `ErrUnbalancedConditional`.

### P2SH behavior in HTND

HTND’s P2SH is a standard “script-hash” pattern:

- A P2SH `ScriptPublicKey` has the form:

  ```
  OP_BLAKE2B <32-byte scriptHash> OP_EQUAL
  ```

  (detected by `isScriptHash` in `txscript/script.go`).

- If the `ScriptPublicKey` is P2SH, `Init()` sets `vm.isP2SH = true` and requires the input `SignatureScript` to be push-only.

- During execution:
  - After script 0 completes, the engine saves the resulting stack (`savedFirstStack`).
  - After script 1 completes, the engine checks success, then:
    - takes the last stack item from script 0 as the redeem script,
    - parses it,
    - appends it as an additional script to execute,
    - restores the stack (minus the redeem script itself), and executes the redeem script.

This matches the classic “scriptSig pushes redeemScript” model.

---

## Consensus limits and validation rules

The engine enforces limits typical of Bitcoin-style script VMs (with HTND-specific values):

- `MaxScriptSize = 10,000` bytes (raw script length).
- `MaxScriptElementSize = 520` bytes (max push size).
- `MaxStackSize = 244` combined elements across data stack + alt stack.
- `MaxOpsPerScript = 201` non-push operations.

See:

- `domain/consensus/utils/txscript/engine.go`
- `domain/consensus/utils/txscript/script.go`

### Push-only signature scripts

During initialization the engine parses `SignatureScript` and requires it to be **push-only**:

- Any opcode with value `> OP_16` is considered non-push and is rejected.

This is enforced in `Engine.Init`.

### Script version behavior

Scripts are versioned via `ScriptPublicKey.Version`.

- Today, the maximum supported version is `constants.MaxScriptPublicKeyVersion` (currently `0`).
- If a `ScriptPublicKey` has a **higher version than supported**, the engine treats it as “unknown” and returns success:
  - `Engine.Init` returns `nil` early.
  - `Engine.Execute` returns `nil` early.

This means outputs created with higher script versions are effectively **anyone-can-spend** under current rules (useful for forward-compat experiments, but it’s an important behavior to understand when designing upgrades).

---

## Signature verification capabilities

HTND supports two public key + signature verification paths:

1. **Schnorr**
   - Public key encoding: 32 bytes (checked by `checkPubKeyEncoding`)
   - Signature length: 64 bytes (checked by `checkSignatureLength`)
   - Uses `consensushashing.CalculateSignatureHashSchnorr`

2. **ECDSA**
   - Public key encoding: 33 bytes (checked by `checkPubKeyEncodingECDSA`)
   - Signature length: 64 bytes (checked by `checkSignatureLengthECDSA`)
   - Uses `consensushashing.CalculateSignatureHashECDSA`

Both paths optionally use signature caches (`SigCache` / `SigCacheECDSA`) to avoid repeated expensive verification.

See the opcode implementations in `domain/consensus/utils/txscript/opcode.go`.

### SigHash type checking

Signatures include a 1-byte sighash type appended at the end. The engine:

- extracts the last byte,
- checks `hashType.IsStandardSigHashType()`, and
- rejects invalid sighash types.

### “NullFail” style rule

The ECDSA checksig implementation enforces a “nullfail” rule: if verification fails and the signature is non-empty, the script fails with `ErrNullFail`.

This reduces some forms of signature malleability and makes failures more strict.

---

## Debugging and introspection

The engine provides several debugging helpers:

- `DisasmPC()` disassembles the next opcode to be executed.
- `DisasmScript(i)` disassembles a whole script (0 = signature script, 1 = public key script, and for P2SH the redeem script becomes script 2).
- `Step()` executes a single opcode.
- `GetStack()` / `GetAltStack()` returns stack contents.

There is also a helper:

- `txscript.DisasmString(version, scriptBytes)` which formats a script as a single-line disassembly (only for the currently supported script version).

### Tracing logs

The `txscript` subsystem logger is registered as `SCRP`. The engine already emits trace logs when executing:

- it logs “stepping” lines with the current PC disassembly,
- it logs stack dumps after steps.

If you enable trace logging for `SCRP`, you can get a very detailed execution trace.

---

## High-level helpers: standard scripts and signing

The package provides standard primitives to build and interpret common scripts:

- `PayToAddrScript(addr)` returns a `ScriptPublicKey` for:
  - pay-to-pubkey (Schnorr)
  - pay-to-pubkey (ECDSA)
  - pay-to-script-hash (P2SH)
- `ExtractScriptPubKeyAddress(scriptPubKey, params)` classifies standard scripts and extracts an address.
- `PayToScriptHashScript(redeemScript)` returns the P2SH locking script.
- `PayToScriptHashSignatureScript(redeemScript, signature)` creates a P2SH spend signature script that pushes `signature` then pushes the `redeemScript`.
- `SignTxOutput(...)` can generate signature scripts for standard scripts using `KeyDB` and `ScriptDB`.

Implementation: `domain/consensus/utils/txscript/standard.go` and `domain/consensus/utils/txscript/sign.go`.

---

## Examples

### Example 1: Validate a single input script pair

This is essentially what consensus does:

```go
vm, err := txscript.NewEngine(
    utxoEntry.ScriptPublicKey(),
    tx,
    inputIndex,
    txscript.ScriptNoFlags,
    sigCache,
    sigCacheECDSA,
    sighashReusedValues,
)
if err != nil {
    return err // parse / init error
}
if err := vm.Execute(); err != nil {
    return err // script failure
}
```

Notes:

- In consensus code, an `Engine` is typically reused from a pool. If you do this yourself, call `vm.Reset()` before putting it back.

### Example 2: Print disassembly on failure

```go
vm, err := txscript.NewEngine(spk, tx, idx, txscript.ScriptNoFlags, sigCache, sigCacheECDSA, reused)
if err != nil {
    return err
}
if err := vm.Execute(); err != nil {
    dis0, _ := vm.DisasmScript(0)
    dis1, _ := vm.DisasmScript(1)
    return fmt.Errorf("script failed: %w\nscriptSig:\n%s\nscriptPubKey:\n%s", err, dis0, dis1)
}
```

### Example 3: Build a CLTV script (CHECKLOCKTIMEVERIFY)

HTND includes `OP_CHECKLOCKTIMEVERIFY` and the consensus tests show how to build a script that enforces a minimum lock time.

From `domain/consensus/timelock_CLTV_test.go`:

```go
builder := txscript.NewScriptBuilder()
builder.AddLockTimeNumber(lockTimeTarget)
builder.AddOp(txscript.OpCheckLockTimeVerify)
builder.AddOp(txscript.OpTrue)
redeemScript, err := builder.Script()
```

This redeem script can then be wrapped into a P2SH output with `PayToScriptHashScript(redeemScript)`.

### Example 4: Build and spend a P2SH output

Lock (output script):

```go
redeemScript := []byte{txscript.OpTrue}
scriptPubKeyBytes, err := txscript.PayToScriptHashScript(redeemScript)
scriptPubKey := &externalapi.ScriptPublicKey{Script: scriptPubKeyBytes, Version: constants.MaxScriptPublicKeyVersion}
```

Spend (input signature script):

```go
sigScript, err := txscript.PayToScriptHashSignatureScript(redeemScript, signatureBytes)
// ... put sigScript into tx.Inputs[i].SignatureScript
```

---

# How the script engine could be made better (Kaspa-inspired directions)

Kaspa’s node (kaspad) has spent a lot of effort on: (a) keeping script execution deterministic and safe under consensus rules, (b) separating “consensus validity” from “policy/standardness”, and (c) preparing for controlled, versioned upgrades.

Below are concrete directions HTND can take that follow those themes.

> This section is intentionally written as design guidance (not a drop-in patch). Each proposal should be evaluated against HTND’s upgrade policy and existing network assumptions.

---

## What it would take to implement a full programmable VM (smart contracts)

HTND’s current script engine is intentionally small and consensus-focused: it’s designed to prove authorization to spend a UTXO under tight, predictable limits.

Building a **full-scale programmable VM** (i.e. smart contracts) is a much larger project. It requires decisions not just about the VM instruction set, but also about the on-chain **state model**, fee metering, upgrade/activation, and tooling.

It’s also worth calling out the trade-off up front: Kaspa’s L1 philosophy has historically leaned toward keeping validation simple and safe. A “general-purpose VM” can be done, but it increases consensus complexity and attack surface, so it needs extremely careful design.

### 1) Define the contract/state model (this drives everything else)

You need to decide how contracts and their state exist on-chain.

Common options:

- **eUTXO-style contracts (UTXO-anchored state)**
  - Contract state lives in one or more UTXOs.
  - A “contract call” is just spending a contract UTXO and creating a new contract UTXO with updated state.
  - Pros: naturally parallel, no global mutable state, easier determinism story.
  - Cons: more complex state composition; contracts must encode state transition rules carefully.

- **Account-based contracts (global mutable state)**
  - Contract has an address/account and persistent storage (key-value) updated by calls.
  - Pros: familiar to EVM-like ecosystems.
  - Cons: higher contention, harder parallelism, introduces more global consensus state and replay/ordering concerns.

HTND is currently UTXO-based, so an eUTXO-style contract model usually fits better with the existing architecture.

### 2) Choose the VM type and its determinism constraints

The VM must be 100% deterministic across nodes, across OS/CPU, and across time.

VM choices you could make:

- **Extend txscript into “Script v1/v2”**
  - Add new opcodes and structured data types.
  - Keep a stack-based interpreter.
  - Pros: minimal conceptual leap; can be activated via script versions.
  - Cons: tends to accrete complexity; developer ergonomics are usually poor.

- **Adopt a bytecode VM (custom)**
  - Define a small, deterministic bytecode with well-defined semantics.
  - Pros: tailored to HTND; can be designed for static metering.
  - Cons: you own everything: compiler, tooling, spec, audits.

- **WASM-based VM (restricted, deterministic subset)**
  - Use WASM as the contract bytecode format, but lock it down:
    - no floating point
    - bounded memory
    - deterministic host functions only
    - deterministic metering
  - Pros: good tooling ecosystem.
  - Cons: “deterministic WASM” is easy to get wrong; host API becomes consensus-critical.

Regardless of VM, you need:

- a written spec (instruction semantics, error behavior, overflow rules, encoding rules)
- a canonical serializer for contract code/state
- stable, test-vector-based validation

### 3) Define the execution environment (“host API”)

Contracts need some way to interact with the transaction and with chain data.

You must decide exactly what contracts can read:

- current transaction fields (inputs/outputs/amounts/locktime)
- the spending input index (like txscript already has)
- referenced UTXO fields (amount, script, daa score)
- block context (daa score, timestamp, past median time) — extremely sensitive for determinism and reorg behavior

And what contracts can do:

- cryptography primitives (hashes, signature verification)
- verify inclusion proofs (if you expose any external commitments)
- emit constraints (e.g. “must create output with script X and value Y”)

No syscalls, no networking, no filesystem, no randomness.

### 4) Add metering: gas / cost model / limits

Full programmability requires a robust resource-accounting system.

At minimum:

- **Instruction cost table** (or other static metering)
- **Memory limits** (max linear memory / heap, max stack depth)
- **Execution step limits** (halting guarantees)
- **I/O limits** (bounds on host calls and proof sizes)

This needs to integrate with fees:

- Decide whether gas is paid in addition to existing fee rules, or whether you map execution costs into a “minimum fee” policy.
- Define what happens on out-of-gas: transaction invalid vs contract call revert-style behavior.

### 5) Define contract deployment and invocation transactions

You need a way to put code on-chain and a way to call it.

Typical approaches:

- **Deployment as a special output type**
  - Create an output that commits to the code hash (and optionally stores code).
  - Code can be stored directly in the output script or in a dedicated “code UTXO”.

- **Invocation as spending a contract/state UTXO** (eUTXO)
  - The input references the contract/state.
  - The unlock data supplies call arguments.
  - The outputs must include the next-state UTXO(s).

You’ll also need:

- canonical argument encoding
- a way to return data (often via outputs or via an execution log committed in the transaction)
- clear rules for failure (invalid tx vs revert that still consumes fees)

### 6) Expand consensus validation pipeline

Script validation today is essentially: “execute these two scripts and require `true` on stack”. A contract VM adds more validation steps.

You would need:

- VM execution integrated into `transactionValidator.validateTransactionScripts` (or an adjacent “contract validator” stage).
- Access to any required context (UTXO set, block context, consensus params).
- Deterministic caching opportunities (similar to `SigCache` and `SighashReusedValues`).

Critically, you need to ensure:

- exact same result across nodes
- bounded runtime per transaction and per block
- stable behavior across upgrades

### 7) Decide how contract state is committed in the UTXO set

If contracts introduce additional state beyond existing UTXO entries, you need to decide:

- is the state just data carried in UTXOs (preferred for eUTXO)
- or do you maintain a separate consensus trie / key-value store

If you create a separate storage layer, you’ll need:

- new DB schema and migration strategy
- proofs or commitments for light clients (optional but often required eventually)
- pruning/IBD behavior defined

### 8) Mempool policy and standardness rules

Even if consensus allows complex contract execution, the mempool usually needs stricter rules to protect nodes:

- max gas per tx / per block
- limits on code size and state size
- limits on host function calls
- deny-list or staged rollout for expensive opcodes

This is the “consensus vs policy” split that mature projects emphasize (Kaspa-style discipline fits here well).

### 9) Tooling: compiler, SDK, debugger, and observability

Without tooling, a programmable VM won’t be usable.

At minimum:

- contract SDK (types, serialization, ABI)
- compiler toolchain (if not scripting directly)
- local execution harness (run a contract call against a mocked chain context)
- on-chain introspection / tracing hooks for debugging
- explorer/RPC support to display contract calls and state transitions

### 10) Rollout plan: versioning and activation

You should treat “programmable VM” as a versioned feature with explicit activation.

Recommended approach:

- Introduce a new `ScriptPublicKey.Version` for contract outputs (e.g. version 1).
- Keep v0 semantics frozen.
- Add an activation mechanism (DAA score threshold / block version / feature bit).
- During pre-activation, treat the new version as non-standard in mempool to avoid accidental funds behavior.

### 11) Testing and assurance (non-negotiable)

Programmability multiplies the consensus state space. You’ll need:

- extensive reference vectors
- differential tests (two independent implementations, if feasible)
- fuzzing (bytecode parser, execution, host calls)
- property tests (determinism, metering monotonicity, halting)
- targeted adversarial tests (DoS patterns, memory blowups, edge-case encodings)

If you want to move toward this, a pragmatic first milestone is often:

1. Add script versioning discipline + policy/consensus flag split.
2. Add a small “contract v1” with a tiny host API and strict metering.
3. Only then consider broadening expressiveness.

## Appendix: Useful code pointers

- Engine + execution: `domain/consensus/utils/txscript/engine.go`
- Script parsing/disassembly: `domain/consensus/utils/txscript/script.go`
- Opcodes: `domain/consensus/utils/txscript/opcode.go`
- Standard scripts + address extraction: `domain/consensus/utils/txscript/standard.go`
- Signing: `domain/consensus/utils/txscript/sign.go`
- CLTV example: `domain/consensus/timelock_CLTV_test.go`
