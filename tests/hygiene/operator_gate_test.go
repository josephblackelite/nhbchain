package hygiene

import (
	"regexp"
	"strings"
	"testing"
)

// net_info, p2p_info, sync_status and the other net_ and sync_ methods answer only
// to a bearer token with the operator role (rpc/operator_methods.go). A script or
// a document that calls one of them with no credential gets a 401 and reports the
// node as down (PL-R5T-17).
var operatorOnlyCall = regexp.MustCompile(`"method"\s*:\s*"(net_[a-z_]+|sync_[a-z_]+|p2p_info)"`)

func TestShellScriptsSendATokenWithOperatorOnlyCalls(t *testing.T) {
	for _, rel := range []string{"deploy_node.sh", "scripts/bringup_production_stack.sh", "scripts/deployvalidator.sh", "nhb-go.sh"} {
		script := readRepoFile(t, rel)
		lines := strings.Split(script, "\n")
		for i, line := range lines {
			if !operatorOnlyCall.MatchString(line) {
				continue
			}
			// the curl command that carries the call, up to two lines before it
			start := i - 2
			if start < 0 {
				start = 0
			}
			window := strings.Join(lines[start:i+1], "\n")
			if !strings.Contains(window, "Authorization") {
				t.Errorf("%s:%d calls an operator-only method with no Authorization header:\n%s", rel, i+1, window)
			}
		}
	}
}

// A script that starts the node with the shipped configuration must give it the
// secret the configuration names for its JWT verifier, or the RPC server refuses
// to start (PL-DC-32).
func TestDeployNodeScriptSetsTheJWTSecretTheConfigNames(t *testing.T) {
	script := readRepoFile(t, "deploy_node.sh")
	if !strings.Contains(readRepoFile(t, "config.toml"), `HSSecretEnv = "NHB_RPC_JWT_SECRET"`) {
		t.Fatalf("config.toml no longer names NHB_RPC_JWT_SECRET: update this test and deploy_node.sh together")
	}
	if !strings.Contains(script, "export NHB_RPC_JWT_SECRET") {
		t.Errorf("deploy_node.sh starts the node without exporting NHB_RPC_JWT_SECRET")
	}
	if !strings.Contains(script, "rpc-token") || !strings.Contains(script, "--role operator") {
		t.Errorf("deploy_node.sh must mint an operator-role token for its own net_info check")
	}
	if strings.Contains(script, "echo \"$NHB_RPC_JWT_SECRET\"") || strings.Contains(script, "echo $NHB_RPC_JWT_SECRET") {
		t.Errorf("deploy_node.sh must not print the secret")
	}
}
