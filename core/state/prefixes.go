package state

var (
	stakingGlobalIndexKeyBytes      = []byte("staking/globalIndex")
	stakingLastIndexUpdateTsKeyByte = []byte("staking/lastUpdate")
	stakingEmissionYTDKeyFormat     = "staking/ytdEmissions/%04d"
	mintEmissionYTDKeyFormat        = "mint/%s/ytdEmissions/%04d"
	stakingAccountPrefix            = []byte("staking/account/")

	// stakeDelegatorIndexPrefix + a validator's 20-byte address is the raw
	// (un-hashed) key for the KVAppend/KVGetList-backed list of every
	// address currently delegating to that validator -- added 2026-08-13
	// alongside stakeDelegatedInTotalPrefix to fix reward attribution for
	// third-party delegation (see StakeValidatorDelegators).
	stakeDelegatorIndexPrefix = []byte("staking/delegators/")

	// stakeDelegatedInTotalPrefix + a validator's 20-byte address composes
	// the loadBigInt/writeBigInt key for that validator's running total of
	// stake delegated to it by others (excluding its own self-stake) --
	// see StakeValidatorDelegatedInTotal.
	stakeDelegatedInTotalPrefix = []byte("staking/delegatedIn/")

	// stakeDelegationIndexBackfilledKey guards the one-time migration that
	// populates the two indexes above from already-existing delegations
	// created before this fix -- see StakeDelegationIndexBackfilled.
	stakeDelegationIndexBackfilledKey = []byte("staking/delegationIndexBackfilled")
)
