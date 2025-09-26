package modules

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
	"nhbchain/native/escrow"
)

// EscrowModule exposes read helpers for escrow governance metadata and event history.
type EscrowModule struct {
	node *core.Node
}

// NewEscrowModule constructs an escrow RPC helper module.
func NewEscrowModule(node *core.Node) *EscrowModule {
	return &EscrowModule{node: node}
}

type getRealmParams struct {
	ID string `json:"id"`
}

type getSnapshotParams struct {
	ID string `json:"id"`
}

type listEventsParams struct {
	Prefix string `json:"prefix,omitempty"`
	Limit  *int   `json:"limit,omitempty"`
}

// EscrowRealmResult represents an arbitration realm definition returned over RPC.
type EscrowRealmResult struct {
	ID              string                  `json:"id"`
	Version         uint64                  `json:"version"`
	NextPolicyNonce uint64                  `json:"nextPolicyNonce"`
	CreatedAt       int64                   `json:"createdAt"`
	UpdatedAt       int64                   `json:"updatedAt"`
	Arbitrators     *EscrowArbitratorResult `json:"arbitrators,omitempty"`
}

// EscrowArbitratorResult captures the resolved arbitrator policy for a realm.
type EscrowArbitratorResult struct {
	Scheme    string   `json:"scheme"`
	Threshold uint32   `json:"threshold"`
	Members   []string `json:"members,omitempty"`
}

// EscrowSnapshotResult describes the latest persisted escrow state along with
// any frozen arbitration policy metadata.
type EscrowSnapshotResult struct {
	ID             string              `json:"id"`
	Payer          string              `json:"payer"`
	Payee          string              `json:"payee"`
	Mediator       *string             `json:"mediator,omitempty"`
	Token          string              `json:"token"`
	Amount         string              `json:"amount"`
	FeeBps         uint32              `json:"feeBps"`
	Deadline       int64               `json:"deadline"`
	CreatedAt      int64               `json:"createdAt"`
	Status         string              `json:"status"`
	Meta           string              `json:"meta"`
	Realm          *string             `json:"realm,omitempty"`
	FrozenPolicy   *FrozenPolicyResult `json:"frozenPolicy,omitempty"`
	ResolutionHash *string             `json:"resolutionHash,omitempty"`
}

// FrozenPolicyResult exposes the immutable arbitrator policy captured at
// escrow creation time.
type FrozenPolicyResult struct {
	RealmID      string   `json:"realmId"`
	RealmVersion uint64   `json:"realmVersion"`
	PolicyNonce  uint64   `json:"policyNonce"`
	Scheme       string   `json:"scheme"`
	Threshold    uint32   `json:"threshold"`
	Members      []string `json:"members,omitempty"`
	FrozenAt     int64    `json:"frozenAt,omitempty"`
}

// EscrowEventResult represents an emitted escrow-related event.
type EscrowEventResult struct {
	Sequence   int64             `json:"sequence"`
	Type       string            `json:"type"`
	Attributes map[string]string `json:"attributes"`
}

var (
	errRealmNotFound = errors.New("escrow realm not found")
	errModuleOffline = &ModuleError{HTTPStatus: http.StatusInternalServerError, Code: codeServerError, Message: "escrow module not initialised"}
)

// GetRealm resolves the supplied arbitration realm identifier.
func (m *EscrowModule) GetRealm(raw json.RawMessage) (*EscrowRealmResult, *ModuleError) {
	if m == nil || m.node == nil {
		return nil, errModuleOffline
	}
	var params getRealmParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "invalid parameter object", Data: err.Error()}
	}
	trimmed := strings.TrimSpace(params.ID)
	if trimmed == "" {
		return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "id is required"}
	}
	var realm *escrow.EscrowRealm
	err := m.node.WithState(func(manager *nhbstate.Manager) error {
		resolved, ok, getErr := manager.EscrowRealmGet(trimmed)
		if getErr != nil {
			return getErr
		}
		if !ok {
			return errRealmNotFound
		}
		realm = resolved
		return nil
	})
	if err != nil {
		if errors.Is(err, errRealmNotFound) {
			return nil, &ModuleError{HTTPStatus: http.StatusNotFound, Code: codeInvalidParams, Message: "realm not found"}
		}
		return nil, &ModuleError{HTTPStatus: http.StatusInternalServerError, Code: codeServerError, Message: err.Error()}
	}
	result := formatRealmResult(realm)
	return &result, nil
}

// GetSnapshot fetches the canonical escrow state for the provided identifier.
func (m *EscrowModule) GetSnapshot(raw json.RawMessage) (*EscrowSnapshotResult, *ModuleError) {
	if m == nil || m.node == nil {
		return nil, errModuleOffline
	}
	var params getSnapshotParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "invalid parameter object", Data: err.Error()}
	}
	id, err := parseEscrowID(params.ID)
	if err != nil {
		return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: err.Error()}
	}
	snapshot, err := m.node.EscrowGet(id)
	if err != nil {
		if errors.Is(err, core.ErrEscrowNotFound) {
			return nil, &ModuleError{HTTPStatus: http.StatusNotFound, Code: codeInvalidParams, Message: "escrow not found"}
		}
		return nil, &ModuleError{HTTPStatus: http.StatusInternalServerError, Code: codeServerError, Message: err.Error()}
	}
	result := formatSnapshotResult(snapshot)
	return result, nil
}

