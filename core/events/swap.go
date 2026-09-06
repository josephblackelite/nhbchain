package events

import (
	"math/big"
	"strconv"
	"strings"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	// TypeSwapMinted is emitted whenever a swap-backed mint settles on-chain.
	TypeSwapMinted = "swap.minted"
	// TypeSwapMintProof references the oracle artifacts persisted alongside a mint.
	TypeSwapMintProof = "swap.mint.proof"
	// TypeSwapAlertLimitHit indicates that a voucher submission triggered a configured limit.
	TypeSwapAlertLimitHit = "swap.alert.limit_hit"
	// TypeSwapAlertSanction indicates that a voucher recipient failed the sanctions hook.
	TypeSwapAlertSanction = "swap.alert.sanction"
	// TypeSwapAlertVelocity indicates that a voucher breached the mint velocity guardrail.
	TypeSwapAlertVelocity = "swap.alert.velocity"
	// TypeSwapBurnRecorded captures a burn receipt produced by the off-ramp.
	TypeSwapBurnRecorded = "swap.burn.recorded"
	// TypeSwapTreasuryReconciled is emitted when vouchers are marked as reconciled against treasury records.
	TypeSwapTreasuryReconciled = "swap.treasury.reconciled"
	// TypeSwapRedeemProof references the oracle proofs associated with redeemed vouchers.
	TypeSwapRedeemProof = "swap.redeem.proof"
	// TypeBuyZNHBRecorded is emitted when a user purchases ZNHB from the admin wallet using NHB.
	TypeBuyZNHBRecorded = "swap.buyznhb.recorded"
	// TypeRedeemNHBRequested is emitted when a user burns NHB to request an
	// off-chain stablecoin payout (swap-out).
	TypeRedeemNHBRequested = "swap.redeem.requested"
	// TypeRedemptionAttested is emitted when an authorized attestor confirms
	// or fails a pending redemption's off-chain payout.
	TypeRedemptionAttested = "swap.redeem.attested"
	// TypeRedemptionRefunded is emitted when a failed redemption's burned
	// NHB is credited back to the original redeemer.
	TypeRedemptionRefunded = "swap.redeem.refunded"
)

// RedeemNHBRequested captures a user-initiated NHB burn requesting an
// off-chain stablecoin payout. The NHB is destroyed immediately and
// irreversibly on-chain the moment this is recorded -- it is not held in
// escrow pending payout confirmation.
type RedeemNHBRequested struct {
	RequestID          string
	Account            [20]byte
	NHBAmount          *big.Int
	DestinationAsset   string
	DestinationAddress string
}

// EventType returns the canonical event identifier.
func (RedeemNHBRequested) EventType() string { return TypeRedeemNHBRequested }

// Event renders the redemption-request event payload.
func (r RedeemNHBRequested) Event() *types.Event {
	amount := big.NewInt(0)
	if r.NHBAmount != nil {
		amount = new(big.Int).Set(r.NHBAmount)
	}
	attrs := map[string]string{
		"requestId":          strings.TrimSpace(r.RequestID),
		"nhbAmount":          amount.String(),
		"destinationAsset":   strings.TrimSpace(r.DestinationAsset),
		"destinationAddress": strings.TrimSpace(r.DestinationAddress),
	}
	if r.Account != ([20]byte{}) {
		attrs["account"] = crypto.MustNewAddress(crypto.NHBPrefix, r.Account[:]).String()
	}
	return &types.Event{Type: TypeRedeemNHBRequested, Attributes: attrs}
}

// RedemptionAttested captures an authorized attestor's confirmation or
// failure report for a pending redemption.
type RedemptionAttested struct {
	RequestID       string
	Attestor        [20]byte
	Status          string
	PayoutReference string
	FailureReason   string
}

// EventType returns the canonical event identifier.
func (RedemptionAttested) EventType() string { return TypeRedemptionAttested }

// Event renders the redemption-attestation event payload.
func (r RedemptionAttested) Event() *types.Event {
	attrs := map[string]string{
		"requestId": strings.TrimSpace(r.RequestID),
		"status":    strings.TrimSpace(r.Status),
	}
	if r.Attestor != ([20]byte{}) {
		attrs["attestor"] = crypto.MustNewAddress(crypto.NHBPrefix, r.Attestor[:]).String()
	}
	if trimmed := strings.TrimSpace(r.PayoutReference); trimmed != "" {
		attrs["payoutReference"] = trimmed
	}
	if trimmed := strings.TrimSpace(r.FailureReason); trimmed != "" {
		attrs["failureReason"] = trimmed
	}
	return &types.Event{Type: TypeRedemptionAttested, Attributes: attrs}
}

