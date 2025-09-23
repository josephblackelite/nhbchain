package loyalty

var (
	programPrefix             = []byte("loyalty/program/")
	programOwnerIndexPref     = []byte("loyalty/merchant/")
	businessPrefix            = []byte("loyalty/business/")
	businessOwnerPrefix       = []byte("loyalty/business-owner/")
	merchantBusinessIndexPref = []byte("loyalty/merchant-index/")
	businessCounterKeyBytes   = []byte("loyalty/business/counter")
	ownerPaymasterPrefix      = []byte("loyalty/owner-paymaster/")
)

func programKey(id ProgramID) []byte {
	key := make([]byte, len(programPrefix)+len(id))
	copy(key, programPrefix)
	copy(key[len(programPrefix):], id[:])
	return key
}

func merchantIdxKey(owner [20]byte) []byte {
	key := make([]byte, len(programOwnerIndexPref)+len(owner))
	copy(key, programOwnerIndexPref)
	copy(key[len(programOwnerIndexPref):], owner[:])
	return key
}

// ProgramStorageKey returns the raw storage key used to persist program metadata.
func ProgramStorageKey(id ProgramID) []byte {
	return programKey(id)
}

// ProgramOwnerIndexKey returns the raw storage key used to index programs by owner.
func ProgramOwnerIndexKey(owner [20]byte) []byte {
	return merchantIdxKey(owner)
}

func businessKey(id BusinessID) []byte {
	key := make([]byte, len(businessPrefix)+len(id))
	copy(key, businessPrefix)
	copy(key[len(businessPrefix):], id[:])
	return key
}

func businessOwnerKey(owner [20]byte) []byte {
	key := make([]byte, len(businessOwnerPrefix)+len(owner))
	copy(key, businessOwnerPrefix)
	copy(key[len(businessOwnerPrefix):], owner[:])
	return key
}

func merchantBusinessIndexKey(addr [20]byte) []byte {
	key := make([]byte, len(merchantBusinessIndexPref)+len(addr))
	copy(key, merchantBusinessIndexPref)
	copy(key[len(merchantBusinessIndexPref):], addr[:])
	return key
}

func businessCounterKey() []byte {
	return append([]byte(nil), businessCounterKeyBytes...)
}

func ownerPaymasterKey(owner [20]byte) []byte {
	key := make([]byte, len(ownerPaymasterPrefix)+len(owner))
	copy(key, ownerPaymasterPrefix)
	copy(key[len(ownerPaymasterPrefix):], owner[:])
	return key
}
