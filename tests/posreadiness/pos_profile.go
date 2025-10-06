//go:build posreadiness

package posreadiness

// Suite describes a single POS readiness check.
type Suite struct {
	Name string   // human-friendly identifier used for logs and targets
	Args []string // go test binary arguments for this suite
}

const (
	// BinaryName is the name of the compiled go test binary generated for the profile.
	BinaryName = "posreadiness.test"
	// ArtifactDir captures the directory used for readiness binaries.
	ArtifactDir = "artifacts"
	// LogDir captures the directory used for readiness logs.
	LogDir = "logs/pos"
)

// Profile enumerates the suites included in the POS readiness profile.
var Profile = []Suite{
	{
		Name: "intent",
		Args: []string{"-test.v", "-test.run", "^TestIntentReadiness$"},
	},
	{
		Name: "paymaster",
		Args: []string{"-test.v", "-test.run", "^TestPaymasterReadiness$"},
	},
	{
		Name: "registry",
		Args: []string{"-test.v", "-test.run", "^TestRegistryReadiness$"},
	},
	{
		Name: "realtime",
		Args: []string{"-test.v", "-test.run", "^TestRealtimeReadiness$"},
	},
	{
		Name: "security",
		Args: []string{"-test.v", "-test.run", "^TestSecurityReadiness$"},
	},
	{
		Name: "fees",
		Args: []string{"-test.v", "-test.run", "^TestFeesReadiness$"},
	},
	{
		Name: "bench-qos",
		Args: []string{"-test.run", "^$", "-test.bench", "^BenchmarkPOSQOS$", "-test.benchmem"},
	},
}
