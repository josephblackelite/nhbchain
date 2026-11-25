package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// consensusDigestVersion is the layout of the digest. It is part of the comparison:
// two nodes that disagree on it have not computed the same thing.
const consensusDigestVersion = 1

// treasuryOverrideEnv is the environment variable that silently replaces the
// genesis admin wallet on the node that has it set (PL-DC-04).
const treasuryOverrideEnv = "NHB_MASTER_TREASURY"

// DigestSection is the hash of one group of settings. Only the hash is kept, so a
// log line or a peer learns whether two nodes agree, never what a setting is.
type DigestSection struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	// LivenessOnly marks a group whose difference between two validators changes
	// when they move on, not what a block holds. It is shown and compared but is
	// not part of the root.
	LivenessOnly bool `json:"livenessOnly,omitempty"`
}

// ConsensusDigest is a fingerprint of the node-local inputs that decide what a
// block leaves behind (PL-R5T-16, PL-DC-04, PL-R5T-30). Two validators that
// compute different roots will accept different blocks or compute different state
// roots the moment a block reaches the setting that differs, and nothing else tells
// them so before that block.
type ConsensusDigest struct {
	Version  int             `json:"version"`
	Root     string          `json:"root"`
	Sections []DigestSection `json:"sections"`
}

// digestFieldSections names, for every top-level setting two validators must
// agree on, the digest section it belongs to. Fields of the same section are
// hashed together. The global section is digested field by field (see
// ConsensusDigest) and is not listed.
//
// A setting added to Config must be placed here or in nodeLocalFields: a test
// fails until it is.
var digestFieldSections = map[string]string{
	"QuorumCertActivationHeight":                 "chain-rules",
	"RewardsByStakeFromEpoch":                    "chain-rules",
	"POSSweepSkipActivationHeight":               "chain-rules",
	"POSHoldUnstakeGuardActivationHeight":        "chain-rules",
	"GovCreditPreservationActivationHeight":      "chain-rules",
	"FeeRouteAliasActivationHeight":              "chain-rules",
	"LendingFixedTermIDActivationHeight":         "chain-rules",
	"NHBSupplyGenesisCorrectionActivationHeight": "chain-rules",
	"TransferMonthlyFreeTierActivationHeight":    "chain-rules",
	"TransferMonthlyFreeTierTxPerMonth":          "chain-rules",
	"LoyaltyBudgetFloorActivationHeight":         "chain-rules",
	"LoyaltyBudgetFloorZNHBWei":                  "chain-rules",
	"PotsoGovernanceWeightActivationHeight":      "chain-rules",
	"SlashEligibilityRefreshActivationHeight":    "chain-rules",
	"SlashPendingUnbondsActivationHeight":        "chain-rules",
	"EvidenceIntakeGuardActivationHeight":        "chain-rules",
	"StakeAPRFromStateActivationHeight":          "chain-rules",
	"ValidatorAdmissionGuardActivationHeight":    "chain-rules",
	"ValidatorAdmissionProbationSeconds":         "chain-rules",
	"PotsoTreasuryShortfallActivationHeight":     "chain-rules",

	// Network identity and replay protection (PL-R5T-91, PL-R5T-58, PL-R5T-29, PL-R5T-31).
	"ValidatorFlagNonceActivationHeight": "chain-rules",
	"MintRunningChainIDActivationHeight": "chain-rules",
	"MintPhaseEChainIDRetireHeight":      "chain-rules",
	"TxNetworkChainIDActivationHeight":   "chain-rules",
	"VoteChainBindingActivationHeight":   "chain-rules",

	// Lending engine (PL-DC-19, PL-R5T-13, 50, 51, 52, 53).
	"LendingFeeWithdrawActivationHeight":     "chain-rules",
	"LendingRiskActivationHeight":            "chain-rules",
	"LendingRefPriceMaxAgeSeconds":           "chain-rules",
	"LendingRefPriceMaxAheadSeconds":         "chain-rules",
	"LendingRefPriceGuardsActivationHeight":  "chain-rules",
	"LendingPoolIDMaxLength":                 "chain-rules",
	"LendingMaxPools":                        "chain-rules",
	"LendingPoolLimitsActivationHeight":      "chain-rules",
	"LendingAccountingFixesActivationHeight": "chain-rules",

	// Paymaster, loyalty, buyback and subscriptions (PL-R5T-22, 15, 33, 81, 82, PL-EXT-17).
	"PaymasterSponsorBindingActivationHeight":             "chain-rules",
	"LoyaltyPaymasterConsentActivationHeight":             "chain-rules",
	"LoyaltyCounterpartyCapActivationHeight":              "chain-rules",
	"LoyaltyCounterpartyCapZNHBWei":                       "chain-rules",
	"BuybackMinAskActivationHeight":                       "chain-rules",
	"BuybackMinAskZNHBWei":                                "chain-rules",
	"SubscriptionsPauseSettlementActivationHeight":        "chain-rules",
	"SubscriptionsChargeHistoryMigrationActivationHeight": "chain-rules",
	"BuybackUnpriceableEpochRefundActivationHeight":       "chain-rules",
	"SwapVoucherSignedBudgetActivationHeight":             "chain-rules",
	"SponsoredTransferFeeCreditActivationHeight":          "chain-rules",
	"UniformTransferFeeActivationHeight":                  "chain-rules",
	"TransferFeeGovernanceActivationHeight":               "chain-rules",
	"TransferFeeGovernanceDelayBlocks":                    "chain-rules",
	"BuybackPriceGuardActivationHeight":                   "chain-rules",
	"EpochSettlementFailsafeActivationHeight":             "chain-rules",
	"TransferFeeStrictAllowanceActivationHeight":          "chain-rules",
	"SaleReferenceActivationHeight":                       "chain-rules",
	"SaleReferenceMaxAgeSeconds":                          "chain-rules",
	"SaleReferenceMaxStepBps":                             "chain-rules",
	"DelegatorHoldWeightActivationHeight":                 "chain-rules",
	"PotsoAbuseGuardsActivationHeight":                    "chain-rules",
	"HeartbeatTimestampBoundActivationHeight":             "chain-rules",
	"LendingClosedDayClampActivationHeight":               "chain-rules",
	"EscrowEnvelopeChainBindingActivationHeight":          "chain-rules",

	"Potso":         "potso",
	"Governance":    "governance",
	"Swap":          "swap",
	"Lending":       "lending",
	"Bridge":        "bridge",
	"Amm":           "amm",
	"Subscriptions": "subscriptions",
	"Consensus":     "bft-timing",
	// Digested field by field, see ConsensusDigest.
	"Global": "global",
}

