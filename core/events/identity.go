package events

import (
	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	TypeIdentityAliasSet     = "identity.alias.set"
	TypeIdentityAliasRenamed = "identity.alias.renamed"
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
			"address": crypto.NewAddress(crypto.NHBPrefix, e.Address[:]).String(),
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
			"address": crypto.NewAddress(crypto.NHBPrefix, e.Address[:]).String(),
		},
	}
}