// RedemptionRefunded captures a failed redemption's NHB burn being reversed:
// the exact amount originally burned (read from the immutable, on-chain
// StoredRedemptionRequest, never attacker/attestor-suppliable) is credited
// back to the original redeemer's balance.
type RedemptionRefunded struct {
	RequestID string
	Account   [20]byte
	NHBAmount *big.Int
}

// EventType returns the canonical event identifier.
func (RedemptionRefunded) EventType() string { return TypeRedemptionRefunded }

// Event renders the redemption-refund event payload.
func (r RedemptionRefunded) Event() *types.Event {
	amount := big.NewInt(0)
	if r.NHBAmount != nil {
		amount = new(big.Int).Set(r.NHBAmount)
	}
	attrs := map[string]string{
		"requestId": strings.TrimSpace(r.RequestID),
		"nhbAmount": amount.String(),
	}
	if r.Account != ([20]byte{}) {
		attrs["account"] = crypto.MustNewAddress(crypto.NHBPrefix, r.Account[:]).String()
	}
	return &types.Event{Type: TypeRedemptionRefunded, Attributes: attrs}
}

// BuyZNHBRecorded captures an admin-wallet-mediated ZNHB purchase: NHB moves
// buyer->admin, ZNHB moves admin->buyer. Always one-directional -- there is
// no on-chain path back from ZNHB to NHB.
type BuyZNHBRecorded struct {
	Buyer      [20]byte
	AdminAddr  [20]byte
	NHBAmount  *big.Int
	ZNHBAmount *big.Int
}

// EventType returns the canonical event identifier.
func (BuyZNHBRecorded) EventType() string { return TypeBuyZNHBRecorded }

// Event renders the ZNHB purchase event payload.
func (b BuyZNHBRecorded) Event() *types.Event {
	nhbAmount := big.NewInt(0)
	if b.NHBAmount != nil {
		nhbAmount = new(big.Int).Set(b.NHBAmount)
	}
	znhbAmount := big.NewInt(0)
	if b.ZNHBAmount != nil {
		znhbAmount = new(big.Int).Set(b.ZNHBAmount)
	}
	attrs := map[string]string{
		"nhbAmount":  nhbAmount.String(),
		"znhbAmount": znhbAmount.String(),
	}
	if b.Buyer != ([20]byte{}) {
		attrs["buyer"] = crypto.MustNewAddress(crypto.NHBPrefix, b.Buyer[:]).String()
	}
	if b.AdminAddr != ([20]byte{}) {
		attrs["admin"] = crypto.MustNewAddress(crypto.NHBPrefix, b.AdminAddr[:]).String()
	}
	return &types.Event{Type: TypeBuyZNHBRecorded, Attributes: attrs}
}

type SwapMinted struct {
	OrderID    string
	Recipient  [20]byte
	Amount     *big.Int
	Fiat       string
	FiatAmount string
	Rate       string
}

func (SwapMinted) EventType() string { return TypeSwapMinted }

func (e SwapMinted) Event() *types.Event {
	amount := big.NewInt(0)
	if e.Amount != nil {
		amount = new(big.Int).Set(e.Amount)
	}
	recipient := ""
	if e.Recipient != ([20]byte{}) {
		recipient = crypto.MustNewAddress(crypto.NHBPrefix, e.Recipient[:]).String()
	}
	return &types.Event{
		Type: TypeSwapMinted,
		Attributes: map[string]string{
			"orderId":    strings.TrimSpace(e.OrderID),
			"recipient":  recipient,
			"amount":     amount.String(),
			"fiat":       strings.TrimSpace(e.Fiat),
			"fiatAmount": strings.TrimSpace(e.FiatAmount),
			"rate":       strings.TrimSpace(e.Rate),
		},
	}
}

// SwapMintProof captures the oracle metadata persisted for a minted voucher.
type SwapMintProof struct {
	ProviderTxID      string
	OrderID           string
	Token             string
	PriceProofID      string
	OracleSource      string
	OracleMedian      string
	OracleFeeders     []string
	QuoteTimestamp    int64
	TwapRate          string
	TwapObservations  int
	TwapWindowSeconds int64
	TwapStart         int64
	TwapEnd           int64
}

// EventType returns the mint proof event identifier.
func (SwapMintProof) EventType() string { return TypeSwapMintProof }

