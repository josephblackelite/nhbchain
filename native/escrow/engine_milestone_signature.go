package escrow

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// NHB-AUDIT-C3: milestone escrow's Fund/Release/Cancel/SubscriptionUpdate
// and Create RPCs used to authorize a mutation by trusting a bare,
// client-supplied "caller"/"payer" address string, with zero proof the
// caller actually controlled the claimed key. An attacker could create a
// project naming a victim as payer and themselves as payee, then fund and
// release it, moving the victim's real balance with no wallet-signature
// proof at all. This file adds the same delegated-signature verification
// classic escrow already uses (see engine.go's escrowActionEnvelope /
// escrowCreateEnvelope / RecoverSigner): the caller is derived SOLELY
// from a cryptographically recovered signer, never a client-supplied
// address field, and Create's envelope is reconstructed by the caller
// (core.Node) from the exact, already-parsed project fields that will
// actually be persisted -- never from a separately supplied payload blob
// that could diverge from what's actually created.

// MilestoneActionEnvelope is the canonical payload a milestone project's
// payer signs to authorize a fund/release/cancel/subscription-update
// action. LegID is omitted (zero) and Active is nil for
// MilestoneActionSubscriptionUpdate's sibling action-independent case;
// Active carries the toggle's target value so a signature for "turn on"
// can never be replayed to also mean "turn off" (mirrors how
// DisputeWithSignature's reason travels inside its signed envelope).
type MilestoneActionEnvelope struct {
	ProjectID string `json:"projectId"`
	LegID     uint64 `json:"legId,omitempty"`
	Action    string `json:"action"`
	Active    *bool  `json:"active,omitempty"`
	// Sequence binds a milestoneSubscriptionUpdate signature to the
	// subscription's current MilestoneSubscription.Sequence counter
	// (NHB-AUDIT-C3 follow-up) so a captured toggle signature can never be
	// replayed once any later toggle has actually committed. It is always
	// nil (and therefore omitted from the signed JSON entirely) for every
	// other action, so it never changes what Fund/Release/Cancel/Create
	// sign over.
	Sequence *uint64 `json:"sequence,omitempty"`
}

// MilestoneAction* are MilestoneActionEnvelope.Action's wire values.
const (
	MilestoneActionCreate             = "milestoneCreate"
	MilestoneActionFund               = "milestoneFund"
	MilestoneActionRelease            = "milestoneRelease"
	MilestoneActionCancel             = "milestoneCancel"
	MilestoneActionSubscriptionUpdate = "milestoneSubscriptionUpdate"
)

func buildMilestoneActionEnvelope(id [32]byte, legID uint64, action string, active *bool, sequence *uint64) ([]byte, error) {
	envelope := MilestoneActionEnvelope{
		ProjectID: hex.EncodeToString(id[:]),
		LegID:     legID,
		Action:    action,
		Active:    active,
		Sequence:  sequence,
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("escrow: build milestone action envelope: %w", err)
	}
	return payload, nil
}

// RecoverMilestoneActionSigner reconstructs the canonical
// MilestoneActionEnvelope for the given (projectId, legId, action[,
// active]) tuple and recovers the address that produced signature over
// it. The caller (core.Node) must treat the RETURNED address as the sole
// source of truth for who is authorizing this action -- never a
// separately supplied address parameter. Sequence is always omitted here;
// use RecoverMilestoneSubscriptionSigner for milestoneSubscriptionUpdate.
func RecoverMilestoneActionSigner(id [32]byte, legID uint64, action string, active *bool, signature []byte) ([20]byte, error) {
	payload, err := buildMilestoneActionEnvelope(id, legID, action, active, nil)
	if err != nil {
		return [20]byte{}, err
	}
	return recoverMilestoneSigner(payload, signature)
}

