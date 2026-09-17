package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	var (
		domain    = flag.String("domain", "", "Authority domain (e.g. seeds.mainnet.example.org)")
		host      = flag.String("host", "", "Seed host name or IP")
		port      = flag.Int("port", 46656, "Seed TCP port")
		nodeID    = flag.String("node-id", "", "Seed node ID (0x-prefixed)")
		lookup    = flag.String("lookup", "", "Optional override for the TXT lookup name")
		notBefore = flag.Int64("not-before", 0, "Optional activation timestamp (unix seconds)")
		notAfter  = flag.Int64("not-after", 0, "Optional expiry timestamp (unix seconds)")
		outFile   = flag.String("out", "authority.json", "Path to write authority metadata")
	)
	flag.Parse()

	if strings.TrimSpace(*domain) == "" {
		exitf("--domain is required")
	}
	if strings.TrimSpace(*host) == "" {
		exitf("--host is required")
	}
	if strings.TrimSpace(*nodeID) == "" {
		exitf("--node-id is required")
	}

	normalizedNode := normalizeNodeID(*nodeID)
	if normalizedNode == "" {
		exitf("invalid node ID: %s", *nodeID)
	}

	addr := fmt.Sprintf("%s:%d", strings.TrimSpace(*host), *port)
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		exitf("failed to generate ed25519 key: %v", err)
	}

	record := map[string]interface{}{
		"nodeId":  normalizedNode,
		"address": addr,
	}
	if *notBefore > 0 {
		record["notBefore"] = *notBefore
	}
	if *notAfter > 0 {
		record["notAfter"] = *notAfter
	}

	message := buildSigningMessage(normalizedNode, addr, *notBefore, *notAfter, *domain)
	signature := ed25519.Sign(priv, message)

	record["signature"] = base64.StdEncoding.EncodeToString(signature)
	signedPayload, err := json.Marshal(record)
	if err != nil {
		exitf("marshal signed record: %v", err)
	}

	txtValue := "nhbseed:v1:" + base64.StdEncoding.EncodeToString(signedPayload)

	lookupName := strings.TrimSpace(*lookup)
	if lookupName == "" {
		lookupName = "_nhbseed." + strings.TrimSpace(*domain)
	}

	out := map[string]interface{}{
		"generatedAt": time.Now().UTC().Format(time.RFC3339),
		"domain":      strings.TrimSpace(*domain),
		"lookup":      lookupName,
		"nodeId":      normalizedNode,
		"address":     addr,
		"publicKey":   base64.StdEncoding.EncodeToString(pub),
		"privateKey":  base64.StdEncoding.EncodeToString(priv),
		"txt":         txtValue,
	}
	if err := os.WriteFile(*outFile, mustJSON(out), 0o600); err != nil {
		exitf("write %s: %v", *outFile, err)
	}

	fmt.Printf("Authority file written to %s\n", *outFile)
	fmt.Println("TXT record:")
	fmt.Printf("%s\tIN\tTXT\t\"%s\"\n", lookupName, txtValue)
	fmt.Println()
	fmt.Println("Registry snippet:")
	fmt.Printf(`{
  "domain": "%s",
  "algorithm": "ed25519",
  "publicKey": "%s"
}
`, strings.TrimSpace(*domain), base64.StdEncoding.EncodeToString(pub))
}

func mustJSON(v interface{}) []byte {
	payload, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return payload
}

func exitf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func normalizeNodeID(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if !strings.HasPrefix(trimmed, "0x") && !strings.HasPrefix(trimmed, "0X") {
		trimmed = "0x" + trimmed
	}
	return strings.ToLower(trimmed)
}

func buildSigningMessage(nodeID, address string, notBefore, notAfter int64, domain string) []byte {
	builder := strings.Builder{}
	builder.Grow(len(nodeID) + len(address) + len(domain) + 40)
	builder.WriteString(nodeID)
	builder.WriteString("\n")
	builder.WriteString(address)
	builder.WriteString("\n")
	builder.WriteString(fmt.Sprintf("%d\n%d\n", notBefore, notAfter))
	builder.WriteString(strings.ToLower(strings.TrimSpace(domain)))
	return []byte(builder.String())
}
