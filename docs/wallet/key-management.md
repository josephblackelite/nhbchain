# Wallet Key Handling

How keys are created, stored and used by the tools in this repository (`cmd/nhb-cli`, `crypto/keystore.go`). Wallet applications outside this repository make their own choices; the node only sees signed transactions.

## Signing model

* Accounts are secp256k1 keys. An address is derived from the public key (`PrivateKey.PubKey().Address()`) and printed as bech32 with the `nhb` prefix.
* Every state-changing action a user takes is a transaction signed with the account key (`Transaction.Sign`). The chain identifies the sender by recovering the signer; there is no username/password or bearer-token authorization for account actions. Bearer tokens (JWT) are used only to authenticate calls to selected RPC methods.
* Some flows use additional signatures over JSON envelopes (for example delegated escrow actions, escrow arbitration decisions and mint vouchers). Those envelopes are documented with their features, for example [`../escrow/escrow.md`](../escrow/escrow.md).

## `nhb-cli` key files

* `nhb-cli generate-key` creates a new key and writes the raw private-key bytes to `wallet.key` in the current directory with file mode `0600`, then prints the address. It never overwrites an existing `wallet.key`: it exits 1 and leaves the file untouched. With `generate-key --force` it first copies the existing file byte for byte to `wallet.key.bak-<UTC time>` (created exclusively, mode `0600`), and replaces the key only once that copy is on disk; if the copy fails nothing is changed. Any other argument is an error (`generateKey`, `cmd/nhb-cli/main.go`). Transaction commands take the key file path as an argument (for example `claim-username <username> <key_file>`).
* `loadPrivateKey` (`cmd/nhb-cli/main.go`) accepts either:
  * a plaintext key file (raw key bytes), or
  * an encrypted Ethereum V3 keystore JSON file. For a keystore the passphrase is read from the environment variable `NHB_KEYSTORE_PASSPHRASE`; if it is unset the command fails.
* A file that is empty, missing, or contains the deprecated placeholder material is rejected with an instruction to run `generate-key`.

## Importing an existing key into a keystore

```bash
NHB_KEYSTORE_IMPORT_PRIVATE_KEY=<hex private key> \
NHB_KEYSTORE_IMPORT_PASSPHRASE=<passphrase> \
nhb-cli keystore import --out <path>
```

`nhb-cli keystore import` (`cmd/nhb-cli/keystore_cmd.go`):

* Reads the hex key (with or without `0x`) and the passphrase from the two environment variables above. They are read from the environment on purpose, not from arguments, so they do not appear in shell history or process listings.
* Writes an encrypted Ethereum V3 keystore (`crypto.SaveToKeystore`, scrypt with the standard parameters, parent directory created with mode `0700`).
* Reads the file back, decrypts it with the same passphrase, checks that the recovered address equals the imported key's address, and prints the address so you can confirm it is the wallet you meant to import. The command cannot know whether the key you supplied is the intended one.

## Authenticating to the node from the CLI

Commands that send a transaction call `nhb_sendTransaction` with a bearer token from the `NHB_RPC_TOKEN` environment variable and fail with `privileged RPC call requires NHB_RPC_TOKEN to be set` otherwise. `nhb-cli rpc-token [--secret-stdin] [--ttl <duration>] [--issuer <name>] [--audience <a,b>]` prints such a token (HS256, default lifetime 10 minutes, at most 24 hours; issuer default `nhb-rpc`, audience default `wallets`), signed with the node's JWT secret, which it reads from `NHB_RPC_JWT_SECRET` or from standard input with `--secret-stdin`, never from an argument. It is meant to be run on the node host: `export NHB_RPC_TOKEN="$(nhb-cli rpc-token)"` (`cmd/nhb-cli/rpc_token.go`). The token authenticates the call to the node; the transaction itself is still authorized by the account key's signature.

## What the node and gateways do with keys

The node, the escrow gateway and the identity gateway never receive private keys through their APIs; they receive signatures. Services that sign on their own behalf (for example the escrow gateway's relayer) load a key from an environment variable at start-up; see [`../escrow/nhbchain-escrow-gateway.md`](../escrow/nhbchain-escrow-gateway.md).

For wallet SDK material see [`../sdk/wallets.md`](../sdk/wallets.md).