// SignMilestoneActionEnvelope signs the canonical MilestoneActionEnvelope
// for (projectId, legId, action[, active]) with priv. Exported for tests
// and reference Go clients constructing the wire signature -- the actual
// authorization check (RecoverMilestoneActionSigner) never calls this.
// Sequence is always omitted here; use SignMilestoneSubscriptionEnvelope
// for milestoneSubscriptionUpdate.
func SignMilestoneActionEnvelope(id [32]byte, legID uint64, action string, active *bool, priv *ecdsa.PrivateKey) ([]byte, error) {
	payload, err := buildMilestoneActionEnvelope(id, legID, action, active, nil)
	if err != nil {
		return nil, err
	}
	return signMilestonePayload(payload, priv)
}

// RecoverMilestoneSubscriptionSigner reconstructs the canonical
// MilestoneActionEnvelope for a milestoneSubscriptionUpdate action bound to
// (projectId, active, expectedSequence) and recovers the address that
// produced signature over it. NHB-AUDIT-C3 follow-up: the caller
// (core.Node) must pass the project's OWN current, persisted
// MilestoneSubscription.Sequence as expectedSequence -- read from state,
// never from a client-supplied field -- so a signature only recovers to
// the true payer when it was produced against the sequence value that is
// actually live right now. A captured signature for an earlier sequence
// value reconstructs a different envelope once the state has moved on
// (whether from this same toggle already applying once, or from any later
// toggle), so it recovers a signer that will not equal the project's
// payer and the update is rejected -- exactly the same "derive the caller
// solely from the signature" pattern RecoverMilestoneActionSigner already
// uses for Fund/Release/Cancel, extended with the one extra binding this
// action's mutable Active field requires.
func RecoverMilestoneSubscriptionSigner(id [32]byte, active bool, expectedSequence uint64, signature []byte) ([20]byte, error) {
	payload, err := buildMilestoneActionEnvelope(id, 0, MilestoneActionSubscriptionUpdate, &active, &expectedSequence)
	if err != nil {
		return [20]byte{}, err
	}
	return recoverMilestoneSigner(payload, signature)
}

// SignMilestoneSubscriptionEnvelope signs the canonical
// milestoneSubscriptionUpdate envelope for (projectId, active,
// expectedSequence) with priv. expectedSequence must be the CURRENT
// MilestoneSubscription.Sequence the signer observed (e.g. via
// EscrowMilestoneGet) -- signing a stale value produces a signature that
// RecoverMilestoneSubscriptionSigner will reject once state has moved past
// it. Exported for tests and reference Go clients constructing the wire
// signature -- the actual authorization check
// (RecoverMilestoneSubscriptionSigner) never calls this.
func SignMilestoneSubscriptionEnvelope(id [32]byte, active bool, expectedSequence uint64, priv *ecdsa.PrivateKey) ([]byte, error) {
	payload, err := buildMilestoneActionEnvelope(id, 0, MilestoneActionSubscriptionUpdate, &active, &expectedSequence)
	if err != nil {
		return nil, err
	}
	return signMilestonePayload(payload, priv)
}

// MilestoneLegEnvelope mirrors one leg's signed fields within
// MilestoneCreateEnvelope.
type MilestoneLegEnvelope struct {
	ID       uint64 `json:"id"`
	Type     string `json:"type"`
	Token    string `json:"token"`
	Amount   string `json:"amount"`
	Deadline int64  `json:"deadline"`
}

// MilestoneSubscriptionEnvelope mirrors a project's optional subscription
// fields within MilestoneCreateEnvelope.
type MilestoneSubscriptionEnvelope struct {
	IntervalSeconds int64 `json:"intervalSeconds"`
	NextReleaseAt   int64 `json:"nextReleaseAt"`
	Active          bool  `json:"active"`
}

// MilestoneCreateEnvelope is the canonical payload a would-be payer signs
// to authorize creating a milestone project naming them as payer. Built
// exclusively from the fields that will actually be persisted (see
// RecoverMilestoneCreateSigner), so there is no way for the signed intent
// to diverge from the project actually created.
type MilestoneCreateEnvelope struct {
	Action       string                         `json:"action"`
	Payer        string                         `json:"payer"`
	Payee        string                         `json:"payee"`
	Realm        string                         `json:"realm,omitempty"`
	Meta         string                         `json:"meta,omitempty"`
	Legs         []MilestoneLegEnvelope         `json:"legs"`
	Subscription *MilestoneSubscriptionEnvelope `json:"subscription,omitempty"`
}