// ListEvents returns recent escrow-related events emitted by the node. The
// optional prefix parameter can narrow results to a specific namespace such as
// "escrow.realm.".
func (m *EscrowModule) ListEvents(raw json.RawMessage) ([]EscrowEventResult, *ModuleError) {
	if m == nil || m.node == nil {
		return nil, errModuleOffline
	}
	var params listEventsParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "invalid parameter object", Data: err.Error()}
		}
	}
	prefix := "escrow."
	if trimmed := strings.TrimSpace(params.Prefix); trimmed != "" {
		prefix = trimmed
	}
	normalizedPrefix := strings.ToLower(prefix)
	events := m.node.Events()
	results := make([]EscrowEventResult, 0, len(events))
	for _, evt := range events {
		if !strings.HasPrefix(strings.ToLower(evt.Type), normalizedPrefix) {
			continue
		}
		attrs := make(map[string]string, len(evt.Attributes))
		for k, v := range evt.Attributes {
			attrs[k] = v
		}
		results = append(results, EscrowEventResult{Type: evt.Type, Attributes: attrs})
	}
	if params.Limit != nil {
		limit := *params.Limit
		if limit < 0 {
			limit = 0
		}
		if limit < len(results) {
			results = results[:limit]
		}
	}
	for i := range results {
		results[i].Sequence = int64(i + 1)
	}
	return results, nil
}

func formatRealmResult(realm *escrow.EscrowRealm) EscrowRealmResult {
	if realm == nil {
		return EscrowRealmResult{}
	}
	result := EscrowRealmResult{
		ID:              strings.TrimSpace(realm.ID),
		Version:         realm.Version,
		NextPolicyNonce: realm.NextPolicyNonce,
		CreatedAt:       realm.CreatedAt,
		UpdatedAt:       realm.UpdatedAt,
	}
	if realm.Arbitrators != nil {
		result.Arbitrators = &EscrowArbitratorResult{
			Scheme:    formatScheme(realm.Arbitrators.Scheme),
			Threshold: realm.Arbitrators.Threshold,
			Members:   formatAddressList(realm.Arbitrators.Members),
		}
	}
	return result
}

func formatSnapshotResult(esc *escrow.Escrow) *EscrowSnapshotResult {
	if esc == nil {
		return nil
	}
	amount := "0"
	if esc.Amount != nil {
		amount = esc.Amount.String()
	}
	result := &EscrowSnapshotResult{
		ID:        formatEscrowID(esc.ID),
		Payer:     crypto.NewAddress(crypto.NHBPrefix, esc.Payer[:]).String(),
		Payee:     crypto.NewAddress(crypto.NHBPrefix, esc.Payee[:]).String(),
		Token:     esc.Token,
		Amount:    amount,
		FeeBps:    esc.FeeBps,
		Deadline:  esc.Deadline,
		CreatedAt: esc.CreatedAt,
		Status:    escrowStatusString(esc.Status),
		Meta:      "0x" + hex.EncodeToString(esc.MetaHash[:]),
	}
	if esc.Mediator != ([20]byte{}) {
		mediator := crypto.NewAddress(crypto.NHBPrefix, esc.Mediator[:]).String()
		result.Mediator = &mediator
	}
	if trimmed := strings.TrimSpace(esc.RealmID); trimmed != "" {
		realm := trimmed
		result.Realm = &realm
	}
	if esc.FrozenArb != nil {
		result.FrozenPolicy = &FrozenPolicyResult{
			RealmID:      strings.TrimSpace(esc.FrozenArb.RealmID),
			RealmVersion: esc.FrozenArb.RealmVersion,
			PolicyNonce:  esc.FrozenArb.PolicyNonce,
			Scheme:       formatScheme(esc.FrozenArb.Scheme),
			Threshold:    esc.FrozenArb.Threshold,
			Members:      formatAddressList(esc.FrozenArb.Members),
			FrozenAt:     esc.FrozenArb.FrozenAt,
		}
	}
	if esc.ResolutionHash != ([32]byte{}) {
		hash := "0x" + hex.EncodeToString(esc.ResolutionHash[:])
		result.ResolutionHash = &hash
	}
	return result
}

func formatScheme(s escrow.ArbitrationScheme) string {
	switch s {
	case escrow.ArbitrationSchemeSingle:
		return "single"
	case escrow.ArbitrationSchemeCommittee:
		return "committee"
	default:
		return "unspecified"
	}
}

func formatAddressList(members [][20]byte) []string {
	if len(members) == 0 {
		return nil
	}
	out := make([]string, 0, len(members))
	for _, member := range members {
		if member == ([20]byte{}) {
			continue
		}
		out = append(out, crypto.NewAddress(crypto.NHBPrefix, member[:]).String())
	}
	return out
}

func parseEscrowID(id string) ([32]byte, error) {
	var out [32]byte
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return out, fmt.Errorf("id required")
	}
	cleaned := strings.TrimPrefix(strings.TrimPrefix(trimmed, "0x"), "0X")
	if len(cleaned) != 64 {
		return out, fmt.Errorf("id must be 32 bytes")
	}
	bytes, err := hex.DecodeString(cleaned)
	if err != nil {
		return out, err
	}
	copy(out[:], bytes)
	return out, nil
}

func formatEscrowID(id [32]byte) string {
	return "0x" + hex.EncodeToString(id[:])
}

func escrowStatusString(status escrow.EscrowStatus) string {
	switch status {
	case escrow.EscrowInit:
		return "init"
	case escrow.EscrowFunded:
		return "funded"
	case escrow.EscrowReleased:
		return "released"
	case escrow.EscrowRefunded:
		return "refunded"
	case escrow.EscrowExpired:
		return "expired"
	case escrow.EscrowDisputed:
		return "disputed"
	default:
		return "unknown"
	}
}
