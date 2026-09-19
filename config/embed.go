package config

import _ "embed"

// MainnetGenesis is the genesis file of the live network (genesis.relaunch.json,
// chain id 18346390202490284624). A node whose configured genesis file is
// missing writes these bytes there, so a fresh node always starts from the
// live chain identity. The bytes must never change: the chain id is derived
// from the hash of the genesis block built from them.
//
//go:embed genesis.relaunch.json
var MainnetGenesis []byte
