package main

// The operator pages say what the code does, and the commands they tell an
// operator to type are ones that work. These tests read the pages next to the code
// they describe.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The headings of two sections the tests read. The pages spell them with an em
// dash and a rocket, written here as escapes so that this file is plain ASCII.
const (
	stepOneHeading = "## Step 1 \U00002014 Run the bootstrap script"
	readmeQuick    = "## \U0001F680 Quick Start for Node Operators (Step-by-Step)"
)

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

// docSection returns the text from the heading (a full line) to the next heading of
// the same or a higher level. A line that starts with # inside a code block is a
// comment, not a heading.
func docSection(t *testing.T, doc, heading string) string {
	t.Helper()
	level := len(heading) - len(strings.TrimLeft(heading, "#"))
	lines := strings.Split(readRepoFile(t, doc), "\n")
	start := -1
	for i, line := range lines {
		if line == heading {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s has no section %q", doc, heading)
	}
	end, fenced := len(lines), false
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "```") {
			fenced = !fenced
			continue
		}
		if !fenced {
			if n := len(lines[i]) - len(strings.TrimLeft(lines[i], "#")); n >= 1 && n <= level && strings.HasPrefix(lines[i][n:], " ") {
				end = i
				break
			}
		}
	}
	return strings.Join(lines[start:end], "\n") + "\n"
}

// The rule the pages state, that ZNHB delegated to a validator's address counts
// toward its eligibility, is the rule the code applies: validatorEligibilityBasis
// reads the account's Stake, which includes what others delegated to it, and does
// not take that out (stakeRewardBasis, the basis of what a stake earns, does).
func TestOnboardingPagesSayDelegatedStakeCountsTowardEligibility(t *testing.T) {
	code := readRepoFile(t, "core/state_transition.go")
	start := strings.Index(code, "func (sp *StateProcessor) validatorEligibilityBasis(")
	if start < 0 {
		t.Fatal("core/state_transition.go has no validatorEligibilityBasis")
	}
	body := code[start:]
	body = body[:strings.Index(body, "\n}\n")]
	if !strings.Contains(body, "new(big.Int).Set(account.Stake)") || strings.Contains(body, "StakeValidatorDelegatedInTotal") {
		t.Fatalf("validatorEligibilityBasis no longer returns the account's whole stake: the pages describe the eligibility rule and have to be read again:\n%s", body)
	}
	reward := code[strings.Index(code, "func (sp *StateProcessor) stakeRewardBasis("):]
	reward = reward[:strings.Index(reward, "\n}\n")]
	if !strings.Contains(reward, "StakeValidatorDelegatedInTotal") {
		t.Fatalf("stakeRewardBasis no longer leaves out delegated-in stake: the pages describe the yield rule and have to be read again")
	}

	guide := readRepoFile(t, "docs/validators/onboarding.md")
	for _, wrong := range []string{
		"does **not** count",
		"self-stake only",
		"never counts toward",
		"does not count toward this validator",
		"Only stake sitting on the\nvalidator's own key",
		"separate wallet's stake",
	} {
		if strings.Contains(guide, wrong) {
			t.Errorf("docs/validators/onboarding.md still says %q, which is the opposite of the code", wrong)
		}
	}
	for _, section := range []string{"### Staking (delegation or self-stake)"} {
		text := strings.Join(strings.Fields(docSection(t, "docs/validators/onboarding.md", section)), " ")
		for _, want := range []string{"including the ZNHB other wallets have delegated", "validatorEligibilityBasis", "no separate self-stake requirement", "stakeRewardBasis", "earns for you, the delegator"} {
			if !strings.Contains(text, want) {
				t.Errorf("the %q section does not say %q", section, want)
			}
		}
	}
	preflight := strings.Join(strings.Fields(docSection(t, "docs/validators/onboarding.md", "## Pre-flight checklist")), " ")
	for _, want := range []string{"delegated to it from any other wallet counts", "Either route is enough"} {
		if !strings.Contains(preflight, want) {
			t.Errorf("the pre-flight checklist does not say %q", want)
		}
	}
	// The script's own next steps say the same.
	script := readRepoFile(t, "scripts/deployvalidator.sh")
	if !strings.Contains(script, "Delegated stake and the validator's own stake add together") {
		t.Errorf("the script's next steps no longer say that delegated stake counts")
	}
	for _, path := range []string{"scripts/deployvalidator.sh", "README.md", "docs/validators/snapshot-onboarding.md"} {
		if text := readRepoFile(t, path); strings.Contains(text, "does not count toward") || strings.Contains(text, "does **not** count") {
			t.Errorf("%s says that delegated ZNHB does not count", path)
		}
	}
}

