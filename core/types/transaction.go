package types

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
)

var nhbChainID = big.NewInt(0x4e4842) // ASCII "NHB"

// NHBChainID returns the canonical chain ID for the NHBCoin network.
func NHBChainID() *big.Int {
	return new(big.Int).Set(nhbChainID)
}

// IsValidChainID reports whether the provided chain ID matches the NHBCoin network.
func IsValidChainID(chainID *big.Int) bool {
	if chainID == nil {
		return false
	}
	return chainID.Cmp(nhbChainID) == 0
}

// TxType defines the purpose of a transaction.
type TxType byte

const (
	TxTypeTransfer             TxType = 0x01 // A standard transfer of NHB
	TxTypeRegisterIdentity     TxType = 0x02 // A transaction to claim a username
	TxTypeCreateEscrow         TxType = 0x03 // Create escrow
	TxTypeReleaseEscrow        TxType = 0x04 // NEW: Buyer releases funds to seller
	TxTypeRefundEscrow         TxType = 0x05 // NEW: Seller refunds funds to buyer
	TxTypeStake                TxType = 0x06 // Implenting stake
	TxTypeUnstake              TxType = 0x07 // NEW: A transaction to un-stake ZapNHB
	TxTypeHeartbeat            TxType = 0x08 // Heartbeat from users device
	TxTypeLockEscrow           TxType = 0x09 // NEW: Buyer commits to a purchase
	TxTypeDisputeEscrow        TxType = 0x0A // NEW: Buyer raises a dispute
	TxTypeArbitrateRelease     TxType = 0x0B // NEW: Admin-only action to release to buyer
	TxTypeArbitrateRefund      TxType = 0x0C // NEW: Admin-only action to refund seller
	TxTypeStakeClaim           TxType = 0x0D // NEW: Claim matured unbonded ZapNHB
	TxTypeMint                 TxType = 0x0E // NEW: Execute a signed mint voucher on-chain
	TxTypeSwapPayoutReceipt    TxType = 0x0F // NEW: Record a swap payout receipt attested by the treasury
	TxTypeTransferZNHB         TxType = 0x10 // A standard transfer of ZapNHB (ZNHB)
	TxTypeSwapMint             TxType = 0x11 // Native On-Chain Swap minting NHB
	TxTypeSwapBurn             TxType = 0x12 // Native On-Chain Swap burning NHB
	TxTypeLendingSupplyNHB     TxType = 0x13 // Supply NHB liquidity to a lending pool
	TxTypeLendingWithdrawNHB   TxType = 0x14 // Withdraw NHB liquidity from a lending pool
	TxTypeLendingDepositZNHB   TxType = 0x15 // Deposit ZNHB collateral into a lending pool
	TxTypeLendingWithdrawZNHB  TxType = 0x16 // Withdraw ZNHB collateral from a lending pool
	TxTypeLendingBorrowNHB     TxType = 0x17 // Borrow NHB against ZNHB collateral
	TxTypeLendingRepayNHB      TxType = 0x18 // Repay NHB debt in a lending pool
	TxTypeBuyZNHB              TxType = 0x19 // Purchase ZNHB from the admin wallet using NHB (one-directional, never reversed on-chain)
	TxTypeSetRewardBeneficiary TxType = 0x1A // Validator redirects its own epoch reward payouts to a chosen wallet
	TxTypeRedeemNHB            TxType = 0x1B // User burns NHB to request an off-chain stablecoin payout (swap-out)
	TxTypeAttestRedemption     TxType = 0x1C // Authorized attestor confirms or fails a pending redemption's off-chain payout
	TxTypeLendingLiquidate     TxType = 0x1D // Liquidator repays a borrower's unhealthy debt for a discounted share of their collateral (permissionless third-party action, not signed by the borrower)
	// TxTypeSwapVoucherMint executes a fiat-gateway-attested ZNHB mint voucher
	// as a signed, network-wide-agreed on-chain transaction (mempool ->
	// gossip -> ApplyTransaction -> consensus), replacing the old
	// Node.SwapSubmitVoucher direct state-trie write whose duplicate check
	// only ever saw one validator's own local state. Senderless/envelope-
	// unsigned like TxTypeMint: the payload carries its own MintAuthority
	// signature over the voucher, so no separate envelope signature is
	// required. 0x1E is the next free byte after 0x1D
	// (TxTypeLendingLiquidate) -- verified against this file's real, current
	// tip: 0x19-0x1D are taken by TxTypeBuyZNHB/TxTypeSetRewardBeneficiary/
	// TxTypeRedeemNHB/TxTypeAttestRedemption/TxTypeLendingLiquidate, and
	// 0x20+ by the POS types below. Do not reuse without re-checking this
	// file's current tip for newly added types.
	TxTypeSwapVoucherMint TxType = 0x1E
	TxTypePOSAuthorize    TxType = 0x20 // Pre-authorize a merchant payment
	TxTypePOSCapture      TxType = 0x21 // Capture an authorized payment
	TxTypePOSVoid         TxType = 0x22 // Void authorized payment
	TxTypePOSRegistry     TxType = 0x23 // POS merchant/device registry update
	// TxTypeBuybackAsk lets a ZNHB holder offer ZNHB for sale into the
	// treasury's per-epoch buyback (core/tokenomics/buyback). A market ask,
	// not a priced bid -- filled pro-rata under oversubscription at the
	// epoch's independently-computed max price (core/buyback_settlement.go),
	// never at a seller-chosen price. 0x24 is the next free byte after
	// TxTypePOSRegistry (0x23).
	TxTypeBuybackAsk TxType = 0x24
	// TxTypeBuybackRefPrice submits an M-of-N-signed independent market
	// reference price for the buyback's safety-margin check
	// (core/tokenomics/buyback.VerifyReferencePrice). Senderless/envelope-
	// unsigned like TxTypeSwapVoucherMint -- the payload carries its own
	// signature bundle, verified against the genesis-immutable signer set,
	// not a single envelope signature. 0x25 is the next free byte after
	// TxTypeBuybackAsk (0x24).
	TxTypeBuybackRefPrice TxType = 0x25
	// TxTypeLendingRefPrice submits an M-of-N-signed independent ZNHB/NHB
	// market reference price into every configured lending market's
	// Market.OracleMedianWei (core/lending_tx.go's
	// applyLendingRefPriceTransaction), mirroring TxTypeBuybackRefPrice's
	// senderless/signature-bundle shape but domain-separated
	// (NHB_LENDING_REFPRICE_V1) and, unlike buyback's ref price, not
	// epoch-gated -- lending's oracle price may be refreshed as often as
	// desired, bounded only by guardOracle's staleness/deviation checks on
	// the read side. 0x26 is the next free byte after TxTypeBuybackRefPrice
	// (0x25) -- verified against this file's real, current tip; do not
	// reuse without re-checking for newly added types above this comment.
	TxTypeLendingRefPrice TxType = 0x26
	// TxTypeGovPropose/Vote/Finalize/Queue/Execute replace the old
	// Node.GovernancePropose/Vote/Finalize/Queue/Execute direct-state-trie
	// writes (core/node.go, removed) with real signed, gossiped,
	// consensus-routed transactions -- the same fix pattern as
	// TxTypeSwapVoucherMint's own predecessor bug. Unlike the senderless
	// reference-price types above, these DO require a real envelope
	// signature (RequiresSignature's default, not added to the exemption
	// switch below): the proposer/voter's identity must be the
	// cryptographically-recovered tx.From(), never a client-supplied
	// payload field -- closing the previously wide-open hole where any RPC
	// caller could submit a proposal or cast a vote "as" any address on the
	// network with zero proof of private-key possession. 0x27 is the next
	// free byte after TxTypeLendingRefPrice (0x26) -- verified against this
	// file's real, current tip; do not reuse without re-checking for newly
	// added types above this comment. (0x1F remains an unexplained
	// historical gap between TxTypeSwapVoucherMint and the POS types --
	// left alone rather than reused here.)
	TxTypeGovPropose  TxType = 0x27
	TxTypeGovVote     TxType = 0x28
	TxTypeGovFinalize TxType = 0x29
	TxTypeGovQueue    TxType = 0x2A
	TxTypeGovExecute  TxType = 0x2B
	// TxTypeLendingCreatePool replaces the old LendingModule.CreatePool
	// direct-state-trie write (rpc/modules/lending.go, via
	// Node.WithState -- see core/lending_native.go's
	// applyLendingCreatePoolTransaction doc comment) with a real signed,
	// consensus-routed transaction. The pool's DeveloperOwner is always the
	// recovered tx signer, never a client-supplied developerOwner field --
	// the old RPC method let any authenticated caller name an arbitrary
	// owner address for a newly created pool with no proof of key
	// possession, the same class of bug the governance fix
	// (TxTypeGovPropose etc.) closed. 0x2C is the next free byte after
	// TxTypeGovExecute (0x2B) -- verified against this file's real,
	// current tip; do not reuse without re-checking for newly added types
	// above this comment.
	TxTypeLendingCreatePool TxType = 0x2C
	// TxTypePotsoStakeLock/Unbond/Withdraw replace the old
	// Node.PotsoStakeLock/Unbond/Withdraw direct-state-trie writes
	// (core/node.go, removed) with real signed, consensus-routed
	// transactions -- see core/potso_stake_tx.go's doc comment for the
	// full rationale, including why the bespoke sha256/secp256k1
	// signature + authNonce scheme the old RPC methods used
	// (rpc/potso_stake_handlers.go, removed) is now fully redundant with
	// the standard envelope signature (tx.From()) and standard account
	// nonce. The owner of a lock/unbond/withdraw is always tx.From(),
	// never a payload field. 0x2D is the next free byte after
	// TxTypeLendingCreatePool (0x2C) -- verified against this file's
	// real, current tip; do not reuse without re-checking for newly added
	// types above this comment.
	TxTypePotsoStakeLock     TxType = 0x2D
	TxTypePotsoStakeUnbond   TxType = 0x2E
	TxTypePotsoStakeWithdraw TxType = 0x2F
	// TxTypeSubscriptionCreatePlan/UpdatePlan/Subscribe/Cancel implement
	// native/subscriptions: a Stripe-like recurring-billing engine where a
	// merchant defines a Plan (price/asset/interval) and a payer's single
	// signed TxTypeSubscriptionSubscribe transaction becomes their standing
	// authorization for the chain to debit that exact, bounded amount every
	// cycle -- with zero further signature required at charge time (see
	// core/subscriptions_settlement.go's doc comment for why this is safe:
	// the debited amount is fixed and disclosed up front, never
	// open-ended, matching the "bounded standing authorization" discipline
	// core/rewards_logic.go and core/buyback_settlement.go already
	// establish for every other system-initiated debit on this chain).
	// All four types require a real envelope signature (RequiresSignature's
	// default, not added to the exemption switch below) -- Subscribe's
	// signature specifically IS the mandate, so it must be the
	// cryptographically-recovered tx.From(), never a client-supplied
	// payload field. 0x30 is the next free byte after
	// TxTypePotsoStakeWithdraw (0x2F) -- verified against this file's real,
	// current tip; do not reuse without re-checking for newly added types
	// above this comment.
	TxTypeSubscriptionCreatePlan TxType = 0x30
	TxTypeSubscriptionUpdatePlan TxType = 0x31
	TxTypeSubscriptionSubscribe  TxType = 0x32
	TxTypeSubscriptionCancel     TxType = 0x33
	// TxTypeStakeClaimRewards replaces the old rpc/stake_handlers.go
	// handleStakeClaimRewards direct-state-trie write (it called
	// Node.StakeClaimRewards -> StateProcessor.StakeClaimRewards under
	// n.stateMu.Lock() completely outside CreateBlock/ApplyTransaction/
	// ValidateBlock) with a real signed, consensus-routed transaction -- the
	// same fix pattern as CreatePool/governance/POTSO-stake before it. This
	// is a genuinely different action from TxTypeStakeClaim (0x0D, which
	// releases already-matured unbonded principal via sp.StakeClaim and an
	// unbondingId payload): TxTypeStakeClaimRewards pays out accrued
	// APR-based staking rewards via sp.StakeClaimRewards, operating on
	// StakeLastIndex/StakeLastPayoutTs/StakingRewards/StakingGlobalIndex/
	// StakingEmissionYTD and the staking treasury -- disjoint state fields,
	// zero code-path overlap with StakeClaim. It takes no payload: unlike
	// StakeClaim's unbondingId, rewards claiming operates purely on the
	// signer's own account and the current block timestamp, so tx.Data is
	// unused. The claimant is always tx.From(), never a client-supplied
	// address parameter. 0x34 is the next free byte after
	// TxTypeSubscriptionCancel (0x33) -- verified against this file's real,
	// current tip; do not reuse without re-checking for newly added types
	// above this comment.
	TxTypeStakeClaimRewards TxType = 0x34

	// TxTypeMarketCreateListing/FillListing/CancelListing implement the
	// peer-to-peer ZNHB-for-NHB marketplace (native/market). 0x35/0x36/0x37
	// are the next free bytes after TxTypeStakeClaimRewards (0x34) --
	// verified 2026-08-24 against this file's real, current tip (an earlier
	// draft of this feature mistakenly assumed 0x30-0x32 were free; they
	// are not, TxTypeSubscription* already occupies them). Do not reuse
	// without re-checking for newly added types above this comment.
	//
	// CreateListing: tx.Value is the ZNHB amount to escrow; tx.Data is
	// RLP([rateNumerator, rateDenominator, allowPartial]) -- price
	// expressed as an exact ZNHB-per-NHB rational, never a float.
	// FillListing: tx.Value is unused (always 0); tx.Data is
	// RLP([listingID, znhbAmountRequested]) -- the NHB cost is computed
	// on-chain from the listing's own rate, never client-declared.
	// CancelListing: tx.Value is unused; tx.Data is RLP([listingID]).
	TxTypeMarketCreateListing TxType = 0x35
	TxTypeMarketFillListing   TxType = 0x36
	TxTypeMarketCancelListing TxType = 0x37

	// TxTypeLendingBorrowFixedTerm originates a new locked-rate, fixed-tenure
	// loan. tx.Value is the principal requested; tx.Data is JSON-encoded
	// lendingFixedTermBorrowPayload{poolId, tenureDays} (matching every
	// other lending tx's JSON payload convention, not RLP) -- the rate is
	// resolved on-chain from the tenure/rate schedule, never client-declared.
	TxTypeLendingBorrowFixedTerm TxType = 0x38
	// TxTypeLendingRepayFixedTerm applies a payment to the sender's active
	// fixed-term loan. tx.Value is the amount offered (capped at the loan's
	// remaining outstanding balance); tx.Data is JSON-encoded
	// lendingNativePayload{poolId}.
	TxTypeLendingRepayFixedTerm TxType = 0x39
	// TxTypeLendingSupplyFixedTerm originates a new locked-rate, fixed-tenure
	// deposit (Milestone 3, the mirror image of TxTypeLendingBorrowFixedTerm
	// on the pool's liability side). tx.Value is the principal deposited;
	// tx.Data is JSON-encoded lendingFixedTermSupplyPayload{poolId,
	// tenureDays, payout} -- the rate is resolved on-chain from the
	// deposit-side tenure/rate schedule, never client-declared.
	TxTypeLendingSupplyFixedTerm TxType = 0x3A
)

