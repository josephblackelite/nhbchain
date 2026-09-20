package main

// The commands the operator pages tell an operator to type to sign a transaction
// with the validator's key work as written. They are read next to the script that
// prints the same commands when one of its own steps fails.

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// printedRecipes returns what scripts/deployvalidator.sh prints for an operator to
// type to run a signing command later: the line that makes a token, and the start of
// the line that runs the command with it. They are taken from the script itself (its
// helper block, run with the paths it installs to), so a page that copies them is
// compared with what the script says, not with what someone remembers of it.
func printedRecipes(t *testing.T) (tokenRecipe, runPrefix string) {
	t.Helper()
	script := readRepoFile(t, "scripts/deployvalidator.sh")
	begin := strings.Index(script, "# --- begin validator CLI helpers")
	end := strings.Index(script, "# --- end validator CLI helpers ---")
	if begin < 0 || end < begin {
		t.Fatal("scripts/deployvalidator.sh has no validator CLI helper block")
	}
	program := "set -euo pipefail\nINSTALL_ROOT=/opt/nhbchain\nCONFIG_DIR=/etc/nhbchain\nSERVICE_USER=nhb\nRPC_ADDR=127.0.0.1:8545\n" +
		script[begin:end] + "\nprintf '%s\\n' \"${TOKEN_RECIPE}\"\ncli_recipe MARK\n"
	out, err := exec.Command(e2eBash(t), "-c", program).Output()
	if err != nil {
		t.Fatalf("the script's recipes did not print: %v", err)
	}
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(string(out)), "\r", ""), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[1], " MARK") {
		t.Fatalf("unexpected recipes from the script:\n%s", out)
	}
	return strings.Join(strings.Fields(lines[0]), " "), strings.Join(strings.Fields(strings.TrimSuffix(lines[1], "MARK")), " ") + " "
}

// signingCommand is what tells that a line shows one of the two commands that sign a
// transaction with the validator's key.
var signingCommand = regexp.MustCompile(`nhb-cli (register-validator|set-reward-beneficiary) `)

// signingMention is one place a page shows such a command: in a code block (fenced,
// with the logical line a shell would run, the lines before it in the block, and where
// it is), or in a sentence (with the six lines before it).
type signingMention struct {
	line   int
	text   string
	fenced bool
	block  []string
	before string
}

func signingMentions(page string) []signingMention {
	lines := strings.Split(page, "\n")
	var out []signingMention
	var block []string
	fenced := false
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "```") {
			fenced, block = !fenced, nil
			continue
		}
		if !fenced {
			if signingCommand.MatchString(lines[i]) {
				start := i - 6
				if start < 0 {
					start = 0
				}
				out = append(out, signingMention{line: i + 1, text: trimmed, before: strings.Join(lines[start:i], "\n")})
			}
			continue
		}
		logical, first := trimmed, i
		for strings.HasSuffix(logical, "\\") && i+1 < len(lines) {
			i++
			logical = strings.TrimSuffix(logical, "\\") + " " + strings.TrimSpace(lines[i])
		}
		logical = strings.Join(strings.Fields(logical), " ")
		if signingCommand.MatchString(logical) {
			out = append(out, signingMention{line: first + 1, text: logical, fenced: true, block: append([]string(nil), block...)})
		}
		block = append(block, logical)
	}
	return out
}

