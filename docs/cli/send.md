# `send-znhb` command

The `send-znhb` subcommand broadcasts a ZapNHB transfer using the
`TxTypeTransferZNHB` transaction type. It signs the payload with the
provided key, submits it to the configured RPC endpoint, and prints the
resulting transaction hash so you can track settlement.

## Usage

```bash
./nhb-cli send-znhb [--rpc <url>] [--gas <limit>] <recipient> <amount> <key_file>
```

- `recipient` – Hex-encoded account address (either NHB bech32 or 0x).
- `amount` – ZapNHB amount in wei.
- `key_file` – Path to the locally stored wallet private key.
- `--rpc` – Optional HTTP endpoint override. Defaults to `RPC_URL` env var
  or `http://localhost:8080`.
- `--gas` – Optional gas limit override. Defaults to `25000` if omitted.

## Example

```bash
$ ./nhb-cli send-znhb nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq4u3h4 500000000000000000 wallet.key --rpc http://localhost:8080 --gas 32000
Broadcasted ZNHB transfer: 0xa9a6f4d59e11cce45bfb0fb89f743ad39df0cedf0e09a0e02ff80db152df2b03
```

The CLI exits non-zero if the transaction fails to sign or the RPC
returns an error. Monitor `nhb_getTransactionReceipt` using the hash to
confirm inclusion.