// RequiresSignature reports whether the transaction type must carry an
// originator signature that can be recovered via From(). Types that originate
// from module attestations rely on their envelope signatures instead.
func RequiresSignature(t TxType) bool {
	switch t {
	case TxTypeMint, TxTypeSwapVoucherMint, TxTypeBuybackRefPrice, TxTypeLendingRefPrice:
		return false
	default:
		return true
	}
}

// Transaction now has a Type field to distinguish its intent.
// Transaction now supports gas fees and a paymaster.
type Transaction struct {
	ChainID  *big.Int `json:"chainId"`
	Type     TxType   `json:"type"`
	Nonce    uint64   `json:"nonce"`
	To       []byte   `json:"to"`
	Value    *big.Int `json:"value"`
	Data     []byte   `json:"data"`
	GasLimit uint64   `json:"gasLimit"` // The maximum gas the user is willing to pay
	GasPrice *big.Int `json:"gasPrice"` // The price per unit of gas

	MaxBlockHeight uint64 `json:"maxBlockHeight,omitempty"`

	Paymaster []byte `json:"paymaster,omitempty"` // NEW: Address of the gas fee sponsor

	IntentRef       []byte `json:"intentRef,omitempty"`
	IntentExpiry    uint64 `json:"intentExpiry,omitempty"`
	MerchantAddress string `json:"merchantAddr,omitempty"`
	DeviceID        string `json:"deviceId,omitempty"`
	RefundOf        string `json:"refundOf,omitempty"`

	// Signatures
	R          *big.Int `json:"r"` // Sender's signature
	S          *big.Int `json:"s"`
	V          *big.Int `json:"v"`
	PaymasterR *big.Int `json:"paymasterR,omitempty"` // NEW: Paymaster's signature
	PaymasterS *big.Int `json:"paymasterS,omitempty"`
	PaymasterV *big.Int `json:"paymasterV,omitempty"`

	from []byte
}

