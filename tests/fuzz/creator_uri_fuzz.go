package fuzz

import (
	"math/big"
	"net/url"
	"strings"
	"testing"

	"nhbchain/core/types"
	"nhbchain/native/creator"
)

type fuzzCreatorState struct {
	contents map[string]*creator.Content
}

func newFuzzCreatorState() *fuzzCreatorState {
	return &fuzzCreatorState{contents: make(map[string]*creator.Content)}
}

func (s *fuzzCreatorState) CreatorContentGet(id string) (*creator.Content, bool, error) {
	content, ok := s.contents[id]
	if !ok {
		return nil, false, nil
	}
	clone := *content
	if content.TotalTips != nil {
		clone.TotalTips = new(big.Int).Set(content.TotalTips)
	}
	if content.TotalStake != nil {
		clone.TotalStake = new(big.Int).Set(content.TotalStake)
	}
	return &clone, true, nil
}

func (s *fuzzCreatorState) CreatorContentPut(content *creator.Content) error {
	if content == nil {
		return nil
	}
	clone := *content
	if content.TotalTips != nil {
		clone.TotalTips = new(big.Int).Set(content.TotalTips)
	}
	if content.TotalStake != nil {
		clone.TotalStake = new(big.Int).Set(content.TotalStake)
	}
	s.contents[content.ID] = &clone
	return nil
}

func (s *fuzzCreatorState) CreatorStakeGet([20]byte, [20]byte) (*creator.Stake, bool, error) {
	return nil, false, nil
}

func (s *fuzzCreatorState) CreatorStakePut(*creator.Stake) error { return nil }

func (s *fuzzCreatorState) CreatorStakeDelete([20]byte, [20]byte) error { return nil }

func (s *fuzzCreatorState) CreatorPayoutLedgerGet([20]byte) (*creator.PayoutLedger, bool, error) {
	return nil, false, nil
}

func (s *fuzzCreatorState) CreatorPayoutLedgerPut(*creator.PayoutLedger) error { return nil }

func (s *fuzzCreatorState) GetAccount([]byte) (*types.Account, error) { return nil, nil }

func (s *fuzzCreatorState) PutAccount([]byte, *types.Account) error { return nil }

func (s *fuzzCreatorState) CreatorRateLimitGet() (*creator.RateLimitSnapshot, bool, error) {
	return nil, false, nil
}

func (s *fuzzCreatorState) CreatorRateLimitPut(*creator.RateLimitSnapshot) error { return nil }

func FuzzCreatorURISanitization(f *testing.F) {
	seeds := []string{
		"https://example.com/content",
		" ipfs://QmExample ",
		"nhb://creator/alpha",
		"ftp://unsupported",
		"",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, rawURI string) {
		engine := creator.NewEngine()
		engine.SetState(newFuzzCreatorState())
		engine.SetNowFunc(func() int64 { return 1 })

		content, err := engine.PublishContent([20]byte{0x01}, "content-id", rawURI, rawURI)
		if err != nil {
			return
		}
		if content == nil {
			t.Fatalf("publish returned nil without error")
		}
		trimmed := strings.TrimSpace(rawURI)
		if content.URI != trimmed {
			t.Fatalf("uri was not trimmed: got %q want %q", content.URI, trimmed)
		}
		if len(content.URI) == 0 {
			t.Fatalf("empty uri accepted")
		}
		if len(content.URI) > 512 {
			t.Fatalf("uri exceeds maximum length: %d", len(content.URI))
		}
		parsed, parseErr := url.Parse(content.URI)
		if parseErr != nil || parsed == nil {
			t.Fatalf("sanitized uri failed to parse: %v", parseErr)
		}
		scheme := strings.ToLower(parsed.Scheme)
		switch scheme {
		case "https", "ipfs", "ar", "nhb":
		default:
			t.Fatalf("unexpected scheme accepted: %q", scheme)
		}
	})
}
