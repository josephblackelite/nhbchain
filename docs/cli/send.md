# `send-nhb` and `send-znhb` commands

`send-nhb` and `send-znhb` broadcast NHB and ZapNHB (ZNHB) transfers. Each
command builds a transaction (`TxTypeTransfer`, value `0x01`, for NHB;
`TxTypeTransferZNHB`, value `0x10`, for ZNHB), signs it with the key file you
give it, submits it with `nhb_sendTransaction`, and prints the transaction hash
the node returns. The implementation is in `cmd/nhb-cli/send.go`.

## Usage

```bash
nhb-cli send-nhb  [--rpc <url>] [--gas <limit>] [--gas-price <price>] <recipient> <amount> <key_file>
nhb-cli send-znhb [--rpc <url>] [--gas <limit>] [--gas-price <price>] <recipient> <amount> <key_file>
```

Arguments:

- `recipient` -- bech32 account address (for example `nhb1...`). The CLI decodes
  it with `crypto.DecodeAddress`, which accepts bech32 only. A `0x` hex address
  is rejected with a "parsing recipient address" error.
- `amount` -- transfer amount as a positive base-10 integer, in the asset's
  smallest unit. Zero, negative and non-integer values are rejected.
- `key_file` -- path to the signing key: a raw private-key file such as the
  `wallet.key` written by `nhb-cli generate-key`, or an encrypted V3 keystore
  (see [`keystore`](./keystore.md)).

Flags:

- `--rpc <url>` -- RPC endpoint. The default is the `RPC_URL` environment
  variable if set, otherwise `http://localhost:8080`. `--rpc` (or `--rpc=<url>`)
  is also accepted as a global flag anywhere on the command line.
- `--gas <limit>` -- gas limit. Default `21000` for `send-nhb` and `25000` for
  `send-znhb`. Must be greater than zero.
- `--gas-price <price>` -- gas price. Default `1`. Must be greater than zero.
  Raise it above `1` to replace a transaction from the same account and nonce
  that is already pending. The mempool replaces a same-nonce transaction only if
  the new gas price times gas limit is higher than the pending one's; otherwise
  it rejects it with "transaction with nonce N already exists and fee is not
  higher" (`core/node.go`).

Flags must come **before** the three positional arguments. The parser is Go's
standard `flag` package, which stops at the first non-flag argument, so a
`--gas` placed after `<key_file>` is counted as an extra positional argument and
the command fails with "expected recipient, amount, and key file". (`--rpc` is
the exception, because the global flag pass removes it first.)

`NHB_RPC_TOKEN` must be set: `nhb_sendTransaction` requires a bearer token, and
the CLI refuses to send without one ("privileged RPC call requires
NHB_RPC_TOKEN to be set").

## Examples

```bash
export NHB_RPC_TOKEN=<token>

$ nhb-cli send-nhb --rpc http://localhost:8080 nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgp6q0uya 1000000000000000000 wallet.key
Broadcasted NHB transfer: 0x<64 hex characters>

$ nhb-cli send-znhb --gas 32000 nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgp6q0uya 500000000000000000 wallet.key
Broadcasted ZNHB transfer: 0x<64 hex characters>
```

The printed value is the string returned by `nhb_sendTransaction`, which is
`0x` followed by the hex transaction hash (`rpc/http.go`,
`handleSendTransaction`). A successful return means the mempool accepted the
transaction, not that it is in a block.

## Failure behavior

The command prints `Error: ...` to stderr and exits with status `1` when the
key cannot be loaded, the recipient or amount is invalid, the account lookup
fails, signing fails, or the node returns an error. Node errors are printed as
`error from node: <message>: <detail>`; for example an already-used nonce is
reported by the node as `nonce N has already been used; current account nonce is M`.

To confirm inclusion, call `nhb_getTransactionReceipt` with the printed hash
(the node returns `null` when it cannot find the transaction).
