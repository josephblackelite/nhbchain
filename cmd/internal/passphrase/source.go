package passphrase

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"golang.org/x/term"
)

// Source lazily resolves a keystore passphrase from an environment variable or
// by prompting the operator. The value is cached after the first successful
// retrieval so repeated calls reuse the same secret.
type Source struct {
	envVar string

	once  sync.Once
	value string
	err   error
}

// NewSource constructs a passphrase source that checks envVar before
// interactively prompting on the terminal.
func NewSource(envVar string) *Source {
	return &Source{envVar: strings.TrimSpace(envVar)}
}

// Get returns the cached passphrase or resolves it if this is the first call.
// When the environment variable is set the exact value is used; otherwise the
// operator is prompted on stderr. Whitespace-only passphrases are rejected to
// avoid unprotected keystores.
func (s *Source) Get() (string, error) {
	s.once.Do(func() {
		if s.envVar != "" {
			if value, ok := os.LookupEnv(s.envVar); ok {
				if strings.TrimSpace(value) == "" {
					s.err = fmt.Errorf("%s is set but empty", s.envVar)
					return
				}
				s.value = value
				return
			}
		}

		if !term.IsTerminal(int(os.Stdin.Fd())) {
			if s.envVar != "" {
				s.err = fmt.Errorf("validator keystore passphrase required; set %s or run interactively", s.envVar)
			} else {
				s.err = errors.New("validator keystore passphrase required and no terminal available")
			}
			return
		}

		fmt.Fprint(os.Stderr, "Enter validator keystore passphrase: ")
		bytes, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			s.err = fmt.Errorf("failed to read passphrase: %w", err)
			return
		}

		passphrase := string(bytes)
		if strings.TrimSpace(passphrase) == "" {
			s.err = errors.New("validator keystore passphrase cannot be empty")
			return
		}

		s.value = passphrase
	})

	return s.value, s.err
}