// MilestoneLegTypeWire returns the wire string for a leg type, matching
// the RPC layer's formatMilestoneLegType so the signed envelope and the
// JSON RPC response describe leg types identically.
func MilestoneLegTypeWire(t MilestoneLegType) string {
	switch t {
	case MilestoneLegTypeDeliverable:
		return "deliverable"
	case MilestoneLegTypeTimebox:
		return "timebox"
	default:
		return "unknown"
	}
}

func buildMilestoneCreateEnvelope(project *MilestoneProject) ([]byte, error) {
	if project == nil {
		return nil, fmt.Errorf("escrow: milestone project required")
	}
	envelope := MilestoneCreateEnvelope{
		Action: MilestoneActionCreate,
		Payer:  hex.EncodeToString(project.Payer[:]),
		Payee:  hex.EncodeToString(project.Payee[:]),
		Realm:  project.RealmID,
	}
	if len(project.Metadata) > 0 {
		envelope.Meta = "0x" + hex.EncodeToString(project.Metadata)
	}
	for _, leg := range project.Legs {
		if leg == nil {
			continue
		}
		amount := "0"
		if leg.Amount != nil {
			amount = leg.Amount.String()
		}
		envelope.Legs = append(envelope.Legs, MilestoneLegEnvelope{
			ID:       leg.ID,
			Type:     MilestoneLegTypeWire(leg.Type),
			Token:    leg.Token,
			Amount:   amount,
			Deadline: leg.Deadline,
		})
	}
	if project.Subscription != nil {
		envelope.Subscription = &MilestoneSubscriptionEnvelope{
			IntervalSeconds: project.Subscription.IntervalSeconds,
			NextReleaseAt:   project.Subscription.NextReleaseAt,
			Active:          project.Subscription.Active,
		}
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("escrow: build milestone create envelope: %w", err)
	}
	return payload, nil
}

// RecoverMilestoneCreateSigner reconstructs the canonical
// MilestoneCreateEnvelope from project's own fields (the same values that
// will be persisted if this call is authorized) and recovers the address
// that produced signature over it. The caller (core.Node) must require
// this to equal project.Payer -- never trust project.Payer on its own.
func RecoverMilestoneCreateSigner(project *MilestoneProject, signature []byte) ([20]byte, error) {
	payload, err := buildMilestoneCreateEnvelope(project)
	if err != nil {
		return [20]byte{}, err
	}
	return recoverMilestoneSigner(payload, signature)
}

// SignMilestoneCreateEnvelope signs project's canonical create envelope
// with priv. Exported for tests and reference Go clients constructing
// the wire signature -- the actual authorization check
// (RecoverMilestoneCreateSigner) never calls this.
func SignMilestoneCreateEnvelope(project *MilestoneProject, priv *ecdsa.PrivateKey) ([]byte, error) {
	payload, err := buildMilestoneCreateEnvelope(project)
	if err != nil {
		return nil, err
	}
	return signMilestonePayload(payload, priv)
}

func recoverMilestoneSigner(payload []byte, signature []byte) ([20]byte, error) {
	digestHash := ethcrypto.Keccak256Hash(payload)
	var digest [32]byte
	copy(digest[:], digestHash[:])
	return RecoverSigner(digest, signature)
}

func signMilestonePayload(payload []byte, priv *ecdsa.PrivateKey) ([]byte, error) {
	if priv == nil {
		return nil, fmt.Errorf("escrow: signing key required")
	}
	digestHash := ethcrypto.Keccak256Hash(payload)
	var digest [32]byte
	copy(digest[:], digestHash[:])
	return ethcrypto.Sign(digest[:], priv)
}