var (
	ErrPaymasterSignatureMissing = errors.New("transaction missing paymaster signature")
	ErrPaymasterSignatureInvalid = errors.New("invalid paymaster signature")

	// secp256k1HalfN is half the secp256k1 curve order, used for signature malleability checks
	secp256k1HalfN = new(big.Int).Div(crypto.S256().Params().N, big.NewInt(2))
)

const maxIntentRefLength = 1024

func paddedAddress20(addr []byte, field string) ([]byte, error) {
	if len(addr) == 0 {
		return make([]byte, 20), nil
	}
	if len(addr) > 20 {
		return nil, fmt.Errorf("%s length must not exceed 20 bytes", field)
	}
	padded := make([]byte, 20)
	copy(padded[20-len(addr):], addr)
	return padded, nil
}

func writeBytes(buf *bytes.Buffer, data []byte) error {
	if len(data) > math.MaxUint16 {
		return fmt.Errorf("byte payload length %d exceeds max %d", len(data), math.MaxUint16)
	}
	binary.Write(buf, binary.BigEndian, uint16(len(data)))
	buf.Write(data)
	return nil
}

// ValidateBasic performs non-stateful transaction shape validation.
func (tx *Transaction) ValidateBasic() error {
	if tx == nil {
		return fmt.Errorf("transaction required")
	}
	if len(tx.To) > 20 {
		return fmt.Errorf("to length must not exceed 20 bytes")
	}
	if len(tx.Paymaster) > 20 {
		return fmt.Errorf("paymaster length must not exceed 20 bytes")
	}
	if len(tx.IntentRef) > maxIntentRefLength {
		return fmt.Errorf("intentRef length must not exceed %d bytes", maxIntentRefLength)
	}
	for name, value := range map[string]*big.Int{
		"value":      tx.Value,
		"gasPrice":   tx.GasPrice,
		"r":          tx.R,
		"s":          tx.S,
		"v":          tx.V,
		"paymasterR": tx.PaymasterR,
		"paymasterS": tx.PaymasterS,
		"paymasterV": tx.PaymasterV,
	} {
		if value != nil && value.Sign() < 0 {
			return fmt.Errorf("%s must not be negative", name)
		}
	}
	// R/S (and PaymasterR/PaymasterS) are secp256k1 signature components and
	// must never exceed the curve's 32-byte field size. From()/PaymasterSponsor()
	// both do copy(sig[32-len(x.Bytes()):32], x.Bytes()) with no length check
	// of their own -- an oversized value (e.g. len(R.Bytes()) > 32, trivially
	// reachable from a single malformed P2P transaction message) produces a
	// NEGATIVE slice index there, which panics rather than errors. That panic
	// is never recovered anywhere in the P2P message-receive path
	// (p2p/peer.go's per-peer readLoop goroutine calls HandleMessage directly,
	// no defer/recover in the whole file), so an unauthenticated remote peer
	// could crash the entire process with one crafted transaction. Rejecting
	// oversized R/S/PaymasterR/PaymasterS here, before Hash()/From() ever run
	// (Hash() already calls ValidateBasic() first), closes it at the source.
	for name, value := range map[string]*big.Int{
		"r":          tx.R,
		"s":          tx.S,
		"paymasterR": tx.PaymasterR,
		"paymasterS": tx.PaymasterS,
	} {
		if value != nil && len(value.Bytes()) > 32 {
			return fmt.Errorf("%s must not exceed 32 bytes", name)
		}
	}
	return nil
}