// nodeLocalFields are the top-level settings that may differ between validators:
// addresses, paths, keys, peer and rate limits, RPC knobs. Naming one here is a
// decision that a difference in it can never fork the chain.
var nodeLocalFields = map[string]struct{}{
	"UndecodedKeys": {}, "ListenAddress": {}, "RPCAddress": {}, "RPCTrustedProxies": {},
	"RPCTrustProxyHeaders": {}, "RPCAllowlistCIDRs": {}, "RPCWebSocketOrigins": {},
	"RPCWebSocketMaxConnections": {}, "RPCWebSocketMaxPerIP": {}, "RPCDisableExplorerLoop": {},
	"RPCQueryMaxConcurrent": {}, "RPCQueryMaxPerClient": {}, "RPCQueryQueueDepth": {},
	"RPCQueryQueueWaitMS": {}, "RPCQueryTimeoutSeconds": {}, "RPCProxyHeaders": {},
	"RPCJWT": {}, "RPCSwapAuth": {}, "RPCReadHeaderTimeout": {}, "RPCReadTimeout": {},
	"RPCWriteTimeout": {}, "RPCIdleTimeout": {}, "RPCMaxTxPerWindow": {}, "RPCMaxTxPerIP": {},
	"RPCMaxTxPerIdentity": {}, "RPCMaxTxPerChain": {}, "RPCMaxTxPerIdentityChain": {},
	"RPCRouteRateLimits": {}, "RPCRateLimitWindow": {}, "RPCCallerMetadataMaxTTL": {},
	// What a call costs the rate limiter, and whether the swap partner HMAC is skipped for
	// an operator: how this node serves its RPC, never what a block leaves behind.
	"RPCMethodCosts": {}, "RPCSwapOperatorNoHMAC": {},
	"RPCAllowInsecure": {}, "RPCAllowInsecureUnspecified": {}, "RPCTLSCertFile": {},
	"RPCTLSKeyFile": {}, "RPCTLSClientCAFile": {}, "DataDir": {}, "GenesisFile": {},
	"AllowAutogenesis": {}, "ValidatorKeystorePath": {}, "ValidatorKMSURI": {},
	"ValidatorKMSEnv": {}, "NetworkName": {}, "Bootnodes": {}, "PersistentPeers": {},
	"BootstrapPeers": {}, "MaxPeers": {}, "MaxInbound": {}, "MaxOutbound": {}, "MinPeers": {},
	"OutboundPeers": {}, "PeerBanSeconds": {}, "ReadTimeout": {}, "WriteTimeout": {},
	"MaxMsgBytes": {}, "MaxMsgsPerSecond": {}, "ClientVersion": {}, "P2P": {},
	// The mempool limit decides what this node keeps waiting, not which block is valid.
	"Mempool":         {},
	"NetworkSecurity": {},
}