// On a clean machine nhb-snapshot does not exist until the script has built it, so
// the pages tell the operator to read the commit the manifest names with curl. The
// command they give is tested here: it prints the commit of a real manifest.
func TestThePagesReadTheManifestsCommitWithCurl(t *testing.T) {
	oneLiner := `curl -fsS https://SNAPSHOT-HOST.example/PATH/manifest.json | sed -n 's/.*"binaryCommit": *"\([0-9a-f]*\)".*/\1/p'`
	for _, path := range []string{"docs/validators/snapshot-onboarding.md", "docs/validators/onboarding.md", "README.md"} {
		text := readRepoFile(t, path)
		want := oneLiner
		if path == "README.md" {
			want = `sed -n 's/.*"binaryCommit": *"\([0-9a-f]*\)".*/\1/p'`
		}
		if !strings.Contains(text, want) {
			t.Errorf("%s does not give the command that reads the commit with curl", path)
		}
	}
	// Before the tool exists it cannot be told to read the commit.
	for doc, headings := range map[string][]string{
		"docs/validators/snapshot-onboarding.md": {"## Quick start"},
		"docs/validators/onboarding.md":          {stepOneHeading},
	} {
		for _, heading := range headings {
			if text := docSection(t, doc, heading); strings.Contains(text, "manifest show") {
				t.Errorf("the %q section of %s tells a clean machine to run nhb-snapshot manifest show", heading, doc)
			}
		}
	}
	need := docSection(t, "docs/validators/snapshot-onboarding.md", "## What you need")
	if strings.Contains(need, "nhb-snapshot manifest show") && !strings.Contains(need, "does not exist") {
		t.Errorf("What you need names nhb-snapshot manifest show without saying it does not exist yet")
	}

	// The command, run on a real manifest.
	bash := e2eBash(t)
	f := newFixture(t)
	encoded, err := f.m.marshal()
	if err != nil {
		t.Fatal(err)
	}
	sed := oneLiner[strings.Index(oneLiner, "sed"):]
	cmd := exec.Command(bash, "-c", sed)
	cmd.Stdin = strings.NewReader(string(encoded))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the command failed: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != f.m.Producer.BinaryCommit || got == "" {
		t.Fatalf("the command printed %q, the manifest names %q", got, f.m.Producer.BinaryCommit)
	}
	// A manifest that names no commit prints nothing.
	f.m.Producer.BinaryCommit = "unknown"
	encoded, _ = f.m.marshal()
	cmd = exec.Command(bash, "-c", sed)
	cmd.Stdin = strings.NewReader(string(encoded))
	if out, _ := cmd.Output(); strings.TrimSpace(string(out)) != "" {
		t.Fatalf("a manifest that names no commit printed %q", out)
	}
}

// Without --max-snapshot-age a snapshot of any age is accepted and a stale one fails
// only later, with a stall. The commands the pages give carry it.
func TestTheCommandsThePagesGiveCarryTheAgeLimit(t *testing.T) {
	for doc, headings := range map[string][]string{
		"docs/validators/snapshot-onboarding.md": {"## Quick start"},
		"docs/validators/onboarding.md":          {stepOneHeading},
		"README.md":                              {"### Join As A Validator In One Command", "### Step 6: Get paid (stake at least 10,000 ZNHB)"},
	} {
		for _, heading := range headings {
			text := docSection(t, doc, heading)
			at := strings.Index(text, "validator-only-bootstrap.sh")
			if at < 0 {
				t.Errorf("the %q section of %s has no command", heading, doc)
				continue
			}
			block := text[at:]
			if end := strings.Index(block, "```"); end >= 0 {
				block = block[:end]
			}
			if !strings.Contains(block, "--max-snapshot-age 72h") {
				t.Errorf("the command in the %q section of %s does not carry --max-snapshot-age 72h:\n%s", heading, doc, block)
			}
		}
	}
	// A recommendation in words is not a command: the flag the script names is real.
	if usage := readRepoFile(t, "scripts/deployvalidator.sh"); !strings.Contains(usage, "--min-release-commit <c>") {
		t.Errorf("the script's usage does not describe --min-release-commit")
	}
}