// writeString securely encodes strings for binary hashing.
func writeString(buf *bytes.Buffer, s string) {
	strBytes := []byte(strings.TrimSpace(s))
	binary.Write(buf, binary.BigEndian, uint32(len(strBytes)))
	buf.Write(strBytes)
}

// Hash logic must now include the new Type field.
func (tx *Transaction) Hash() ([]byte, error) {
	if err := tx.ValidateBasic(); err != nil {
		return nil, err
	}
	if tx.Type > 0 {
		// V3 Canonical Binary Encoding for Native Types
		buf := new(bytes.Buffer)
		buf.WriteString("NHB_TX_V3_MAINNET")

		if tx.ChainID != nil {
			binary.Write(buf, binary.BigEndian, tx.ChainID.Uint64())
		} else {
			binary.Write(buf, binary.BigEndian, uint64(0))
		}

		binary.Write(buf, binary.BigEndian, uint8(tx.Type))
		binary.Write(buf, binary.BigEndian, tx.Nonce)
		binary.Write(buf, binary.BigEndian, tx.MaxBlockHeight)
		binary.Write(buf, binary.BigEndian, tx.IntentExpiry)
		if err := writeBytes(buf, tx.IntentRef); err != nil {
			return nil, fmt.Errorf("intentRef: %w", err)
		}

		// To Address
		toPad, err := paddedAddress20(tx.To, "to")
		if err != nil {
			return nil, err
		}
		buf.Write(toPad)

		// Value
		if tx.Value != nil {
			valBytes := tx.Value.Bytes()
			binary.Write(buf, binary.BigEndian, uint16(len(valBytes)))
			buf.Write(valBytes)
		} else {
			binary.Write(buf, binary.BigEndian, uint16(0))
		}

		// Data Payload
		binary.Write(buf, binary.BigEndian, uint32(len(tx.Data)))
		buf.Write(tx.Data)

		// Fees
		binary.Write(buf, binary.BigEndian, tx.GasLimit)
		if tx.GasPrice != nil {
			gpBytes := tx.GasPrice.Bytes()
			binary.Write(buf, binary.BigEndian, uint16(len(gpBytes)))
			buf.Write(gpBytes)
		} else {
			binary.Write(buf, binary.BigEndian, uint16(0))
		}

		// Optional fields
		if len(tx.Paymaster) > 0 {
			buf.Write([]byte{1})
			pmPad, err := paddedAddress20(tx.Paymaster, "paymaster")
			if err != nil {
				return nil, err
			}
			buf.Write(pmPad)
		} else {
			buf.Write([]byte{0})
		}

		writeString(buf, tx.MerchantAddress)
		writeString(buf, tx.DeviceID)
		writeString(buf, tx.RefundOf)

		hash := sha256.Sum256(buf.Bytes())
		return hash[:], nil
	}

	// Legacy V2 / EVM JSON fallback
	txData := struct {
		ChainID        *big.Int
		Type           TxType
		Nonce          uint64
		MaxBlockHeight uint64
		To             []byte
		Value          *big.Int
		Data           []byte
		GasLimit       uint64
		GasPrice       *big.Int
		Paymaster      []byte `json:"paymaster,omitempty"`
		IntentRef      []byte `json:"intentRef,omitempty"`
		IntentExpiry   uint64 `json:"intentExpiry,omitempty"`
		MerchantAddr   string `json:"merchantAddr,omitempty"`
		DeviceID       string `json:"deviceId,omitempty"`
		RefundOf       string `json:"refundOf,omitempty"`
	}{ChainID: tx.ChainID, Type: tx.Type, Nonce: tx.Nonce, MaxBlockHeight: tx.MaxBlockHeight, To: tx.To, Value: tx.Value, Data: tx.Data, GasLimit: tx.GasLimit, GasPrice: tx.GasPrice, IntentExpiry: tx.IntentExpiry, MerchantAddr: tx.MerchantAddress, DeviceID: tx.DeviceID, RefundOf: strings.TrimSpace(tx.RefundOf)}

	if len(tx.Paymaster) > 0 {
		txData.Paymaster = append([]byte(nil), tx.Paymaster...)
	}
	if len(tx.IntentRef) > 0 {
		txData.IntentRef = append([]byte(nil), tx.IntentRef...)
	}

	b, err := json.Marshal(txData)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(b)
	return hash[:], nil
}

