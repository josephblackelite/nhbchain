package events

import (
	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	TypeIdentityAliasSet            = "identity.alias.set"
	TypeIdentityAliasRenamed        = "identity.alias.renamed"
	TypeIdentityAliasAvatarUpdated  = "identity.alias.avatarUpdated"
	TypeIdentityAliasAddressLinked  = "identity.alias.addressLinked"
	TypeIdentityAliasAddressRemoved = "identity.alias.addressRemoved"
	TypeIdentityAliasPrimaryUpdated = "identity.alias.primaryUpdated"
)

// IdentityAliasSet is emitted when an address registers an alias for the first time.
type IdentityAliasSet struct {
	Alias   string
	Address [20]byte
}

// EventType implements the Event interface.
func (IdentityAliasSet) EventType() string { return TypeIdentityAliasSet }

// Event converts the strongly typed event to the generic representation used by subscribers.
func (e IdentityAliasSet) Event() *types.Event {
	return &types.Event{
		Type: TypeIdentityAliasSet,
		Attributes: map[string]string{
			"alias":   e.Alias,
			"address": crypto.MustNewAddress(crypto.NHBPrefix, e.Address[:]).String(),
		},
	}
}

// IdentityAliasRenamed is emitted when an existing alias is moved to a new value for the same address.
type IdentityAliasRenamed struct {
	OldAlias string
	NewAlias string
	Address  [20]byte
}

// EventType implements the Event interface.
func (IdentityAliasRenamed) EventType() string { return TypeIdentityAliasRenamed }

// Event converts the strongly typed event to the generic representation used by subscribers.
func (e IdentityAliasRenamed) Event() *types.Event {
	return &types.Event{
		Type: TypeIdentityAliasRenamed,
		Attributes: map[string]string{
			"old":     e.OldAlias,
			"new":     e.NewAlias,
			"address": crypto.MustNewAddress(crypto.NHBPrefix, e.Address[:]).String(),
		},
	}
}

// IdentityAliasAvatarUpdated is emitted when an alias updates its avatar reference.
type IdentityAliasAvatarUpdated struct {
	Alias     string
	Address   [20]byte
	AvatarRef string
}

// EventType implements the Event interface.
func (IdentityAliasAvatarUpdated) EventType() string { return TypeIdentityAliasAvatarUpdated }

// Event converts the strongly typed event to the generic representation used by subscribers.
func (e IdentityAliasAvatarUpdated) Event() *types.Event {
	return &types.Event{
		Type: TypeIdentityAliasAvatarUpdated,
		Attributes: map[string]string{
			"alias":     e.Alias,
			"address":   crypto.MustNewAddress(crypto.NHBPrefix, e.Address[:]).String(),
			"avatarRef": e.AvatarRef,
		},
	}
}

// IdentityAliasAddressLinked is emitted when an alias links a new secondary address.
type IdentityAliasAddressLinked struct {
	Alias   string
	Address [20]byte
}

// EventType implements the Event interface.
func (IdentityAliasAddressLinked) EventType() string { return TypeIdentityAliasAddressLinked }

// Event converts the strongly typed event to the generic representation used by subscribers.
func (e IdentityAliasAddressLinked) Event() *types.Event {
	return &types.Event{
		Type: TypeIdentityAliasAddressLinked,
		Attributes: map[string]string{
			"alias":   e.Alias,
			"address": crypto.MustNewAddress(crypto.NHBPrefix, e.Address[:]).String(),
		},
	}
}

// IdentityAliasAddressRemoved is emitted when an alias unlinks an address.
type IdentityAliasAddressRemoved struct {
	Alias   string
	Address [20]byte
}

// EventType implements the Event interface.
func (IdentityAliasAddressRemoved) EventType() string { return TypeIdentityAliasAddressRemoved }

// Event converts the strongly typed event to the generic representation used by subscribers.
func (e IdentityAliasAddressRemoved) Event() *types.Event {
	return &types.Event{
		Type: TypeIdentityAliasAddressRemoved,
		Attributes: map[string]string{
			"alias":   e.Alias,
			"address": crypto.MustNewAddress(crypto.NHBPrefix, e.Address[:]).String(),
		},
	}
}

// IdentityAliasPrimaryUpdated is emitted when the primary address for an alias changes.
type IdentityAliasPrimaryUpdated struct {
	Alias   string
	Address [20]byte
}

// EventType implements the Event interface.
func (IdentityAliasPrimaryUpdated) EventType() string { return TypeIdentityAliasPrimaryUpdated }

// Event converts the strongly typed event to the generic representation used by subscribers.
func (e IdentityAliasPrimaryUpdated) Event() *types.Event {
	return &types.Event{
		Type: TypeIdentityAliasPrimaryUpdated,
		Attributes: map[string]string{
			"alias":   e.Alias,
			"address": crypto.MustNewAddress(crypto.NHBPrefix, e.Address[:]).String(),
		},
	}
}
