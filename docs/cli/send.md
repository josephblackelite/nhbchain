# `send-nhb` and `send-znhb` commands

The `send-nhb` and `send-znhb` subcommands broadcast NHB and ZapNHB
transfers respectively. Each command constructs the appropriate
transaction type (`TxTypeTransfer` for NHB, `TxTypeTransferZNHB` for
ZapNHB), signs it with the provided key, submits it to the configured RPC
endpoint, and prints the resulting transaction hash so you can track
settlement.

Both helpers accept a `--rpc` flag to override the HTTP endpoint (falling
back to the `RPC_URL` environment variable or `http://localhost:8080`)
and a `--gas` flag to override the gas limit. If `--gas` is not supplied,
`send-nhb` defaults to `21000` and `send-znhb` defaults to `25000`.

## Usage

### Send NHB

```bash
./nhb-cli send-nhb [--rpc <url>] [--gas <limit>] <recipient> <amount> <key_file>
```

### Send ZapNHB

```bash
./nhb-cli send-znhb [--rpc <url>] [--gas <limit>] <recipient> <amount> <key_file>
```

For both commands:

- `recipient` – Hex-encoded account address (either NHB bech32 or 0x).
- `amount` – Transfer amount in wei.
- `key_file` – Path to the locally stored wallet private key.
- `--rpc` – Optional RPC endpoint override.
- `--gas` – Optional gas limit override.

## Examples

```bash
$ ./nhb-cli send-nhb nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq4u3h4 1000000000000000000 wallet.key --rpc http://localhost:8080
Broadcasted NHB transfer: 0x4c1b9d265df98a6df6925c1370f9d8b2f047f53a6b0f39db16355e1c5fb2d3af

$ ./nhb-cli send-znhb nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq4u3h4 500000000000000000 wallet.key --gas 32000
Broadcasted ZNHB transfer: 0xa9a6f4d59e11cce45bfb0fb89f743ad39df0cedf0e09a0e02ff80db152df2b03
```

The CLI exits non-zero if the transaction fails to sign or the RPC
returns an error. Monitor `nhb_getTransactionReceipt` using the printed
hash to confirm inclusion.
