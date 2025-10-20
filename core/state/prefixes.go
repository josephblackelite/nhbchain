package state

var (
	stakingGlobalIndexKeyBytes      = []byte("staking/globalIndex")
	stakingLastIndexUpdateTsKeyByte = []byte("staking/lastUpdate")
	stakingEmissionYTDKeyFormat     = "staking/ytdEmissions/%04d"
	mintEmissionYTDKeyFormat        = "mint/%s/ytdEmissions/%04d"
	stakingAccountPrefix            = []byte("staking/account/")
)