// Event renders the mint proof event payload.
func (p SwapMintProof) Event() *types.Event {
	attrs := map[string]string{}
	if trimmed := strings.TrimSpace(p.ProviderTxID); trimmed != "" {
		attrs["providerTxId"] = trimmed
	}
	if trimmed := strings.TrimSpace(p.OrderID); trimmed != "" {
		attrs["orderId"] = trimmed
	}
	if trimmed := strings.TrimSpace(p.Token); trimmed != "" {
		attrs["token"] = trimmed
	}
	if trimmed := strings.TrimSpace(p.PriceProofID); trimmed != "" {
		attrs["priceProofId"] = trimmed
	}
	if trimmed := strings.TrimSpace(p.OracleSource); trimmed != "" {
		attrs["source"] = trimmed
	}
	if trimmed := strings.TrimSpace(p.OracleMedian); trimmed != "" {
		attrs["oracleMedian"] = trimmed
	}
	if len(p.OracleFeeders) > 0 {
		attrs["oracleFeeders"] = strings.Join(p.OracleFeeders, ",")
	}
	if p.QuoteTimestamp > 0 {
		attrs["quoteTs"] = strconv.FormatInt(p.QuoteTimestamp, 10)
	}
	if strings.TrimSpace(p.TwapRate) != "" {
		attrs["twapRate"] = strings.TrimSpace(p.TwapRate)
	}
	attrs["twapObservations"] = strconv.Itoa(p.TwapObservations)
	attrs["twapWindowSeconds"] = strconv.FormatInt(p.TwapWindowSeconds, 10)
	if p.TwapStart > 0 {
		attrs["twapStart"] = strconv.FormatInt(p.TwapStart, 10)
	}
	if p.TwapEnd > 0 {
		attrs["twapEnd"] = strconv.FormatInt(p.TwapEnd, 10)
	}
	return &types.Event{Type: TypeSwapMintProof, Attributes: attrs}
}

// SwapLimitAlert captures metadata describing a limit violation.
type SwapLimitAlert struct {
	Address      [20]byte
	Provider     string
	ProviderTxID string
	Limit        string
	Amount       *big.Int
	LimitValue   *big.Int
	CurrentValue *big.Int
}

// EventType returns the canonical event type string.
func (SwapLimitAlert) EventType() string { return TypeSwapAlertLimitHit }

// Event renders the event payload for downstream consumers.
func (a SwapLimitAlert) Event() *types.Event {
	attrs := map[string]string{
		"provider":     strings.TrimSpace(a.Provider),
		"providerTxId": strings.TrimSpace(a.ProviderTxID),
		"limit":        strings.TrimSpace(a.Limit),
	}
	if a.Address != ([20]byte{}) {
		attrs["address"] = crypto.MustNewAddress(crypto.NHBPrefix, a.Address[:]).String()
	}
	amount := big.NewInt(0)
	if a.Amount != nil {
		amount = new(big.Int).Set(a.Amount)
	}
	attrs["amount"] = amount.String()
	if a.LimitValue != nil {
		attrs["limitValue"] = new(big.Int).Set(a.LimitValue).String()
	}
	if a.CurrentValue != nil {
		attrs["currentValue"] = new(big.Int).Set(a.CurrentValue).String()
	}
	return &types.Event{Type: TypeSwapAlertLimitHit, Attributes: attrs}
}

// SwapSanctionAlert indicates a sanctions failure for a voucher recipient.
type SwapSanctionAlert struct {
	Address      [20]byte
	Provider     string
	ProviderTxID string
}

// EventType returns the canonical event type string.
func (SwapSanctionAlert) EventType() string { return TypeSwapAlertSanction }

// Event renders the sanction alert payload.
func (a SwapSanctionAlert) Event() *types.Event {
	attrs := map[string]string{
		"provider":     strings.TrimSpace(a.Provider),
		"providerTxId": strings.TrimSpace(a.ProviderTxID),
	}
	if a.Address != ([20]byte{}) {
		attrs["address"] = crypto.MustNewAddress(crypto.NHBPrefix, a.Address[:]).String()
	}
	return &types.Event{Type: TypeSwapAlertSanction, Attributes: attrs}
}

// SwapVelocityAlert records a velocity guardrail violation.
type SwapVelocityAlert struct {
	Address       [20]byte
	Provider      string
	ProviderTxID  string
	WindowSeconds uint64
	ObservedCount int
	AllowedMints  uint64
}

// EventType returns the canonical event type string.
func (SwapVelocityAlert) EventType() string { return TypeSwapAlertVelocity }

