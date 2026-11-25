package config

// productionNetworkNames are the network names a production deployment of this
// chain runs under. "nhbchain-1" is the name the live validators use; "mainnet"
// is the generic name the shipped documents use.
var productionNetworkNames = []string{"mainnet", "nhbchain-1"}

// isProductionNetworkName reports whether name is one of the production
// network names. Checks that must hold on production (a block timestamp
// tolerance of 5 seconds, swap RPC secrets present) all ask this one function,
// so a new production name is added in one place.
func isProductionNetworkName(name string) bool {
	for _, candidate := range productionNetworkNames {
		if name == candidate {
			return true
		}
	}
	return false
}
