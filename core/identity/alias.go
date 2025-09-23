package identity

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// AliasRecord captures the metadata for a registered alias.
type AliasRecord struct {
	Alias     string
	Address   [20]byte
	CreatedAt int64
	UpdatedAt int64
}

const (
	aliasMinLength = 3
	aliasMaxLength = 32
)

var (
	aliasPattern = regexp.MustCompile(`^[a-z0-9._-]+$`)
	// ErrInvalidAlias is returned when the supplied alias does not satisfy
	// the naming constraints.
	ErrInvalidAlias = errors.New("identity: invalid alias")
	// ErrAliasTaken is returned when the alias is already owned by another
	// address.
	ErrAliasTaken = errors.New("identity: alias already registered")
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