// Event renders the velocity alert payload.
func (a SwapVelocityAlert) Event() *types.Event {
	attrs := map[string]string{
		"provider":      strings.TrimSpace(a.Provider),
		"providerTxId":  strings.TrimSpace(a.ProviderTxID),
		"windowSeconds": strconv.FormatUint(a.WindowSeconds, 10),
		"allowedMints":  strconv.FormatUint(a.AllowedMints, 10),
		"observedCount": strconv.Itoa(a.ObservedCount),
	}
	if a.Address != ([20]byte{}) {
		attrs["address"] = crypto.MustNewAddress(crypto.NHBPrefix, a.Address[:]).String()
	}
	return &types.Event{Type: TypeSwapAlertVelocity, Attributes: attrs}
}

// SwapBurnRecorded encapsulates metadata describing an off-ramp burn receipt.
type SwapBurnRecorded struct {
	ReceiptID    string
	ProviderTxID string
	Token        string
	Amount       *big.Int
	BurnTx       string
	TreasuryTx   string
	VoucherIDs   []string
	ObservedAt   int64
}

// EventType returns the canonical burn receipt event identifier.
func (SwapBurnRecorded) EventType() string { return TypeSwapBurnRecorded }

// Event renders the burn receipt event for downstream consumers.
func (r SwapBurnRecorded) Event() *types.Event {
	amount := big.NewInt(0)
	if r.Amount != nil {
		amount = new(big.Int).Set(r.Amount)
	}
	attrs := map[string]string{
		"receiptId":    strings.TrimSpace(r.ReceiptID),
		"providerTxId": strings.TrimSpace(r.ProviderTxID),
		"token":        strings.TrimSpace(r.Token),
		"amountWei":    amount.String(),
	}
	if strings.TrimSpace(r.BurnTx) != "" {
		attrs["burnTx"] = strings.TrimSpace(r.BurnTx)
	}
	if strings.TrimSpace(r.TreasuryTx) != "" {
		attrs["treasuryTx"] = strings.TrimSpace(r.TreasuryTx)
	}
	if len(r.VoucherIDs) > 0 {
		attrs["vouchers"] = strings.Join(r.VoucherIDs, ",")
	}
	if r.ObservedAt > 0 {
		attrs["observedAt"] = strconv.FormatInt(r.ObservedAt, 10)
	}
	return &types.Event{Type: TypeSwapBurnRecorded, Attributes: attrs}
}

// SwapRedeemProof references the proofs attached to vouchers covered by a burn receipt.
type SwapRedeemProof struct {
	ReceiptID     string
	ProviderTxID  string
	VoucherIDs    []string
	PriceProofIDs []string
	ObservedAt    int64
}

// EventType returns the redeem proof event identifier.
func (SwapRedeemProof) EventType() string { return TypeSwapRedeemProof }

// Event renders the redeem proof event payload.
func (p SwapRedeemProof) Event() *types.Event {
	attrs := map[string]string{}
	if trimmed := strings.TrimSpace(p.ReceiptID); trimmed != "" {
		attrs["receiptId"] = trimmed
	}
	if trimmed := strings.TrimSpace(p.ProviderTxID); trimmed != "" {
		attrs["providerTxId"] = trimmed
	}
	if len(p.VoucherIDs) > 0 {
		attrs["vouchers"] = strings.Join(p.VoucherIDs, ",")
	}
	if len(p.PriceProofIDs) > 0 {
		attrs["priceProofIds"] = strings.Join(p.PriceProofIDs, ",")
	}
	if p.ObservedAt > 0 {
		attrs["observedAt"] = strconv.FormatInt(p.ObservedAt, 10)
	}
	return &types.Event{Type: TypeSwapRedeemProof, Attributes: attrs}
}

// SwapTreasuryReconciled captures a treasury reconciliation marker for minted vouchers.
type SwapTreasuryReconciled struct {
	VoucherIDs []string
	ReceiptID  string
	ObservedAt int64
}

// EventType returns the canonical reconciliation event identifier.
func (SwapTreasuryReconciled) EventType() string { return TypeSwapTreasuryReconciled }

// Event renders the reconciliation event payload.
func (r SwapTreasuryReconciled) Event() *types.Event {
	if len(r.VoucherIDs) == 0 {
		return nil
	}
	attrs := map[string]string{
		"vouchers": strings.Join(r.VoucherIDs, ","),
	}
	if strings.TrimSpace(r.ReceiptID) != "" {
		attrs["receiptId"] = strings.TrimSpace(r.ReceiptID)
	}
	if r.ObservedAt > 0 {
		attrs["observedAt"] = strconv.FormatInt(r.ObservedAt, 10)
	}
	return &types.Event{Type: TypeSwapTreasuryReconciled, Attributes: attrs}
}
