package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"nhbchain/config"
)

// config-digest prints the digest of the consensus settings a config file gives a
// node, and check-parity compares the digest files two validators wrote at start-up
// (PL-R5T-16, PL-R5T-30). Neither reads a key or writes anything.

func cmdConfigDigest(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("config-digest", stderr)
	configPath := fs.String("config", "", "node config file to digest (its validator keystore must already exist)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *configPath == "" {
		return usagef("--config is required")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	d, err := cfg.ConsensusDigest(os.Getenv)
	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, d.Text())
	if strings.TrimSpace(os.Getenv("NHB_MASTER_TREASURY")) != "" {
		fmt.Fprintln(stderr, "note: NHB_MASTER_TREASURY is set in THIS shell and is part of the digest above; the running service's environment is what counts, and its own start-up file shows that.")
	}
	return err
}

func cmdCheckParity(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("check-parity", stderr)
	fileA := fs.String("a", "", "digest file of the first validator (consensus_config_digest.txt from its data directory)")
	fileB := fs.String("b", "", "digest file of the second validator")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *fileA == "" || *fileB == "" {
		return usagef("--a and --b are required")
	}
	read := func(path string) (config.ConsensusDigest, error) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return config.ConsensusDigest{}, err
		}
		d, err := config.ParseConsensusDigest(string(raw))
		if err != nil {
			return config.ConsensusDigest{}, fmt.Errorf("%s: %w", path, err)
		}
		return d, nil
	}
	a, err := read(*fileA)
	if err != nil {
		return err
	}
	b, err := read(*fileB)
	if err != nil {
		return err
	}
	diff := config.CompareConsensusDigests(a, b)
	var state, timing []string
	liveness := map[string]bool{}
	for _, s := range append(append([]config.DigestSection{}, a.Sections...), b.Sections...) {
		if s.LivenessOnly {
			liveness[s.Name] = true
		}
	}
	for _, name := range diff {
		if liveness[name] {
			timing = append(timing, name)
		} else {
			state = append(state, name)
		}
	}
	if len(timing) > 0 {
		fmt.Fprintf(stdout, "note: these settings differ but only change when a validator moves on, not what a block holds: %s\n", strings.Join(timing, ", "))
	}
	if a.Root != b.Root || len(state) > 0 {
		return fmt.Errorf("the two validators do NOT hold the same consensus settings. They will accept different blocks or compute different state roots at the first block that reaches a setting that differs.\n  differing: %s\n  (hashes only: compare the two config.toml files and the services' environment for these sections)", strings.Join(state, ", "))
	}
	fmt.Fprintf(stdout, "ok: both validators hold the same consensus settings (root %s)\n", a.Root)
	return nil
}