// ConsensusDigest hashes the effective settings (after the loader's defaults) that
// two validators must hold alike, one section at a time, and combines the
// state-affecting sections into a root. getenv reads the environment (pass
// os.Getenv): NHB_MASTER_TREASURY is part of the digest because it replaces the
// genesis admin wallet on the node that has it.
func (c *Config) ConsensusDigest(getenv func(string) string) (ConsensusDigest, error) {
	if c == nil {
		return ConsensusDigest{}, fmt.Errorf("config: nil")
	}
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	val := reflect.ValueOf(*c)
	typ := val.Type()

	grouped := map[string]map[string]any{}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		section, ok := digestFieldSections[name]
		if !ok || section == "global" {
			continue
		}
		if grouped[section] == nil {
			grouped[section] = map[string]any{}
		}
		grouped[section][name] = val.Field(i).Interface()
	}

	global := reflect.ValueOf(c.Global)
	globalType := global.Type()
	for i := 0; i < globalType.NumField(); i++ {
		section := "global." + strings.ToLower(globalType.Field(i).Name)
		grouped[section] = map[string]any{globalType.Field(i).Name: global.Field(i).Interface()}
	}

	grouped["environment"] = map[string]any{treasuryOverrideEnv: strings.TrimSpace(getenv(treasuryOverrideEnv))}

	names := make([]string, 0, len(grouped))
	for name := range grouped {
		names = append(names, name)
	}
	sort.Strings(names)

	out := ConsensusDigest{Version: consensusDigestVersion}
	root := sha256.New()
	fmt.Fprintf(root, "nhb-consensus-config-digest/%d\n", consensusDigestVersion)
	for _, name := range names {
		blob, err := json.Marshal(grouped[name])
		if err != nil {
			return ConsensusDigest{}, fmt.Errorf("config: digest section %s: %w", name, err)
		}
		sum := sha256.Sum256(blob)
		section := DigestSection{Name: name, Digest: hex.EncodeToString(sum[:]), LivenessOnly: name == "bft-timing"}
		out.Sections = append(out.Sections, section)
		if !section.LivenessOnly {
			fmt.Fprintf(root, "%s=%s\n", section.Name, section.Digest)
		}
	}
	out.Root = hex.EncodeToString(root.Sum(nil))
	return out, nil
}

