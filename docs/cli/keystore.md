# `keystore import` command

`nhb-cli keystore import` encrypts a raw private key into a local Ethereum V3
keystore file, using the same helpers the node uses for validator keys
(`crypto.SaveToKeystore` / `crypto.LoadFromKeystore` in `crypto/keystore.go`).
It is a scriptable way to produce a keystore file from a key you already hold.
The result is a standard V3 keystore (scrypt parameters
`keystore.StandardScryptN` / `StandardScryptP` from go-ethereum) and can be read
anywhere the repo calls `crypto.LoadFromKeystore`.

The command is implemented in `cmd/nhb-cli/keystore_cmd.go`.

## Usage

```bash
NHB_KEYSTORE_IMPORT_PRIVATE_KEY=<hex-private-key> \
NHB_KEYSTORE_IMPORT_PASSPHRASE=<passphrase> \
  nhb-cli keystore import --out <path>
```

- `NHB_KEYSTORE_IMPORT_PRIVATE_KEY` -- **environment variable, required.**
  The raw private key, hex-encoded. A leading `0x` or `0X` is stripped. The
  bytes must decode to a valid secp256k1 private key (32 bytes). This is
  deliberately an environment variable and not a CLI argument, so the key does
  not appear in shell history or in the process argument list. A one-shot
  `VAR=... command` prefix, as shown above, avoids `export`.
- `NHB_KEYSTORE_IMPORT_PASSPHRASE` -- **environment variable, required.**
  The passphrase used to encrypt the keystore file. Whoever reads the file back
  later needs this same passphrase.
- `--out <path>` -- **flag, required.** Output path for the keystore JSON file.
  The parent directory is created if it does not exist (mode `0700`). The file
  is written with mode `0600`. An existing file at `--out` is removed and
  replaced (`crypto.SaveToKeystore`).

Both environment variables must be set and non-empty (whitespace-only counts as
empty).

## What it does

1. Reads and decodes the hex key from `NHB_KEYSTORE_IMPORT_PRIVATE_KEY` and
   derives the public address.
2. Calls `crypto.SaveToKeystore` with the passphrase from
   `NHB_KEYSTORE_IMPORT_PASSPHRASE` to write the keystore file to `--out`.
3. Immediately calls `crypto.LoadFromKeystore` on the file it just wrote and
   checks that the recovered address equals the address derived in step 1.
4. Prints the path, the public address, a "Verified" line, and a reminder to
   confirm the address matches the wallet you meant to import.

## Confirming you imported the right key

The output has this shape:

```
Keystore written to: <path>
Public address:      nhb1...
Verified: decrypting the written file recovers the same address.

CONFIRM the address above matches the wallet you intended to import
before pointing anything (e.g. <name of an off-chain service and its signer>)
at this file.
```

The last two lines are printed by `runKeystoreImport` (`cmd/nhb-cli/keystore_cmd.go`).
The parenthetical in the real output names an off-chain service that is not
part of this repository; it is shown here as a placeholder.

Compare the printed address with the wallet you meant to import. The command
cannot tell whether the key you supplied is the right one; it only confirms
that the file it wrote decrypts back to that same key. If the address is wrong,
delete the file, check `NHB_KEYSTORE_IMPORT_PRIVATE_KEY`, and run the command
again.

## Using a keystore file with other `nhb-cli` commands

Commands that take a `<key_file>` argument load it through `loadPrivateKey`
(`cmd/nhb-cli/main.go`). If the file's contents are valid JSON it is treated as
an encrypted keystore and the passphrase is read from the
`NHB_KEYSTORE_PASSPHRASE` environment variable (the command fails with an error
naming that variable if it is unset or empty). Any other file is read as a raw
private key: `nhb-cli generate-key` writes such a file (`wallet.key`, mode
`0600`).

## Exit status

The command exits non-zero and prints an error to stderr if any of these hold:

- `--out` is missing;
- either environment variable is unset or empty;
- the private key is not valid hex, or does not decode to a valid private key;
- the keystore write fails;
- the write-then-verify step fails (decrypt error or address mismatch). The
  error text tells you not to trust the file and to delete it.

It exits `0` only after the write-then-verify step has succeeded.
