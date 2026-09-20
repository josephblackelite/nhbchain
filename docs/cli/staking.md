# Staking CLI

The staking-related commands of `nhb-cli`. They fall into two groups:

- **Read and preview helpers** that call RPC methods: `stake position`,
  `stake preview`, `stake claim` (retired). These are documented in
  [stake.md](./stake.md). `stake claim` is retired: the CLI reports that and
  exits `1` without contacting the node.
- **Signed-transaction commands** that build a transaction locally, sign it with
  a key file and submit it with `nhb_sendTransaction`: `stake <amount>
  <key_file>`, `un-stake`, `register-validator`, `deregister-validator` and
  `set-reward-beneficiary`. They are described below.

All commands use the global `--rpc <url>` flag (or the `RPC_URL` environment
variable, default `http://localhost:8080`). Submitting a transaction needs
`NHB_RPC_TOKEN` to be set, because `nhb_sendTransaction` requires a bearer token.
`<key_file>` is a raw private-key file (as written by `nhb-cli generate-key`) or a
V3 keystore file with its passphrase in `NHB_KEYSTORE_PASSPHRASE` (see
[keystore.md](./keystore.md)).

## View state

```bash
nhb-cli balance <address>
```

Calls the public `nhb_getBalance` RPC and prints the username, NHB balance,
ZapNHB balance, staked ZapNHB (`Staked`), locked ZapNHB (`Locked`), the delegated
validator if any, whether the address is a registered validator (and when), any
pending unbonds (`ID`, amount, release time, validator) and the account nonce.
The same RPC also returns `pendingStakingRewards` in its JSON result.

## Stake

```bash
nhb-cli stake <amount> <key_file>
```

Sends `TxTypeStake` (`0x06`) with no payload, so the stake goes to the signer's
own address. See [stake.md](./stake.md#legacy-shortcut-stake-amount-key_file).
This is the only stake form that does not name another validator; the CLI has
no command to delegate to a different validator. (The transaction type supports
it through an RLP payload with a `Validator` field, `stakePayload` in
`core/state_transition.go`.)

## Un-stake

```bash
nhb-cli un-stake <amount> <key_file>
```

Sends `TxTypeUnstake` (`0x07`) with `Value` set to the amount, gas limit `21000`
and gas price `1`. On success it prints `Successfully sent un-stake transaction
for <amount> ZapNHB.` Unstaked funds do not return immediately: the node records
a pending unbond (visible in `nhb-cli balance`) that is released after the
staking unbonding period. The period is the governance parameter
`staking.unbondingDays` and defaults to 7 days when that parameter is unset
(`unbondingPeriod` and `stakingUnbondingPeriod` in `core/state_transition.go`). Claiming a matured unbond is a separate transaction,
`TxTypeStakeClaim` (`0x0D`, payload `unbondingId`, which must be greater than
zero); a successful claim emits the `stake.unbondClaimed` event
(`core/state_transition.go`, `core/events/stake.go`). `nhb-cli` has no command for
the claim.

As with the legacy `stake` form, errors are printed to stdout and the process
exits `1`.

## Register as a validator candidate

```bash
nhb-cli register-validator <amount> <key_file>
```

Sends `TxTypeStake` with the `RegisterValidator` flag set (payload
`RLP{Validator: empty, RegisterValidator: true}`), `Value` set to `<amount>`,
gas limit `50000`, gas price `1`. `<amount>` is a non-negative base-10 integer:

- `0` registers the account without staking anything more (the "pure
  registration" path in `applyStake`); this is for an account that already holds
  enough stake.
- A positive amount stakes that much to the signer's own address and registers in
  the same transaction.

Registration only sets the account's `ValidatorRegistered` flag. Whether the
address becomes an eligible validator is decided by `setAccount` in
`core/state_transition.go`, which requires all of:

1. the account is registered;
2. the account is not delegating its own stake to a different validator;
3. its eligibility basis, which is its total `Stake` (including ZNHB delegated in
   by other wallets, with nothing subtracted), is at least the minimum validator
   stake.

The minimum is the governance parameter `staking.minimumValidatorStake`; when it
has never been set it defaults to 10,000 ZNHB (`10000000000000000000000` base
units, `defaultMinimumValidatorStakeWei` in `native/governance/types.go`). Reward
accrual is computed separately from this (`stakeRewardBasis`) and excludes
stake delegated in by other wallets.

Exit status is `1` on any failure and `0` on success.

## De-register

```bash
nhb-cli deregister-validator <key_file>
```

Sends `TxTypeUnstake` with the `DeregisterValidator` flag set and zero value
(gas limit `50000`, gas price `1`). It clears the `ValidatorRegistered` flag
without unstaking anything, and works even for an account that has already fully
unstaked (`applyUnstake`). The flag can only be cleared when the account's unbond
target is itself (self-stake). Exit status is `1` on failure, `0` on success.

## Redirect validator reward payouts

```bash
nhb-cli set-reward-beneficiary <address|""> <key_file>
```

Sends `TxTypeSetRewardBeneficiary` (`0x1A`) with an RLP payload carrying the
beneficiary string, gas limit `21000`, gas price `1`. An empty string (`""`)
clears the redirect. It is signed by the validator's own key, so run it where
that key is available. Exit status is `1` on failure, `0` on success.