// ... (Sign and From methods remain the same)
func (tx *Transaction) Sign(privKey *ecdsa.PrivateKey) error {
	if tx.ChainID == nil {
		return fmt.Errorf("chain id required")
	}
	hash, err := tx.Hash()
	if err != nil {
		return err
	}
	sig, err := crypto.Sign(hash, privKey)
	if err != nil {
		return err
	}
	tx.R = new(big.Int).SetBytes(sig[:32])
	tx.S = new(big.Int).SetBytes(sig[32:64])
	tx.V = new(big.Int).SetBytes([]byte{sig[64] + 27})
	tx.from = nil
	return nil
}

func (tx *Transaction) From() ([]byte, error) {
	if tx.from != nil {
		return tx.from, nil
	}
	if tx.R == nil || tx.S == nil || tx.V == nil {
		return nil, fmt.Errorf("transaction missing signature")
	}
	if tx.S.Cmp(secp256k1HalfN) > 0 {
		return nil, fmt.Errorf("invalid signature: S > secp256k1n/2 (malleability protection)")
	}
	hash, err := tx.Hash()
	if err != nil {
		return nil, err
	}
	sig := make([]byte, 65)
	copy(sig[32-len(tx.R.Bytes()):32], tx.R.Bytes())
	copy(sig[64-len(tx.S.Bytes()):64], tx.S.Bytes())
	sig[64] = byte(tx.V.Uint64() - 27)
	pubKey, err := crypto.SigToPub(hash, sig)
	if err != nil {
		return nil, err
	}
	tx.from = crypto.PubkeyToAddress(*pubKey).Bytes()
	return tx.from, nil
}

