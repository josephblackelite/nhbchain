package identity

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// AliasRecord captures the metadata for a registered alias.
type AliasRecord struct {
	Alias     string
	Owner     [20]byte
	Primary   [20]byte
	Addresses [][20]byte
	AvatarRef string
	CreatedAt int64
	UpdatedAt int64
}

func (r *AliasRecord) Clone() *AliasRecord {
	if r == nil {
		return nil
	}
	clone := *r
	if len(r.Addresses) > 0 {
		clone.Addresses = make([][20]byte, len(r.Addresses))
		copy(clone.Addresses, r.Addresses)
	}
	return &clone
}

func (r *AliasRecord) AliasID() [32]byte {
	return DeriveAliasID(r.Alias)
}

const (
	aliasMinLength    = 3
	aliasMaxLength    = 32
	avatarRefMaxBytes = 512
)

var (
	aliasPattern = regexp.MustCompile(`^[a-z0-9._-]+$`)
	// ErrInvalidAlias is returned when the supplied alias does not satisfy
	// the naming constraints.
	ErrInvalidAlias = errors.New("identity: invalid alias")
	// ErrInvalidAddress is returned when the supplied address is malformed.
	ErrInvalidAddress = errors.New("identity: invalid address")
	// ErrAliasTaken is returned when the alias is already owned by another
	// address.
	ErrAliasTaken = errors.New("identity: alias already registered")
	// ErrAliasNotFound denotes that the requested alias record does not exist.
	ErrAliasNotFound = errors.New("identity: alias not found")
	// ErrInvalidAvatarRef indicates the avatar reference is malformed or
	// violates policy.
	ErrInvalidAvatarRef = errors.New("identity: invalid avatar reference")
	// ErrAddressLinked is returned when an address is already associated with
	// a different alias.
	ErrAddressLinked = errors.New("identity: address already linked to alias")
	// ErrAddressNotLinked is returned when attempting to remove or promote an
	// address that is not associated with the alias.
	ErrAddressNotLinked = errors.New("identity: address not linked to alias")
	// ErrPrimaryAddressRequired is returned when attempting to remove the
	// primary address from an alias.
	ErrPrimaryAddressRequired = errors.New("identity: cannot remove primary address")
	// ErrNotAliasOwner indicates the caller does not control the alias.
	ErrNotAliasOwner = errors.New("identity: caller is not alias owner")
)

// NormalizeAlias lowercases and validates the supplied alias.
func NormalizeAlias(alias string) (string, error) {
	trimmed := strings.TrimSpace(alias)
	lower := strings.ToLower(trimmed)
	length := len(lower)
	if length < aliasMinLength || length > aliasMaxLength {
		return "", fmt.Errorf("%w: must be between %d and %d characters", ErrInvalidAlias, aliasMinLength, aliasMaxLength)
	}
	if !aliasPattern.MatchString(lower) {
		return "", fmt.Errorf("%w: allowed characters are [a-z0-9._-]", ErrInvalidAlias)
	}
	return lower, nil
}

// DeriveAliasID returns the deterministic 32-byte identifier for the alias.
func DeriveAliasID(alias string) [32]byte {
	normalized := strings.ToLower(strings.TrimSpace(alias))
	hash := ethcrypto.Keccak256([]byte(normalized))
	var id [32]byte
	copy(id[:], hash)
	return id
}

// NormalizeAvatarRef validates the avatar reference and returns the canonical
// value if valid. Supported schemes are HTTPS and blob references.
func NormalizeAvatarRef(ref string) (string, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return "", ErrInvalidAvatarRef
	}
	if len(trimmed) > avatarRefMaxBytes {
		return "", fmt.Errorf("%w: exceeds %d characters", ErrInvalidAvatarRef, avatarRefMaxBytes)
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "blob://") {
		return trimmed, nil
	}
	return "", fmt.Errorf("%w: must use https:// or blob:// scheme", ErrInvalidAvatarRef)
}