// The node's RPC accepts a transaction that is signed and sent by nhb-cli
// (register-validator, set-reward-beneficiary) only with a bearer token in
// NHB_RPC_TOKEN, and without one nhb-cli stops with "privileged RPC call requires
// NHB_RPC_TOKEN to be set". The key file is the service user's (0600, in a directory
// only that user can enter), so the command runs as that user. The script does both
// for its own steps and prints how to do it for a later one; every page that shows
// one of the commands shows it that way: a line that makes the token, exactly as the
// script prints it, before the command in the same block, and the command in the form
// the script prints. A sentence that only names a command says where the token comes
// from.
func TestThePagesGiveATokenForTheSigningCommands(t *testing.T) {
	tokenRecipe, runPrefix := printedRecipes(t)
	for page, atLeast := range map[string]int{
		"README.md":                              3,
		"docs/validators/onboarding.md":          3,
		"docs/validators/snapshot-onboarding.md": 2,
	} {
		text := readRepoFile(t, page)
		commands := 0
		for _, m := range signingMentions(text) {
			if !m.fenced {
				if !strings.Contains(m.before, "rpc-token") && !strings.Contains(m.before, "NHB_RPC_TOKEN") {
					t.Errorf("%s:%d names a signing command with no word about the token it needs in the six lines before it: %s", page, m.line, m.text)
				}
				continue
			}
			commands++
			if !strings.HasPrefix(m.text, runPrefix) {
				t.Errorf("%s:%d: the command is not the form the script prints (%s...): %s", page, m.line, runPrefix, m.text)
			}
			found := false
			for _, before := range m.block {
				found = found || before == tokenRecipe
			}
			if !found {
				t.Errorf("%s:%d: no line before the command, in its code block, makes the token as the script prints it (%s): %s", page, m.line, tokenRecipe, m.text)
			}
		}
		if commands < atLeast {
			t.Errorf("%s shows %d signing commands, expected at least %d: the test no longer finds them", page, commands, atLeast)
		}
		// node.env is in a directory the service user owns: a root shell that runs it
		// runs what that user put in it.
		for _, bad := range []string{". /etc/nhbchain/node.env", "sudo sh -c '. "} {
			if strings.Contains(text, bad) {
				t.Errorf("%s has root run node.env as a script (%q); the secret is read as data", page, bad)
			}
		}
	}
	guide := readRepoFile(t, "docs/validators/onboarding.md")
	if strings.Contains(guide, "exactly the retry command") {
		t.Errorf("docs/validators/onboarding.md says the script prints a bare command as its retry command; it prints the command that makes a token and the command that uses it")
	}
	if got := strings.Join(strings.Fields(docSection(t, "docs/validators/onboarding.md", "### Consensus reward beneficiary")), " "); !strings.Contains(got, "prints the command that makes a token") {
		t.Errorf("the beneficiary section does not say what the script prints when its own attempt fails")
	}
}

// "Doing it by hand" is what the script does, including who is trusted with what:
// root builds in a directory only root can write and runs the tools it needs from
// there, the tools of /opt/nhbchain (the service user's) are run by that user, and
// the unit comes from the checkout.
func TestDoingItByHandKeepsRootOffTheServiceUsersTree(t *testing.T) {
	text := docSection(t, "docs/validators/snapshot-onboarding.md", "## Doing it by hand")
	for _, gone := range []string{".gocache", ".gopath", ".gotmp", "GOCACHE=$PWD", "cd /opt/nhbchain", "/opt/nhbchain/deploy/systemd"} {
		if strings.Contains(text, gone) {
			t.Errorf("Doing it by hand still has %q: root would build, or install the unit, from the service user's tree", gone)
		}
	}
	for _, want := range []string{
		"GOCACHE=$B/go-cache", "GOTMPDIR=$B/go-tmp", "-o $B/bin/$p", "install -d -m 0700 -o root -g root $B/go-cache",
		"$B/bin/nhb-cli generate-key", "sudo $B/bin/nhb-snapshot check-config", "$B/bin/nhb-snapshot wait-synced",
		"sudo install -m 0644 $SRC/deploy/systemd/nhb.service /etc/systemd/system/nhb.service",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("Doing it by hand does not have %q", want)
		}
	}
	// Whatever runs a tool of /opt/nhbchain/bin does so as the service user.
	var logical []string
	pending := ""
	for _, line := range strings.Split(text, "\n") {
		if strings.HasSuffix(line, "\\") {
			pending += strings.TrimSuffix(line, "\\") + " "
			continue
		}
		logical = append(logical, pending+line)
		pending = ""
	}
	for _, line := range logical {
		if strings.HasPrefix(strings.TrimSpace(line), "#") || !strings.Contains(line, "/opt/nhbchain/bin/nhb") {
			continue
		}
		if !asServiceUser.MatchString(line) {
			t.Errorf("Doing it by hand runs a tool of /opt/nhbchain/bin, which the service user owns, as root or as you: %s", strings.TrimSpace(line))
		}
	}
}

// asServiceUser is a sudo command that runs as the service user, whatever options
// come between sudo and -u.
var asServiceUser = regexp.MustCompile(`sudo.* -u nhb( |$)`)

// The guide says where root builds, and that the script runs its own tools from
// there.
func TestTheGuideSaysWhereRootBuilds(t *testing.T) {
	guide := strings.Join(strings.Fields(readRepoFile(t, "docs/validators/onboarding.md")), " ")
	for _, want := range []string{
		"in `/var/cache/nhbchain-build`: a directory only root can write",
		"`GOCACHE`/`GOPATH`/`GOTMPDIR` under `/var/cache/nhbchain-build`",
		"never the copies in `/opt/nhbchain`",
		"installs `deploy/systemd/nhb.service` from your checkout",
	} {
		if !strings.Contains(guide, want) {
			t.Errorf("docs/validators/onboarding.md does not say %q", want)
		}
	}
	if strings.Contains(guide, "`GOCACHE`/`GOPATH`/`GOTMPDIR` under `/opt/nhbchain`") {
		t.Errorf("docs/validators/onboarding.md still puts the build caches under /opt/nhbchain")
	}
}