// Text is the digest as the lines an operator reads and compares, one per section.
func (d ConsensusDigest) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "version %d\nroot %s\n", d.Version, d.Root)
	for _, s := range d.Sections {
		tag := ""
		if s.LivenessOnly {
			tag = " (liveness only)"
		}
		fmt.Fprintf(&b, "%s %s%s\n", s.Name, s.Digest, tag)
	}
	return b.String()
}

// CompareConsensusDigests returns the names of the sections in which a and b
// differ, sorted. A section only one of them has counts as different, and so does
// a different digest version (reported as "version").
func CompareConsensusDigests(a, b ConsensusDigest) []string {
	var diff []string
	if a.Version != b.Version {
		diff = append(diff, "version")
	}
	left := map[string]string{}
	for _, s := range a.Sections {
		left[s.Name] = s.Digest
	}
	right := map[string]string{}
	for _, s := range b.Sections {
		right[s.Name] = s.Digest
	}
	seen := map[string]struct{}{}
	for name, d := range left {
		seen[name] = struct{}{}
		if right[name] != d {
			diff = append(diff, name)
		}
	}
	for name := range right {
		if _, ok := seen[name]; !ok {
			diff = append(diff, name)
		}
	}
	sort.Strings(diff)
	return diff
}

// ParseConsensusDigest reads the text form written by Text. It refuses anything
// else, and a digest whose root is not the one its sections give, so a file edited
// by hand to show agreement is not accepted as a digest.
func ParseConsensusDigest(text string) (ConsensusDigest, error) {
	var out ConsensusDigest
	haveVersion, haveRoot := false, false
	seen := map[string]struct{}{}
	isHex := func(s string) bool {
		if len(s) != 64 {
			return false
		}
		for i := 0; i < len(s); i++ {
			c := s[i]
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
		return true
	}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 0:
			continue
		case fields[0] == "version" && len(fields) == 2:
			if _, err := fmt.Sscanf(fields[1], "%d", &out.Version); err != nil {
				return ConsensusDigest{}, fmt.Errorf("config: digest version %q: %w", fields[1], err)
			}
			haveVersion = true
		case fields[0] == "root" && len(fields) == 2:
			if !isHex(fields[1]) {
				return ConsensusDigest{}, fmt.Errorf("config: digest root %q is not a hex sha256", fields[1])
			}
			out.Root = fields[1]
			haveRoot = true
		case len(fields) == 2 || (len(fields) == 4 && fields[2] == "(liveness" && fields[3] == "only)"):
			if !isHex(fields[1]) {
				return ConsensusDigest{}, fmt.Errorf("config: digest section %q has %q, not a hex sha256", fields[0], fields[1])
			}
			if _, dup := seen[fields[0]]; dup {
				return ConsensusDigest{}, fmt.Errorf("config: digest section %q appears twice", fields[0])
			}
			seen[fields[0]] = struct{}{}
			out.Sections = append(out.Sections, DigestSection{Name: fields[0], Digest: fields[1], LivenessOnly: len(fields) == 4})
		default:
			return ConsensusDigest{}, fmt.Errorf("config: digest line %q is not understood", line)
		}
	}
	if !haveVersion || !haveRoot {
		return ConsensusDigest{}, fmt.Errorf("config: digest needs a version line and a root line")
	}
	sort.Slice(out.Sections, func(i, j int) bool { return out.Sections[i].Name < out.Sections[j].Name })
	if got := digestRoot(out.Version, out.Sections); got != out.Root {
		return ConsensusDigest{}, fmt.Errorf("config: digest root %s is not the root its sections give (%s)", out.Root, got)
	}
	return out, nil
}

// digestRoot combines the state-affecting sections into the root.
func digestRoot(version int, sections []DigestSection) string {
	root := sha256.New()
	fmt.Fprintf(root, "nhb-consensus-config-digest/%d\n", version)
	for _, s := range sections {
		if !s.LivenessOnly {
			fmt.Fprintf(root, "%s=%s\n", s.Name, s.Digest)
		}
	}
	return hex.EncodeToString(root.Sum(nil))
}
