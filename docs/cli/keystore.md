# `keystore import` command

`nhb-cli keystore import` encrypts a raw private key into a local
Ethereum V3 keystore file -- the same encryption this repo already uses for
validator keys (see `crypto/keystore.go`'s `SaveToKeystore` /
`LoadFromKeystore`). No new encryption scheme is introduced; this command is
just a safe, scriptable way to produce a keystore file from a key you
already hold.

The primary use case today is producing the keystore file for
`services/swapd`'s local price-proof signer
(`price_proof.signer.type: local`, see `docs/swap/staging-deploy.md`), but
the resulting file is a standard keystore usable anywhere this repo already
accepts one (e.g. `--keystore` flags on other tools that call
`crypto.LoadFromKeystore`).

## Usage

```bash
NHB_KEYSTORE_IMPORT_PRIVATE_KEY=<hex-private-key> \
NHB_KEYSTORE_IMPORT_PASSPHRASE=<passphrase> \
  nhb-cli keystore import --out <path>
```

- `NHB_KEYSTORE_IMPORT_PRIVATE_KEY` -- **environment variable, required.**
  The raw private key to import, hex-encoded (with or without a `0x`
  prefix). This is deliberately an environment variable and NOT a CLI
  argument: an argument value is recorded in shell history files and is
  visible to any other process on the host via `ps aux` /
  `/proc/<pid>/cmdline` for as long as the command runs. Prefer a one-shot
  `VAR=... command` invocation (as shown above) over `export`, since most
  shells never write a one-shot prefix assignment to history either.
- `NHB_KEYSTORE_IMPORT_PASSPHRASE` -- **environment variable, required.**
  The passphrase used to encrypt the resulting keystore file. Whatever
  reads the file back later (e.g. swapd's local price-proof signer) must be
  configured with this exact same passphrase, via its own environment
  variable indirection -- never copy the passphrase into a config file.
- `--out <path>` -- **flag, required.** Output path for the encrypted
  keystore JSON file. The parent directory is created if it doesn't exist
  (mode `0700`); the file itself is written mode `0600`.

## What it does

1. Reads the raw hex key from `NHB_KEYSTORE_IMPORT_PRIVATE_KEY` and derives
   the key pair and its public address.
2. Reads the passphrase from `NHB_KEYSTORE_IMPORT_PASSPHRASE` and calls
   `crypto.SaveToKeystore` to write a standard Ethereum V3 (scrypt
   encrypted) keystore file to `--out`.
3. **Immediately reads the file back**: calls `crypto.LoadFromKeystore` on
   the freshly-written file with the same passphrase, and confirms the
   recovered address matches the address derived in step 1. This is a
   write-then-verify round trip -- a corrupted or unexpectedly-wrong write
   is caught right here, at import time, rather than surfacing later as an
   opaque decrypt failure when something like swapd tries to start with the
   file.
4. Prints the public address.

## Confirming you imported the right key

The command's final output looks like:

```
Keystore written to: /etc/nhbchain/secrets/swapd-price-signer.keystore.json
Public address:      nhb1...
Verified: decrypting the written file recovers the same address.

CONFIRM the address above matches the wallet you intended to import
before pointing anything (e.g. services/swapd's local price-proof
signer) at this file.
```

**You must manually compare the printed address against the wallet you
meant to import.** This command has no independent way to know whether the
correct private key was supplied -- it can only confirm that the file it
wrote decrypts back to whatever key it was given, not that the key was the
right one. If the printed address is wrong, delete the file, double-check
`NHB_KEYSTORE_IMPORT_PRIVATE_KEY`, and re-run the command.

## Exit status

The command exits non-zero (and prints an error to stderr) if: `--out` is
missing, either environment variable is unset or empty, the private key is
not valid hex, the keystore write fails, or the write-then-verify round
trip fails (decrypt error or address mismatch). It exits `0` only after the
round trip has independently confirmed the file on disk is trustworthy.
