package state

var (
	stakingGlobalIndexKeyBytes      = []byte("staking/index/global")
	stakingLastIndexUpdateTsKeyByte = []byte("staking/index/last-ts")
	stakingEmissionYTDKeyFormat     = "staking/emissions/%04d"
	stakingAccountPrefix            = []byte("staking/account/")
)
