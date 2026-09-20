// Command nhb-snapshot packs, verifies and unpacks snapshots of a node's chain
// database, and reports how far a node is from the network tip.
//
// A snapshot is a copy of the chain database (blocks, state trie and indexes)
// of a running node, taken by scripts/make-snapshot.sh. It contains no key and
// nothing that identifies the node it came from. A new node that starts from
// one and then syncs the blocks after it as a non-voting follower reaches the
// tip of a chain whose history cannot be replayed from genesis by a current
// build (see docs/validators/snapshot-onboarding.md).
//
// The command only ever opens a database read-only, and only a copy: a
// database a running node holds open cannot be read (its lock is taken).
//
//	nhb-snapshot info        --data-dir DIR
//	nhb-snapshot pack        --data-dir DIR --out-dir DIR
//	nhb-snapshot verify      --manifest FILE --archive FILE --chain-id N --genesis-hash HEX
//	nhb-snapshot extract     --manifest FILE --archive FILE --target DIR --chain-id N --genesis-hash HEX
//	nhb-snapshot manifest    show --manifest FILE [--field NAME]
//	nhb-snapshot wait-synced --rpc URL [--tip-rpc URL]
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"
)

const (
	envChainID     = "NHB_SNAPSHOT_CHAIN_ID"
	envGenesisHash = "NHB_SNAPSHOT_GENESIS_HASH"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage: nhb-snapshot <command> [flags]

Commands:
  info         open a chain database read-only and print its chain id, genesis
               hash, height, tip hash and state root (checking it as it goes)
  pack         pack a staged copy of a chain database into a deterministic
               archive and write its manifest
  verify       check an archive and its manifest without writing anything
  extract      verify, unpack into a new data directory, open the result and
               check it against the manifest
  manifest     show a manifest (or one field of it)
  check-config check that a node config carries the consensus-relevant values
               of the network its genesis file describes
  wait-synced  wait until a node's RPC reports a height near the network tip
  version      print the tool version

Run "nhb-snapshot <command> -h" for the flags of a command.
`)
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "info":
		err = cmdInfo(rest, stdout, stderr)
	case "pack":
		err = cmdPack(rest, stdout, stderr)
	case "verify":
		err = cmdVerify(rest, stdout, stderr)
	case "extract":
		err = cmdExtract(rest, stdout, stderr)
	case "manifest":
		err = cmdManifest(rest, stdout, stderr)
	case "check-config":
		err = cmdCheckConfig(rest, stdout, stderr)
	case "wait-synced":
		err = cmdWaitSynced(rest, stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, "nhb-snapshot "+toolVersion)
		return 0
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "nhb-snapshot: unknown command %q\n\n", cmd)
		usage(stderr)
		return 2
	}
	if err == nil {
		return 0
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	var usageErr *usageError
	if errors.As(err, &usageErr) {
		fmt.Fprintf(stderr, "nhb-snapshot %s: %v\n", cmd, err)
		return 2
	}
	fmt.Fprintf(stderr, "nhb-snapshot %s: %v\n", cmd, err)
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return 1
}

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{fmt.Sprintf(format, args...)}
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("nhb-snapshot "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return flag.ErrHelp
		}
		return &usageError{err.Error()}
	}
	if fs.NArg() > 0 {
		return usagef("unexpected argument %q", fs.Arg(0))
	}
	return nil
}

func printIdentity(w io.Writer, id *chainIdentity, format string) error {
	switch format {
	case "json":
		out := map[string]any{
			"chainId":      strconv.FormatUint(id.ChainID, 10),
			"genesisHash":  hex0x(id.GenesisHash),
			"height":       id.Height,
			"tipHash":      hex0x(id.TipHash),
			"stateRoot":    hex0x(id.StateRoot),
			"tipTimestamp": id.TipTimestamp,
			"validators":   id.Validators,
			"headersRead":  id.HeadersRead,
			"stateNodes":   id.StateNodes,
		}
		if out["validators"] == nil {
			out["validators"] = []string{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	case "text":
		fmt.Fprintf(w, "chain id:      %d\n", id.ChainID)
		fmt.Fprintf(w, "genesis hash:  %s\n", hex0x(id.GenesisHash))
		fmt.Fprintf(w, "height:        %d\n", id.Height)
		fmt.Fprintf(w, "tip hash:      %s\n", hex0x(id.TipHash))
		fmt.Fprintf(w, "state root:    %s\n", hex0x(id.StateRoot))
		fmt.Fprintf(w, "tip time:      %s\n", time.Unix(id.TipTimestamp, 0).UTC().Format(time.RFC3339))
		fmt.Fprintf(w, "validators:    %s\n", strings.Join(id.Validators, " "))
		fmt.Fprintf(w, "checked:       %d headers linked and hashed, %d state trie nodes re-hashed\n", id.HeadersRead, id.StateNodes)
		return nil
	}
	return usagef("unknown format %q (use text or json)", format)
}

func cmdInfo(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("info", stderr)
	dataDir := fs.String("data-dir", "", "chain database directory (a copy, or the data directory of a stopped node)")
	format := fs.String("format", "text", "output format: text or json")
	window := fs.Uint64("header-window", defaultHeaderWindow, "how many of the newest blocks to decode, hash and link (0 = every block back to genesis)")
	noState := fs.Bool("no-state-check", false, "skip re-hashing every node of the tip's state trie")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *dataDir == "" {
		return usagef("--data-dir is required")
	}
	id, err := openAndReadIdentity(*dataDir, checkOptions{HeaderWindow: *window, SkipState: *noState})
	if err != nil {
		return err
	}
	return printIdentity(stdout, id, *format)
}

func cmdPack(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("pack", stderr)
	dataDir := fs.String("data-dir", "", "staged copy of the chain database")
	outDir := fs.String("out-dir", "", "directory the archive and its manifest are written to")
	version := fs.String("binary-version", "unknown", "version of the node binary the database was taken with")
	commit := fs.String("binary-commit", "unknown", "source commit of the node binary")
	binSum := fs.String("binary-sha256", "", "sha256 of the node binary (optional)")
	createdAt := fs.String("created-at", "", "creation time, RFC 3339 (default: now)")
	latest := fs.Bool("latest", false, "also write manifest.json, the fixed name consumers fetch")
	window := fs.Uint64("header-window", defaultHeaderWindow, "how many of the newest blocks to decode, hash and link (0 = all)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *dataDir == "" || *outDir == "" {
		return usagef("--data-dir and --out-dir are required")
	}
	created := time.Now().UTC().Truncate(time.Second)
	if *createdAt != "" {
		t, err := time.Parse(time.RFC3339, *createdAt)
		if err != nil {
			return usagef("--created-at: %v", err)
		}
		created = t
	}
	m, err := packSnapshot(packOptions{
		DataDir:   *dataDir,
		OutDir:    *outDir,
		Producer:  producerInfo{BinaryVersion: *version, BinaryCommit: *commit, BinarySha256: strings.ToLower(*binSum)},
		CreatedAt: created,
		Check:     checkOptions{HeaderWindow: *window},
		Latest:    *latest,
	})
	if err != nil {
		return err
	}
	encoded, err := m.marshal()
	if err != nil {
		return err
	}
	_, err = stdout.Write(encoded)
	return err
}

// expectFlags registers the pinned expectations shared by verify and extract.
type expectFlags struct {
	chainID     *string
	genesisHash *string
	minHeight   *uint64
	maxAge      *time.Duration
	tipHash     *string
	stateRoot   *string
	reject      *stringList
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func addExpectFlags(fs *flag.FlagSet) *expectFlags {
	e := &expectFlags{reject: &stringList{}}
	e.chainID = fs.String("chain-id", "", "pinned chain id (or "+envChainID+")")
	e.genesisHash = fs.String("genesis-hash", "", "pinned genesis hash, 32 bytes hex (or "+envGenesisHash+")")
	e.minHeight = fs.Uint64("min-height", 0, "refuse a snapshot below this height")
	e.maxAge = fs.Duration("max-age", 0, "refuse a snapshot created longer ago than this (0 = any age)")
	e.tipHash = fs.String("tip-hash", "", "pin the tip hash (32 bytes hex)")
	e.stateRoot = fs.String("state-root", "", "pin the state root (32 bytes hex)")
	fs.Var(e.reject, "reject-validator", "refuse the snapshot if this address is a validator in its state (repeatable)")
	return e
}

func (e *expectFlags) resolve() (expectations, error) {
	var out expectations
	chain := *e.chainID
	if chain == "" {
		chain = os.Getenv(envChainID)
	}
	if strings.TrimSpace(chain) == "" {
		return out, usagef("the chain id is not pinned: pass --chain-id or set %s", envChainID)
	}
	id, err := strconv.ParseUint(strings.TrimSpace(chain), 10, 64)
	if err != nil {
		return out, usagef("--chain-id %q: %v", chain, err)
	}
	out.ChainID = id
	genesis := *e.genesisHash
	if genesis == "" {
		genesis = os.Getenv(envGenesisHash)
	}
	if strings.TrimSpace(genesis) == "" {
		return out, usagef("the genesis hash is not pinned: pass --genesis-hash or set %s", envGenesisHash)
	}
	if out.GenesisHash, err = parseHex32(genesis); err != nil {
		return out, usagef("--genesis-hash: %v", err)
	}
	out.MinHeight = *e.minHeight
	out.MaxAge = *e.maxAge
	if *e.tipHash != "" {
		if out.TipHash, err = parseHex32(*e.tipHash); err != nil {
			return out, usagef("--tip-hash: %v", err)
		}
	}
	if *e.stateRoot != "" {
		if out.StateRoot, err = parseHex32(*e.stateRoot); err != nil {
			return out, usagef("--state-root: %v", err)
		}
	}
	for _, a := range *e.reject {
		out.RejectValidators = append(out.RejectValidators, strings.ToLower(strings.TrimSpace(a)))
	}
	return out, nil
}

func cmdVerify(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("verify", stderr)
	manifestPath := fs.String("manifest", "", "manifest file")
	archivePath := fs.String("archive", "", "archive file")
	exp := addExpectFlags(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *manifestPath == "" || *archivePath == "" {
		return usagef("--manifest and --archive are required")
	}
	want, err := exp.resolve()
	if err != nil {
		return err
	}
	m, err := readManifestFile(*manifestPath)
	if err != nil {
		return err
	}
	if err := checkManifest(m, want, time.Now()); err != nil {
		return err
	}
	if err := verifyArchiveFile(*archivePath, m); err != nil {
		return err
	}
	if err := verifyArchiveContent(*archivePath, m); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "ok: %s is chain %s at height %d (tip %s, state root %s); archive sha256 %s\n",
		m.Archive.Name, m.ChainID, m.Height, m.TipHash, m.StateRoot, m.Archive.Sha256)
	return nil
}

func cmdExtract(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("extract", stderr)
	manifestPath := fs.String("manifest", "", "manifest file")
	archivePath := fs.String("archive", "", "archive file")
	target := fs.String("target", "", "data directory to create (must not exist or be empty)")
	maxBytes := fs.Int64("max-bytes", defaultMaxBytes, "refuse a manifest announcing more uncompressed bytes than this")
	window := fs.Uint64("header-window", defaultHeaderWindow, "how many of the newest blocks to decode, hash and link (0 = all)")
	exp := addExpectFlags(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *manifestPath == "" || *archivePath == "" || *target == "" {
		return usagef("--manifest, --archive and --target are required")
	}
	want, err := exp.resolve()
	if err != nil {
		return err
	}
	m, id, err := extractSnapshot(extractOptions{
		ManifestPath: *manifestPath,
		ArchivePath:  *archivePath,
		Target:       *target,
		Expect:       want,
		Check:        checkOptions{HeaderWindow: *window},
		MaxBytes:     *maxBytes,
		Now:          time.Now(),
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "ok: extracted %d files (%d bytes) into %s\n", len(m.Archive.Files), m.Archive.UncompressedSize, *target)
	return printIdentity(stdout, id, "text")
}

func cmdManifest(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "show" {
		return usagef("usage: nhb-snapshot manifest show --manifest FILE [--field NAME]")
	}
	fs := newFlagSet("manifest show", stderr)
	manifestPath := fs.String("manifest", "", "manifest file")
	field := fs.String("field", "", "print only this field (chainId, genesisHash, height, tipHash, stateRoot, tipTimestamp, createdAt, producer.binaryVersion, producer.binaryCommit, producer.binarySha256, archive.name, archive.size, archive.sha256)")
	if err := parseFlags(fs, args[1:]); err != nil {
		return err
	}
	if *manifestPath == "" {
		return usagef("--manifest is required")
	}
	m, err := readManifestFile(*manifestPath)
	if err != nil {
		return err
	}
	if *field != "" {
		value, err := manifestField(m, *field)
		if err != nil {
			return usagef("%v", err)
		}
		fmt.Fprintln(stdout, value)
		return nil
	}
	encoded, err := m.marshal()
	if err != nil {
		return err
	}
	_, err = stdout.Write(encoded)
	return err
}

func cmdCheckConfig(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("check-config", stderr)
	configPath := fs.String("config", "", "node config file to check")
	genesisPath := fs.String("genesis", "", "genesis file the node is started from")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *configPath == "" || *genesisPath == "" {
		return usagef("--config and --genesis are required")
	}
	bad, err := checkLiveConfig(*configPath, *genesisPath)
	if err != nil {
		return err
	}
	if len(bad) > 0 {
		return fmt.Errorf("%s does not carry the consensus-relevant values of the network:\n  %s\nA node started with them would compute different state from the other validators.", *configPath, strings.Join(bad, "\n  "))
	}
	fmt.Fprintf(stdout, "ok: %s carries the consensus-relevant values of %s\n", *configPath, *genesisPath)
	return nil
}

func cmdWaitSynced(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("wait-synced", stderr)
	rpc := fs.String("rpc", "", "the node's own RPC URL")
	tipRPC := fs.String("tip-rpc", "", "RPC URL of a node you trust, measured against (optional)")
	chain := fs.String("chain-id", "", "expected chain id (or "+envChainID+")")
	genesis := fs.String("genesis-hash", "", "expected genesis hash (or "+envGenesisHash+")")
	lagBlocks := fs.Uint64("max-lag-blocks", 3, "with --tip-rpc: how many blocks behind still counts as synced")
	lagSecs := fs.Int64("max-lag-seconds", 60, "without --tip-rpc: how old the newest block may be, by this host's clock")
	interval := fs.Duration("interval", 5*time.Second, "time between polls")
	timeout := fs.Duration("timeout", 2*time.Hour, "give up after this long")
	stall := fs.Duration("stall-timeout", 15*time.Minute, "give up when the height has not advanced for this long")
	stable := fs.Int("stable", 2, "how many polls in a row must be within the lag")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *rpc == "" {
		return usagef("--rpc is required")
	}
	opts := waitOptions{
		RPC: *rpc, TipRPC: *tipRPC,
		MaxLagBlocks: *lagBlocks, MaxLagSeconds: *lagSecs,
		Interval: *interval, Timeout: *timeout, StallTimeout: *stall, Stable: *stable,
		Out: stderr,
	}
	if v := firstNonEmpty(*chain, os.Getenv(envChainID)); v != "" {
		id, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return usagef("--chain-id %q: %v", v, err)
		}
		opts.ExpectChainID = &id
	}
	if v := firstNonEmpty(*genesis, os.Getenv(envGenesisHash)); v != "" {
		h, err := parseHex32(v)
		if err != nil {
			return usagef("--genesis-hash: %v", err)
		}
		opts.ExpectGenesis = h
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res, err := waitSynced(ctx, opts)
	if err != nil {
		return err
	}
	out := map[string]any{
		"height":    res.Height,
		"tipHash":   hex0x(res.TipHash),
		"stateRoot": hex0x(res.Root),
		"seconds":   int64(res.Duration.Seconds()),
	}
	if res.TipRPC != 0 {
		out["referenceHeight"] = res.TipRPC
		out["lagBlocks"] = res.LagBlock
	} else {
		out["lagSeconds"] = res.LagSecs
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
