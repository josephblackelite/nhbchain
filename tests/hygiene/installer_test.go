package hygiene

import (
	"bufio"
	"strings"
	"testing"
)

// serviceDirectives returns the Key=Value lines of the [Service] section of a
// systemd unit.
func serviceDirectives(t *testing.T, rel string) map[string]string {
	t.Helper()
	out := map[string]string{}
	inService := false
	sc := bufio.NewScanner(strings.NewReader(readRepoFile(t, rel)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inService = line == "[Service]"
			continue
		}
		if !inService {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("%s: not a Key=Value line: %q", rel, line)
		}
		out[key] = value
	}
	return out
}

// The validator unit runs the node as its own user and must also be sandboxed
// (PL-JN-11): it used to set none of systemd's protections.
func TestValidatorUnitIsSandboxed(t *testing.T) {
	d := serviceDirectives(t, "deploy/systemd/nhb.service")
	want := map[string]string{
		"NoNewPrivileges":         "yes",
		"PrivateTmp":              "yes",
		"PrivateDevices":          "yes",
		"ProtectSystem":           "full",
		"ProtectHome":             "yes",
		"ProtectKernelTunables":   "yes",
		"ProtectKernelModules":    "yes",
		"ProtectKernelLogs":       "yes",
		"ProtectControlGroups":    "yes",
		"ProtectClock":            "yes",
		"RestrictSUIDSGID":        "yes",
		"RestrictRealtime":        "yes",
		"RestrictNamespaces":      "yes",
		"LockPersonality":         "yes",
		"RestrictAddressFamilies": "AF_INET AF_INET6 AF_UNIX",
		"CapabilityBoundingSet":   "",
		"AmbientCapabilities":     "",
		"SystemCallArchitectures": "native",
	}
	for key, value := range want {
		got, ok := d[key]
		if !ok {
			t.Errorf("deploy/systemd/nhb.service does not set %s", key)
			continue
		}
		if got != value {
			t.Errorf("deploy/systemd/nhb.service sets %s=%q, want %q", key, got, value)
		}
	}
}

// The sandbox must not have changed who the node runs as or what it starts.
func TestValidatorUnitStillRunsAsTheServiceUser(t *testing.T) {
	d := serviceDirectives(t, "deploy/systemd/nhb.service")
	for key, value := range map[string]string{
		"User":            "nhb",
		"Group":           "nhb",
		"EnvironmentFile": "/etc/nhbchain/node.env",
		"ExecStart":       "/opt/nhbchain/bin/nhb --config /etc/nhbchain/config.toml",
		"Restart":         "on-failure",
	} {
		if d[key] != value {
			t.Errorf("deploy/systemd/nhb.service: %s = %q, want %q", key, d[key], value)
		}
	}
	// The node listens on ports above 1024, so it needs no capability, and it
	// must not be given one back by an ambient or bounding set naming one.
	for _, key := range []string{"CapabilityBoundingSet", "AmbientCapabilities"} {
		if strings.TrimSpace(d[key]) != "" {
			t.Errorf("%s names capabilities (%q): the node needs none", key, d[key])
		}
	}
}

// systemd reads node.env from the configuration directory as root, so the
// service user must not own that directory (PL-R5T-75): an owner can unlink a file
// in it and leave a symbolic link that root's systemd would follow.
func TestInstallerDoesNotGiveTheServiceUserTheConfigDirectory(t *testing.T) {
	script := readRepoFile(t, "scripts/deployvalidator.sh")
	for _, banned := range []string{
		`chown "${SERVICE_USER}:${SERVICE_USER}" "${CONFIG_DIR}"`,
		`-o "${SERVICE_USER}" -g "${SERVICE_USER}" "${CONFIG_DIR}"`,
		`"${SERVICE_USER}" -g "${SERVICE_USER}" "${tmp}" "${CONFIG_DIR}/config.toml"`,
	} {
		if strings.Contains(script, banned) {
			t.Errorf("the installer still gives the service user ownership of the configuration directory or its config file: %s", banned)
		}
	}
	for _, required := range []string{
		`chown "root:${SERVICE_USER}" "${CONFIG_DIR}"`,
		`chmod 750 "${CONFIG_DIR}"`,
		`install -d -m 0750 -o root -g "${SERVICE_USER}" "${CONFIG_DIR}"`,
		`install -m 0640 -o root -g "${SERVICE_USER}" "${tmp}" "${CONFIG_DIR}/config.toml"`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("the installer does not contain %q", required)
		}
	}
}

// The address printed as the place to send the stake comes from the tool root
// built, not from the copy in the service user's own tree.
func TestInstallerDerivesTheValidatorAddressWithTheRootBuiltTool(t *testing.T) {
	script := readRepoFile(t, "scripts/deployvalidator.sh")
	if strings.Contains(script, `as_service "${INSTALL_ROOT}/bin/nhb-cli" address`) {
		t.Errorf("the validator address is derived with a binary the service user owns")
	}
	if !strings.Contains(script, `as_service "${TOOL_DIR}/nhb-cli" address`) {
		t.Errorf("the validator address must be derived with the root-built tool")
	}
}

// The installer does not touch the host firewall; it prints what to open
// (PL-JN-11): TCP 6001 for peers and the SSH port, nothing else.
func TestInstallerPrintsTheFirewallRule(t *testing.T) {
	script := readRepoFile(t, "scripts/deployvalidator.sh")
	for _, required := range []string{
		"Firewall: this script does not change your firewall",
		"TCP 6001",
		"sudo ufw allow 6001/tcp",
		"loopback",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("the installer's closing text does not contain %q", required)
		}
	}
	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "echo ") || strings.HasPrefix(trimmed, "#") {
			continue
		}
		for _, tool := range []string{"ufw ", "iptables", "ip6tables", "nft ", "firewall-cmd"} {
			if strings.Contains(trimmed, tool) {
				t.Errorf("the installer must print the firewall rule, not apply one: %q", trimmed)
			}
		}
	}
}
