// Command swapd-partner-admin is operator tooling for managing the
// OTC/institutional partner credentials (api_key + HMAC secret) that live in
// a swapd config.yaml file under stable.partners.
//
// It does not talk to a running swapd process -- it edits the config file
// directly, on disk, so it can be used before swapd is even started (e.g.
// while a config is still being assembled) as well as against a live
// deployment's config ahead of a restart. Every write is atomic (temp file +
// rename) and every subcommand that mutates the file reminds the operator
// that swapd must be restarted to pick up the change, since partner config
// is not hot-reloaded.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"nhbchain/services/swapd/config"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}

	switch args[0] {
	case "-h", "--help":
		printUsage(stdout)
		return 0
	case "list":
		return runList(args[1:], stdout, stderr)
	case "add":
		return runAdd(args[1:], stdout, stderr)
	case "rotate":
		return runRotate(args[1:], stdout, stderr)
	case "remove":
		return runRemove(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "swapd-partner-admin: unknown subcommand %q\n\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `swapd-partner-admin manages OTC/institutional partner credentials stored in
a swapd config.yaml file (stable.partners), without requiring the swapd
process to be running.

Usage:
  swapd-partner-admin <subcommand> [flags]

Subcommands:
  list --config <path>
        List every partner's id, a redacted api_key (first 6 + last 4
        characters only), and its settlement_rail ("(default)" when unset).
        Never prints secrets.

  add --config <path> --id <partner-id> [--settlement-rail nowpayments|manual_treasury] [--daily-quota <float>]
        Generate a new api_key and secret (crypto/rand, 32 random bytes each,
        hex-encoded), add a partner entry, write the config back, and print
        the generated api_key and secret to stdout EXACTLY ONCE. Fails if
        --id already exists. --settlement-rail may be omitted (meaning "use
        stable.settlement.default_rail"), or must be exactly "nowpayments"
        or "manual_treasury". --daily-quota defaults to 0 (no quota).

  rotate --config <path> --id <partner-id>
        Regenerate ONLY the secret for an existing partner; the api_key is
        left unchanged. Prints the new secret to stdout EXACTLY ONCE. Fails
        if --id does not exist.

  remove --config <path> --id <partner-id>
        Remove a partner entry. Fails if --id does not exist.

Every subcommand that writes the file (add/rotate/remove) does so atomically
(temp file in the same directory, fsync, rename) and preserves the original
file's permission mode. Afterwards it reminds you that swapd does not
hot-reload partner config -- restart the running process (e.g.
"systemctl restart swapd") for the change to take effect.

Flags:
  -h, --help
        Show this help message.
`)
}

// loadConfig reads and parses the config file at path using yaml.Unmarshal
// directly into a config.Config value -- deliberately NOT config.Load(),
// which enforces full validation (TLS certs must exist, admin tokens must be
// set, etc.) that would make this tool unusable against a config file that's
// still mid-setup. It also returns the file's current permission mode so
// callers can preserve it when rewriting the file.
func loadConfig(path string) (config.Config, os.FileMode, error) {
	var cfg config.Config
	info, err := os.Stat(path)
	if err != nil {
		return cfg, 0, fmt.Errorf("stat config %q: %w", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, 0, fmt.Errorf("read config %q: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, 0, fmt.Errorf("parse config %q: %w", path, err)
	}
	return cfg, info.Mode().Perm(), nil
}

// writeConfigAtomic re-marshals the full config.Config and writes it back to
// path atomically: write to a new temp file in the same directory, fsync,
// chmod to match the original file's mode, then rename over the original.
// This never leaves a half-written config file on disk, and round-tripping
// the complete struct (rather than a partial one) preserves every other
// section of the file exactly.
func writeConfigAtomic(path string, cfg config.Config, mode os.FileMode) error {
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".swapd-partner-admin-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if mode != 0 {
		if err := os.Chmod(tmpName, mode); err != nil {
			return fmt.Errorf("chmod temp file: %w", err)
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	cleanup = false
	return nil
}

// generateHexSecret returns numBytes of crypto/rand randomness, hex-encoded.
// It deliberately uses crypto/rand (not math/rand) since these values are
// security-sensitive API keys and HMAC secrets.
func generateHexSecret(numBytes int) (string, error) {
	buf := make([]byte, numBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// redactAPIKey shows only the first 6 and last 4 characters of key, e.g.
// "ab12cd...ef34". Never used on secrets -- only on api keys, and only for
// display purposes in `list`.
func redactAPIKey(key string) string {
	const prefixLen, suffixLen = 6, 4
	if len(key) <= prefixLen+suffixLen {
		return strings.Repeat("*", len(key))
	}
	return key[:prefixLen] + "..." + key[len(key)-suffixLen:]
}

func validateSettlementRail(rail string) error {
	switch rail {
	case "", "nowpayments", "manual_treasury":
		return nil
	default:
		return fmt.Errorf("--settlement-rail must be %q, %q, or omitted", "nowpayments", "manual_treasury")
	}
}

func findPartnerIndex(partners []config.StablePartner, id string) int {
	for i, p := range partners {
		if strings.TrimSpace(p.ID) == id {
			return i
		}
	}
	return -1
}

func printOneTimeSecrets(w io.Writer, pairs [][2]string) {
	fmt.Fprintln(w, "================================================================")
	fmt.Fprintln(w, "WARNING: the value(s) below will NOT be shown again. Copy them now.")
	fmt.Fprintln(w, "================================================================")
	for _, kv := range pairs {
		fmt.Fprintf(w, "%s: %s\n", kv[0], kv[1])
	}
	fmt.Fprintln(w, "================================================================")
}

func printRestartReminder(w io.Writer) {
	fmt.Fprintln(w, "swapd does not hot-reload partner config; restart the running process (e.g. `systemctl restart swapd`) for this change to take effect.")
}

func runList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "path to swapd config.yaml (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	path := strings.TrimSpace(*configPath)
	if path == "" {
		fmt.Fprintln(stderr, "list: --config is required")
		return 2
	}

	cfg, _, err := loadConfig(path)
	if err != nil {
		fmt.Fprintf(stderr, "list: %v\n", err)
		return 1
	}

	if len(cfg.Stable.Partners) == 0 {
		fmt.Fprintln(stdout, "no partners configured")
		return 0
	}

	for _, p := range cfg.Stable.Partners {
		rail := strings.TrimSpace(p.SettlementRail)
		if rail == "" {
			rail = "(default)"
		}
		fmt.Fprintf(stdout, "id=%s api_key=%s settlement_rail=%s\n", p.ID, redactAPIKey(p.APIKey), rail)
	}
	return 0
}

func runAdd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "path to swapd config.yaml (required)")
	id := fs.String("id", "", "partner id (required)")
	settlementRail := fs.String("settlement-rail", "", "optional settlement rail override: nowpayments or manual_treasury")
	dailyQuota := fs.Float64("daily-quota", 0, "optional daily quota (default 0 = no quota)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	path := strings.TrimSpace(*configPath)
	partnerID := strings.TrimSpace(*id)
	rail := strings.TrimSpace(*settlementRail)

	if path == "" {
		fmt.Fprintln(stderr, "add: --config is required")
		return 2
	}
	if partnerID == "" {
		fmt.Fprintln(stderr, "add: --id is required")
		return 2
	}
	if err := validateSettlementRail(rail); err != nil {
		fmt.Fprintf(stderr, "add: %v\n", err)
		return 2
	}

	cfg, mode, err := loadConfig(path)
	if err != nil {
		fmt.Fprintf(stderr, "add: %v\n", err)
		return 1
	}

	if findPartnerIndex(cfg.Stable.Partners, partnerID) >= 0 {
		fmt.Fprintf(stderr, "add: partner id %q already exists\n", partnerID)
		return 1
	}

	apiKey, err := generateHexSecret(32)
	if err != nil {
		fmt.Fprintf(stderr, "add: %v\n", err)
		return 1
	}
	secret, err := generateHexSecret(32)
	if err != nil {
		fmt.Fprintf(stderr, "add: %v\n", err)
		return 1
	}

	cfg.Stable.Partners = append(cfg.Stable.Partners, config.StablePartner{
		ID:             partnerID,
		APIKey:         apiKey,
		Secret:         secret,
		Quota:          config.StablePartnerQuota{Daily: *dailyQuota},
		SettlementRail: rail,
	})

	if err := writeConfigAtomic(path, cfg, mode); err != nil {
		fmt.Fprintf(stderr, "add: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "partner %q added.\n", partnerID)
	printOneTimeSecrets(stdout, [][2]string{
		{"api_key", apiKey},
		{"secret", secret},
	})
	printRestartReminder(stdout)
	return 0
}

func runRotate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rotate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "path to swapd config.yaml (required)")
	id := fs.String("id", "", "partner id (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	path := strings.TrimSpace(*configPath)
	partnerID := strings.TrimSpace(*id)

	if path == "" {
		fmt.Fprintln(stderr, "rotate: --config is required")
		return 2
	}
	if partnerID == "" {
		fmt.Fprintln(stderr, "rotate: --id is required")
		return 2
	}

	cfg, mode, err := loadConfig(path)
	if err != nil {
		fmt.Fprintf(stderr, "rotate: %v\n", err)
		return 1
	}

	idx := findPartnerIndex(cfg.Stable.Partners, partnerID)
	if idx < 0 {
		fmt.Fprintf(stderr, "rotate: partner id %q not found\n", partnerID)
		return 1
	}

	// Only the secret is regenerated. Partners identify themselves by
	// api_key, so rotating it would require them to also update which key
	// they present -- rotating just the secret is "rotate what proves you
	// hold the key" and does not require the partner to change how they
	// identify themselves.
	secret, err := generateHexSecret(32)
	if err != nil {
		fmt.Fprintf(stderr, "rotate: %v\n", err)
		return 1
	}
	cfg.Stable.Partners[idx].Secret = secret

	if err := writeConfigAtomic(path, cfg, mode); err != nil {
		fmt.Fprintf(stderr, "rotate: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "partner %q secret rotated (api_key unchanged).\n", partnerID)
	printOneTimeSecrets(stdout, [][2]string{
		{"secret", secret},
	})
	printRestartReminder(stdout)
	return 0
}

func runRemove(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "path to swapd config.yaml (required)")
	id := fs.String("id", "", "partner id (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	path := strings.TrimSpace(*configPath)
	partnerID := strings.TrimSpace(*id)

	if path == "" {
		fmt.Fprintln(stderr, "remove: --config is required")
		return 2
	}
	if partnerID == "" {
		fmt.Fprintln(stderr, "remove: --id is required")
		return 2
	}

	cfg, mode, err := loadConfig(path)
	if err != nil {
		fmt.Fprintf(stderr, "remove: %v\n", err)
		return 1
	}

	idx := findPartnerIndex(cfg.Stable.Partners, partnerID)
	if idx < 0 {
		fmt.Fprintf(stderr, "remove: partner id %q not found\n", partnerID)
		return 1
	}

	cfg.Stable.Partners = append(cfg.Stable.Partners[:idx], cfg.Stable.Partners[idx+1:]...)

	if err := writeConfigAtomic(path, cfg, mode); err != nil {
		fmt.Fprintf(stderr, "remove: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "partner %q removed.\n", partnerID)
	printRestartReminder(stdout)
	return 0
}