// README Step 5 is the from-genesis start, which cannot sync this network: it says
// so and points to the snapshot procedure, before and after the command.
func TestReadmeStepFiveWarnsThatItCannotJoinTheMainnet(t *testing.T) {
	step := strings.Join(strings.Fields(docSection(t, "README.md", "### Step 5: Run the Automated Node Bootstrap")), " ")
	for _, want := range []string{
		"do not use this step to join the current mainnet",
		"from-genesis start",
		"Block sync from genesis is not supported on this network",
		"docs/validators/snapshot-onboarding.md",
		"stays at height 0",
	} {
		if !strings.Contains(step, want) {
			t.Errorf("README Step 5 does not say %q", want)
		}
	}
	if warning, command := strings.Index(step, "do not use this step"), strings.Index(step, "bash scripts/run_nhbcoin_node.sh"); warning < 0 || command < 0 || warning > command {
		t.Errorf("README Step 5 gives the command before it warns")
	}
	if strings.Contains(step, "**That brings the NHBCoin node online as a peer/full node** once") {
		t.Errorf("README Step 5 still says, without a condition, that the script brings a node online as a peer")
	}
	top := strings.Join(strings.Fields(docSection(t, "README.md", readmeQuick)), " ")
	if !strings.Contains(top, "cannot sync the current mainnet") {
		t.Errorf("the note at the top of the Quick Start does not say that the manual walkthrough cannot sync the mainnet")
	}
}

// "Doing it by hand" is the whole of what the script does, in the order it does it:
// the service user and directories, the build, the key, the snapshot, the config,
// the node's environment, the systemd unit, the start, and the registration only
// after the node has been seen at the tip.
func TestDoingItByHandIsComplete(t *testing.T) {
	text := docSection(t, "docs/validators/snapshot-onboarding.md", "## Doing it by hand")
	var in []string
	for _, step := range []string{
		"useradd --system",
		"go build",
		"generate-key",
		"manifest show",
		" verify ",
		" extract ",
		"check-config",
		"node.env",
		"nhb.service /etc/systemd/system/nhb.service",
		"systemctl start nhb.service",
		"wait-synced",
		"rpc-token",
		"register-validator 0",
	} {
		at := strings.Index(text, step)
		if at < 0 {
			t.Errorf("Doing it by hand does not have the step %q", step)
			continue
		}
		in = append(in, step)
		if len(in) > 1 && at < strings.Index(text, in[len(in)-2]) {
			t.Errorf("Doing it by hand has %q before %q", step, in[len(in)-2])
		}
	}
	for _, want := range []string{"install_tree_and_build", "ensure_key", "write_env", "install_service", "install_config"} {
		if !strings.Contains(text, want) {
			t.Errorf("Doing it by hand does not say which function of the script %s stands for", want)
		}
		if script := readRepoFile(t, "scripts/deployvalidator.sh"); !strings.Contains(script, want+"() {") {
			t.Errorf("the page names %s, which the script does not have", want)
		}
	}
	// What it installs is what the script installs.
	script := readRepoFile(t, "scripts/deployvalidator.sh")
	for _, value := range []string{"--shell /usr/sbin/nologin", "NHB_VALIDATOR_RAW_KEY", "NHB_RPC_JWT_SECRET", "nhb-mainnet-validator", "-buildvcs=false"} {
		if strings.Contains(text, value) && !strings.Contains(script, value) {
			t.Errorf("Doing it by hand uses %q, which the script does not", value)
		}
	}
}
