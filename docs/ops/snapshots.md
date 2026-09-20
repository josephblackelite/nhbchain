# Snapshot Operations Guide

A snapshot is a copy of a running node's chain database (blocks, state trie and
indexes). A new node that starts from one and syncs the blocks after it reaches
the tip of the network. **This is the only supported way to add a node to the
network that started on 2026-09-09: block sync from genesis is not supported on
it.** The full procedure, what it verifies, and the measured limits are in
[Onboarding a validator from a snapshot](../validators/snapshot-onboarding.md).
This page is the operator's checklist for producing and publishing snapshots.

An earlier version of this page described `nhbchain snapshot export`,
`consensusd --statesync.snapshot-height` and a snapshot bucket. None of those
exist in this repository, and the `sync_snapshot_export` / `sync_snapshot_import`
RPC methods and the `core/sync` package are not used by this procedure (see
"Why block sync from genesis is not supported" in the onboarding page).

## Producing a snapshot

Run `scripts/make-snapshot.sh` on a host that runs a validator or a follower, **as
the user the node runs as** (the owner of its data directory). It only reads the
data directory and never stops, signals or locks the node, so there is no need to
pause anything first.

```bash
sudo -u nhb bash scripts/make-snapshot.sh \
  --data-dir /var/lib/nhbchain/nhb-data \
  --out-dir /var/lib/nhbchain/snapshots
```

Not as root. The node's user controls the data directory, and what the script copies
is published, so a root-run script would read whatever that user pointed a link at
and publish it. The script refuses to run as root against a directory root does not
own, and says how to run it. (If the node itself runs as root, the script may be run
as root, and then `--tool`, `--work-dir` and `--out-dir` have to be given and have to
be places only root can change.) Whoever uploads `--out-dir` as another user must
copy regular files only and never follow a link.

That command is for a host that `scripts/deployvalidator.sh` installed: the script
reads the node binary from `/opt/nhbchain/bin/nhb` and the commit it was built from
out of the checkout in `/opt/nhbchain`. **On a host that was not installed by
`scripts/deployvalidator.sh`** it can find neither, and it stops before copying
anything: a manifest that does not name the commit its node was built from is one
no new node can use (`deployvalidator.sh` needs the commit to know what to check
out). Pass the binary that is running and the full commit id it was built from:

```bash
sudo -u nhb bash scripts/make-snapshot.sh \
  --data-dir /path/to/nhb-data --out-dir /path/to/snapshots \
  --node-binary /path/to/nhbchain/bin/nhb \
  --binary-commit "$(git -C /path/to/nhbchain rev-parse HEAD)"
```

git refuses to read a checkout that another user owns, so run the script as the
checkout's owner (the service user, in the layout the installer makes), or pass
`--binary-commit`. `--allow-unknown-binary` publishes a manifest without the commit
anyway, for tests only; a new node then refuses it unless it passes
`--allow-binary-mismatch`.

The commit has to contain this procedure (`scripts/deployvalidator.sh` as
described in the onboarding page, and `cmd/nhb-snapshot`), because a new node
checks it out and runs the script from it. Snapshots made by a validator that still
runs an earlier build name a commit whose script syncs from genesis, which does not
work on this network: upgrade the validators that make snapshots first.

It copies the database consistently while the node runs, and only the files the
database refers to: `CURRENT`, the MANIFEST it names, the tables that MANIFEST lists
(by hard link or copy) and the journal it names, last, repeated until two passes
agree. A file that merely has the name of one of those (a page of text called
`999999.log`, a table nothing lists, an older MANIFEST) is never copied, and a name
of the database that is a link or a directory ends the run before anything is read.
It opens the copy read-only (`nhb-snapshot info --staged`, which refuses a copy that
holds anything its MANIFEST does not refer to, a journal that is not one, or a table
that does not read in full) and refuses to go on if it does not open, packs it
deterministically, unpacks and opens the archive again, and only then writes
`nhb-snapshot-<genesis prefix>-h<height>.tar.gz`, its manifest and `manifest.json`.
The manifest carries the chain id, genesis hash, height, tip hash, state root,
creation time, the binary the snapshot was taken with (sha256, commit, version),
and the archive's size and sha256.

Only the chain database files are ever read and packed. The node's p2p identity
and peer list, its vote and lock state, its keys and its logs are not, and the
consensus key is not in the data directory at all. The script prints which files
were left out.

Old snapshots are not deleted unless you ask: `--keep N` keeps the `N` newest
snapshots of each chain in `--out-dir` and deletes the archives and manifests of
older ones (never the one just made, nor the one `manifest.json` names, and nothing
but files named exactly as the script names them). Each snapshot is about as large
as the database (334 MB when the live chain had 214,000 blocks).

The tool refuses to publish, and consumers refuse to use, a snapshot larger than
64 GiB (archive or unpacked) or one that unpacks to more than 64 times its archive:
the live chain's is a few hundred megabytes. A new node's script also refuses, by
default, an archive or an unpacked database above 16 GiB (`--max-snapshot-gib`). If
the chain ever approaches those limits, tell operators to raise the flag, and raise
the tool's own bounds in `cmd/nhb-snapshot/manifest.go` in the next release.

## Cadence

Publish a new snapshot regularly and delete old ones (`make-snapshot.sh --keep N`
does the deleting): a new node starts from the newest, and a follower can only
close so large a gap in reasonable time (measured figures are in the onboarding
page). A daily snapshot is a sensible default; the
schedule and retention are yours to set. Take snapshots from a node you trust and
that is at the tip. New nodes are told to refuse a snapshot older than a limit
(`--max-snapshot-age`); it is judged on the date of the snapshot's newest block,
so a snapshot taken from a node that lagged is as stale as its tip, whatever
creation time the manifest states.

## Distribution

Where snapshots are hosted is your decision and is not part of this repository.
Whatever you choose:

- serve `manifest.json` and the archive from one directory URL over `https`,
  updating `manifest.json` last (the script already writes it last);
- restrict who can write to the location and monitor its access log;
- the manifest is not signed: a new node checks the archive against it, and the
  manifest against the chain id and genesis hash the script pins, so tell
  operators the location out of band.

## Restoring

Use `scripts/deployvalidator.sh` (see the onboarding page), or the manual steps
there (`nhb-snapshot verify`, `extract`, `check-config`, `wait-synced`). Do not
unpack an archive with `tar`: `nhb-snapshot extract` refuses paths outside the
target, links, devices and files that are not chain database files, and checks
that what it unpacked opens and matches the manifest.

## Validation

After every change to the scripts or the tool, run:

```bash
go test ./cmd/nhb-snapshot ./tests/scripts ./tests/config -count=1
NHB_SNAPSHOT_E2E=1 go test ./cmd/nhb-snapshot -run TestSnapshotOnboardingEndToEnd -v -timeout 30m
```

The second runs a local network end to end (about four minutes) and needs
`bash`; on Windows set `NHB_TEST_BASH` to a Git Bash.
