# NHB and ZNHB transfers

NHB transfers use `TxTypeTransfer` (`0x01`) and ZNHB transfers use
`TxTypeTransferZNHB` (`0x10`). Both are submitted as signed native transactions
through `nhb_sendTransaction` ([signing](./signing.md),
[rpc.md](../api/rpc.md#transaction-encoding)).

## Authenticated submission

`nhb_sendTransaction` requires auth (`requireAuthInto` in `rpc/http.go`): a JWT in
`Authorization: Bearer <token>` (or a verified client certificate when the server
requires them). The check runs before the payload is parsed. Keep the token on
trusted server infrastructure and forward already-signed transactions; do not put
it in a browser or mobile client. After validation the handler calls
`Node.AddTransaction`, which admits the transaction to the mempool (see
[rpc.md](../api/rpc.md) for the error responses).

The CLI does this for you: `nhb-cli send-nhb` and `nhb-cli send-znhb` take
`[--rpc <url>] [--gas <limit>] [--gas-price <price>] <recipient> <amount> <key_file>`,
read the token from `NHB_RPC_TOKEN` and the endpoint from `--rpc` or `RPC_URL`
(default `http://localhost:8080`). `--gas` defaults to `21000` for NHB and
`25000` for ZNHB; `--gas-price` defaults to `1`. `amount` is a positive integer in
wei (`cmd/nhb-cli/send.go`).

## 1. Read the nonce

`nhb_getBalance` returns `nonce`, `balanceNHB` and `balanceZNHB`
([rpc.md](../api/rpc.md#nhb_getbalance)). Use `nonce` as the transaction `nonce`;
a lower value is rejected.

## 2. NHB transfer (`0x01`)

Fields: `chainId` (`0x4e4842`), `type` `1`, `nonce`, `to` (base64 of the 20-byte
address), `value` (wei), `gasLimit` (> 0), `gasPrice` (> 0), signature. The value
and recipient rules enforced in `core/state_transition.go`: the recipient must be
20 bytes and non-zero, and `value` must be positive.

`TxTypeTransfer` takes a native fast path inside `applyEvmTransaction`: balances
are updated by the state processor rather than by running EVM code. The receipt's
gas used is the transaction's `gasLimit`.

## 3. ZNHB transfer (`0x10`)

Same fields with `type` `16`; `value` is in ZNHB wei. `applyTransferZNHB` debits
the sender's ZNHB balance, credits the recipient (creating the account if
needed), and emits a `transfer.native` event with `asset` `ZNHB`
([events](../api/events.md)). It fails with `znhb transfer: insufficient balance`
if the sender's ZNHB balance is less than the amount plus the fee below, and with
`ErrTransferZNHBPaused` when governance has paused the `transfer_znhb` module.

## Fees

Transfer fees are a protocol-enforced percentage of the transferred value, not
`gasLimit * gasPrice`; the gas fields are required by the RPC but the fee is
computed by `TransferGasPolicy.ComputeFee` (`core/transfer_gas_policy.go`):

* The fee is charged in the asset being transferred: NHB for `0x01`, ZNHB for
  `0x10`. It is `value * bps / 10000` (integer division), added to what the
  sender must hold.
* A sender is fee-free while the cumulative amount it has sent in that asset in
  the current window is below `TransferFreeTierSpendWei`. The window is
  `lifetime` or `monthly` (`TransferFreeTierWindow`); the counter is kept per
  asset. `fees_getTransferStatus` and `fees_getTransferQuote`
  ([fees-query](../api/fees-query.md)) report eligibility and the quoted fee.
* Config (`[global.Fees]`) defaults in `config/config.go`:
  `TransferFreeTierSpendWei = 1000e18` (1000 tokens), `TransferFreeTierWindow =
  "lifetime"`, `TransferFeeBps = 20` (NHB), `TransferFeeBpsZNHB = 10` (ZNHB),
  plus `TransferFeeCollector`. If `TransferFreeTierSpendWei` is zero or less the
  policy is marked disabled (`buildTransferGasPolicyFromConfig`).
* The fee is credited to the fee collector wallet. For ZNHB fees that land on the
  admin wallet, the amount is also added to the ZNHB reward pool ledger so the
  supply invariant holds.
* For an NHB transfer with a valid paymaster signature and an enabled paymaster
  module, the sponsor's NHB balance pays the fee instead of the sender
  (`EvaluateSponsorship`, `core/sponsorship.go`). A rejected sponsorship fails the
  transaction with `ErrSponsorshipRejected`.

The separate domain fee (`fees.applied` event) applies only when `merchantAddr`
names a configured fee domain; see [fee policy](../fees/policy.md).

## Result and receipt

`nhb_sendTransaction` returns the transaction hash. Poll
`nhb_getTransactionReceipt` for the receipt; its `Transfer` log carries `asset`,
`from`, `to`, `value`, `txHash` ([rpc.md](../api/rpc.md#nhb_gettransactionreceipt)).
