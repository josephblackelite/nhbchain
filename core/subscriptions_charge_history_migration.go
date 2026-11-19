package core

// PL-R5T-82: a subscription's charge history under the old key is read through and
// migrated, from a configured height.
//
// The history used to be one list per subscription (subscriptions/charges/<id>). The
// commit that moved it to one key per attempt plus a count never reads the old key, so
// a subscription holding history there lists as empty, restarts its attempt numbering
// at 1 and leaves the old list orphaned, and the first attempt recorded writes other
// keys than the old code would have, so the state root differs from it. That commit says
// no subscription was ever charged on the live chain; if so none of this matters, and
// the setting stays off. From subsChargeMigrationFromHeight the settlement's registry
// reads the old list when a subscription has no charge count and the first attempt
// recorded copies it (see native/subscriptions Registry.SetLegacyChargeHistory).

// SetSubscriptionsChargeHistoryMigration sets the first block height from which charge
// history under the old key is read through and migrated. Zero (the default) leaves the
// newer layout alone at every height. A chain rule: the same value on every validator,
// set before the chain reaches the height.
func (sp *StateProcessor) SetSubscriptionsChargeHistoryMigration(fromHeight uint64) {
	if sp == nil {
		return
	}
	sp.subsChargeMigrationFromHeight = fromHeight
}

// subscriptionsChargeMigrationActive reports whether the block being executed reads
// the old key through.
func (sp *StateProcessor) subscriptionsChargeMigrationActive() bool {
	if sp == nil || sp.subsChargeMigrationFromHeight == 0 {
		return false
	}
	return sp.blockHeight() >= sp.subsChargeMigrationFromHeight
}