// PaymasterSponsor recovers the sponsoring paymaster address from the
// associated signature. A nil result with no error indicates the transaction
// does not request sponsorship.
func (tx *Transaction) PaymasterSponsor() ([]byte, error) {
	if len(tx.Paymaster) == 0 {
		return nil, nil
	}
	if tx.PaymasterR == nil || tx.PaymasterS == nil || tx.PaymasterV == nil {
		return nil, ErrPaymasterSignatureMissing
	}
	if tx.PaymasterS.Cmp(secp256k1HalfN) > 0 {
		return nil, ErrPaymasterSignatureInvalid
	}
	hash, err := tx.Hash()
	if err != nil {
		return nil, err
	}
	sig := make([]byte, 65)
	copy(sig[32-len(tx.PaymasterR.Bytes()):32], tx.PaymasterR.Bytes())
	copy(sig[64-len(tx.PaymasterS.Bytes()):64], tx.PaymasterS.Bytes())
	sig[64] = byte(tx.PaymasterV.Uint64() - 27)
	pubKey, err := crypto.SigToPub(hash, sig)
	if err != nil {
		return nil, ErrPaymasterSignatureInvalid
	}
	recovered := crypto.PubkeyToAddress(*pubKey).Bytes()
	if !bytes.Equal(recovered, tx.Paymaster) {
		return nil, ErrPaymasterSignatureInvalid
	}
	sponsor := make([]byte, len(recovered))
	copy(sponsor, recovered)
	return sponsor, nil
}
