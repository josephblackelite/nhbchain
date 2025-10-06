package core

import (
	"fmt"
	"math/big"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"

	"github.com/ethereum/go-ethereum/common"
)

// SponsorshipStatus describes the evaluation outcome for a transaction's
// paymaster request.
type SponsorshipStatus string

const (
	SponsorshipStatusNone                SponsorshipStatus = "none"
	SponsorshipStatusModuleDisabled      SponsorshipStatus = "module_disabled"
	SponsorshipStatusSignatureMissing    SponsorshipStatus = "signature_missing"
	SponsorshipStatusSignatureInvalid    SponsorshipStatus = "signature_invalid"
	SponsorshipStatusInsufficientBalance SponsorshipStatus = "insufficient_balance"
	SponsorshipStatusThrottled           SponsorshipStatus = "throttled"
	SponsorshipStatusReady               SponsorshipStatus = "ready"
)

// PaymasterThrottleScope identifies the scope for a throttled sponsorship attempt.
type PaymasterThrottleScope string

const (
	// PaymasterThrottleScopeMerchant indicates the merchant aggregate exceeded its budget.
	PaymasterThrottleScopeMerchant PaymasterThrottleScope = "merchant"
	// PaymasterThrottleScopeDevice indicates the device exhausted its transaction allocation.
	PaymasterThrottleScopeDevice PaymasterThrottleScope = "device"
	// PaymasterThrottleScopeGlobal indicates the global sponsorship cap has been consumed.
	PaymasterThrottleScopeGlobal PaymasterThrottleScope = "global"
)

// PaymasterThrottle captures context for a throttled sponsorship attempt.
type PaymasterThrottle struct {
	Scope            PaymasterThrottleScope
	Merchant         string
	DeviceID         string
	Day              string
	LimitWei         *big.Int
	UsedBudgetWei    *big.Int
	AttemptBudgetWei *big.Int
	TxCount          uint64
	LimitTxCount     uint64
}

// Clone returns a deep copy of the throttle metadata.
func (p *PaymasterThrottle) Clone() *PaymasterThrottle {
	if p == nil {
		return nil
	}
	clone := &PaymasterThrottle{
		Scope:        p.Scope,
		Merchant:     p.Merchant,
		DeviceID:     p.DeviceID,
		Day:          p.Day,
		TxCount:      p.TxCount,
		LimitTxCount: p.LimitTxCount,
	}
	if p.LimitWei != nil {
		clone.LimitWei = new(big.Int).Set(p.LimitWei)
	}
	if p.UsedBudgetWei != nil {
		clone.UsedBudgetWei = new(big.Int).Set(p.UsedBudgetWei)
	}
	if p.AttemptBudgetWei != nil {
		clone.AttemptBudgetWei = new(big.Int).Set(p.AttemptBudgetWei)
	}
	return clone
}

// SponsorshipAssessment summarises the pre-flight checks for a paymaster
// sponsored transaction. Callers may surface the status and reason to clients.
type SponsorshipAssessment struct {
	Status   SponsorshipStatus
	Reason   string
	Sponsor  common.Address
	GasCost  *big.Int
	GasPrice *big.Int
	Throttle *PaymasterThrottle
	day      string
	merchant string
	deviceID string
}

// PaymasterLimits captures the configured sponsorship bounds enforced per merchant, device, and globally.
type PaymasterLimits struct {
	MerchantDailyCapWei *big.Int
	DeviceDailyTxCap    uint64
	GlobalDailyCapWei   *big.Int
}

// Clone returns a deep copy of the limits structure.
func (l PaymasterLimits) Clone() PaymasterLimits {
	clone := PaymasterLimits{DeviceDailyTxCap: l.DeviceDailyTxCap}
	if l.MerchantDailyCapWei != nil {
		clone.MerchantDailyCapWei = new(big.Int).Set(l.MerchantDailyCapWei)
	}
	if l.GlobalDailyCapWei != nil {
		clone.GlobalDailyCapWei = new(big.Int).Set(l.GlobalDailyCapWei)
	}
	return clone
}

// PaymasterCounters aggregates usage statistics for the configured scopes on a given day.
type PaymasterCounters struct {
	Day string

	Merchant string
	DeviceID string

	MerchantBudgetWei  *big.Int
	MerchantChargedWei *big.Int
	MerchantTxCount    uint64

	DeviceBudgetWei  *big.Int
	DeviceChargedWei *big.Int
	DeviceTxCount    uint64

	GlobalBudgetWei  *big.Int
	GlobalChargedWei *big.Int
	GlobalTxCount    uint64
}

// Clone returns a deep copy of the counters snapshot.
func (p *PaymasterCounters) Clone() *PaymasterCounters {
	if p == nil {
		return nil
	}
	clone := &PaymasterCounters{
		Day:             p.Day,
		Merchant:        p.Merchant,
		DeviceID:        p.DeviceID,
		MerchantTxCount: p.MerchantTxCount,
		DeviceTxCount:   p.DeviceTxCount,
		GlobalTxCount:   p.GlobalTxCount,
	}
	if p.MerchantBudgetWei != nil {
		clone.MerchantBudgetWei = new(big.Int).Set(p.MerchantBudgetWei)
	}
	if p.MerchantChargedWei != nil {
		clone.MerchantChargedWei = new(big.Int).Set(p.MerchantChargedWei)
	}
	if p.DeviceBudgetWei != nil {
		clone.DeviceBudgetWei = new(big.Int).Set(p.DeviceBudgetWei)
	}
	if p.DeviceChargedWei != nil {
		clone.DeviceChargedWei = new(big.Int).Set(p.DeviceChargedWei)
	}
	if p.GlobalBudgetWei != nil {
		clone.GlobalBudgetWei = new(big.Int).Set(p.GlobalBudgetWei)
	}
	if p.GlobalChargedWei != nil {
		clone.GlobalChargedWei = new(big.Int).Set(p.GlobalChargedWei)
	}
	return clone
}

// EvaluateSponsorship inspects the transaction and returns the expected
// sponsorship status. Errors represent unexpected state retrieval failures; all
// validation issues are reflected in the returned assessment instead.
func (sp *StateProcessor) EvaluateSponsorship(tx *types.Transaction) (*SponsorshipAssessment, error) {
	if tx == nil {
		return nil, fmt.Errorf("transaction required")
	}
	assessment := &SponsorshipAssessment{Status: SponsorshipStatusNone}
	if len(tx.Paymaster) == 0 {
		return assessment, nil
	}

	sponsorAddr := common.BytesToAddress(tx.Paymaster)
	assessment.Sponsor = sponsorAddr

	if sponsorAddr == (common.Address{}) {
		assessment.Status = SponsorshipStatusSignatureInvalid
		assessment.Reason = "paymaster address cannot be zero"
		return assessment, nil
	}

	if !sp.paymasterEnabled {
		assessment.Status = SponsorshipStatusModuleDisabled
		assessment.Reason = "paymaster module disabled"
		return assessment, nil
	}

	sponsor, err := tx.PaymasterSponsor()
	if err != nil {
		switch err {
		case types.ErrPaymasterSignatureMissing:
			assessment.Status = SponsorshipStatusSignatureMissing
			assessment.Reason = "missing paymaster signature"
			return assessment, nil
		case types.ErrPaymasterSignatureInvalid:
			assessment.Status = SponsorshipStatusSignatureInvalid
			assessment.Reason = "invalid paymaster signature"
			return assessment, nil
		default:
			return nil, err
		}
	}
	if len(sponsor) == 0 {
		assessment.Status = SponsorshipStatusSignatureInvalid
		assessment.Reason = "unable to recover paymaster"
		return assessment, nil
	}

	gasPrice := big.NewInt(0)
	if tx.GasPrice != nil {
		gasPrice = new(big.Int).Set(tx.GasPrice)
	}
	gasCost := new(big.Int).Mul(new(big.Int).SetUint64(tx.GasLimit), gasPrice)
	assessment.GasCost = gasCost
	assessment.GasPrice = gasPrice

	account, err := sp.getAccount(tx.Paymaster)
	if err != nil {
		return nil, err
	}
	if account == nil || account.BalanceNHB == nil || account.BalanceNHB.Cmp(gasCost) < 0 {
		assessment.Status = SponsorshipStatusInsufficientBalance
		assessment.Reason = "paymaster balance below required gas budget"
		return assessment, nil
	}

	assessment.merchant = nhbstate.NormalizePaymasterMerchant(tx.MerchantAddress)
	assessment.deviceID = nhbstate.NormalizePaymasterDevice(tx.DeviceID)
	assessment.day = sp.currentPaymasterDay()

	if err := sp.checkPaymasterCaps(assessment); err != nil {
		return nil, err
	}
	if assessment.Status == SponsorshipStatusThrottled {
		return assessment, nil
	}

	assessment.Status = SponsorshipStatusReady
	assessment.Reason = ""
	return assessment, nil
}

func (sp *StateProcessor) currentPaymasterDay() string {
	if sp == nil {
		return ""
	}
	return nhbstate.NormalizePaymasterDay(sp.blockTimestamp().UTC().Format(nhbstate.PaymasterDayFormat))
}

func (sp *StateProcessor) checkPaymasterCaps(assessment *SponsorshipAssessment) error {
	if sp == nil || assessment == nil {
		return nil
	}
	limits := sp.paymasterLimits.Clone()
	if assessment.GasCost == nil {
		return nil
	}
	budget := new(big.Int).Set(assessment.GasCost)
	if budget.Sign() <= 0 {
		return nil
	}

	manager := nhbstate.NewManager(sp.Trie)
	day := assessment.day

	// Global cap check.
	if limits.GlobalDailyCapWei != nil && limits.GlobalDailyCapWei.Sign() > 0 {
		global, _, err := manager.PaymasterGetGlobalDay(day)
		if err != nil {
			return err
		}
		used := big.NewInt(0)
		if global != nil && global.BudgetWei != nil {
			used = new(big.Int).Set(global.BudgetWei)
		}
		projected := new(big.Int).Add(used, budget)
		if projected.Cmp(limits.GlobalDailyCapWei) > 0 {
			assessment.Status = SponsorshipStatusThrottled
			assessment.Reason = "global sponsorship cap reached"
			assessment.Throttle = &PaymasterThrottle{
				Scope:            PaymasterThrottleScopeGlobal,
				Day:              day,
				LimitWei:         new(big.Int).Set(limits.GlobalDailyCapWei),
				UsedBudgetWei:    used,
				AttemptBudgetWei: budget,
			}
			return nil
		}
	}

	merchant := assessment.merchant
	if limits.MerchantDailyCapWei != nil && limits.MerchantDailyCapWei.Sign() > 0 {
		if merchant == "" {
			assessment.Status = SponsorshipStatusThrottled
			assessment.Reason = "merchant address required for sponsorship throttling"
			assessment.Throttle = &PaymasterThrottle{
				Scope:            PaymasterThrottleScopeMerchant,
				Day:              day,
				LimitWei:         new(big.Int).Set(limits.MerchantDailyCapWei),
				AttemptBudgetWei: budget,
			}
			return nil
		}
		merchantRecord, _, err := manager.PaymasterGetMerchantDay(merchant, day)
		if err != nil {
			return err
		}
		used := big.NewInt(0)
		txCount := uint64(0)
		if merchantRecord != nil {
			if merchantRecord.BudgetWei != nil {
				used = new(big.Int).Set(merchantRecord.BudgetWei)
			}
			txCount = merchantRecord.TxCount
		}
		projected := new(big.Int).Add(used, budget)
		if projected.Cmp(limits.MerchantDailyCapWei) > 0 {
			assessment.Status = SponsorshipStatusThrottled
			assessment.Reason = "merchant sponsorship cap reached"
			assessment.Throttle = &PaymasterThrottle{
				Scope:            PaymasterThrottleScopeMerchant,
				Merchant:         merchant,
				Day:              day,
				LimitWei:         new(big.Int).Set(limits.MerchantDailyCapWei),
				UsedBudgetWei:    used,
				AttemptBudgetWei: budget,
				TxCount:          txCount,
			}
			return nil
		}
	}

	if limits.DeviceDailyTxCap > 0 {
		device := assessment.deviceID
		if device == "" || merchant == "" {
			assessment.Status = SponsorshipStatusThrottled
			assessment.Reason = "device identifier required for sponsorship throttling"
			assessment.Throttle = &PaymasterThrottle{
				Scope:            PaymasterThrottleScopeDevice,
				Merchant:         merchant,
				DeviceID:         device,
				Day:              day,
				AttemptBudgetWei: budget,
				LimitTxCount:     limits.DeviceDailyTxCap,
			}
			return nil
		}
		deviceRecord, _, err := manager.PaymasterGetDeviceDay(merchant, device, day)
		if err != nil {
			return err
		}
		txCount := uint64(0)
		if deviceRecord != nil {
			txCount = deviceRecord.TxCount
		}
		if txCount >= limits.DeviceDailyTxCap {
			assessment.Status = SponsorshipStatusThrottled
			assessment.Reason = "device sponsorship cap reached"
			assessment.Throttle = &PaymasterThrottle{
				Scope:            PaymasterThrottleScopeDevice,
				Merchant:         merchant,
				DeviceID:         device,
				Day:              day,
				AttemptBudgetWei: budget,
				TxCount:          txCount,
				LimitTxCount:     limits.DeviceDailyTxCap,
			}
			return nil
		}
	}

	return nil
}

// PaymasterLimits returns the current sponsorship caps applied to the state processor.
func (sp *StateProcessor) PaymasterLimits() PaymasterLimits {
	if sp == nil {
		return PaymasterLimits{}
	}
	return sp.paymasterLimits.Clone()
}

// SetPaymasterLimits updates the sponsorship caps enforced during evaluation.
func (sp *StateProcessor) SetPaymasterLimits(limits PaymasterLimits) {
	if sp == nil {
		return
	}
	sp.paymasterLimits = limits.Clone()
}

// PaymasterCounters aggregates the current usage metrics for the provided scope and day.
func (sp *StateProcessor) PaymasterCounters(merchant, device, day string) (*PaymasterCounters, error) {
	if sp == nil {
		return nil, fmt.Errorf("state processor not initialised")
	}
	manager := nhbstate.NewManager(sp.Trie)
	normalizedDay := nhbstate.NormalizePaymasterDay(day)
	if normalizedDay == "" {
		normalizedDay = sp.currentPaymasterDay()
	}
	normalizedMerchant := nhbstate.NormalizePaymasterMerchant(merchant)
	normalizedDevice := nhbstate.NormalizePaymasterDevice(device)

	snapshot := &PaymasterCounters{Day: normalizedDay, Merchant: normalizedMerchant, DeviceID: normalizedDevice}

	if normalizedMerchant != "" {
		merchantRecord, _, err := manager.PaymasterGetMerchantDay(normalizedMerchant, normalizedDay)
		if err != nil {
			return nil, err
		}
		if merchantRecord != nil {
			snapshot.MerchantTxCount = merchantRecord.TxCount
			snapshot.MerchantBudgetWei = new(big.Int).Set(merchantRecord.BudgetWei)
			snapshot.MerchantChargedWei = new(big.Int).Set(merchantRecord.ChargedWei)
		} else {
			snapshot.MerchantBudgetWei = big.NewInt(0)
			snapshot.MerchantChargedWei = big.NewInt(0)
		}
	} else {
		snapshot.MerchantBudgetWei = big.NewInt(0)
		snapshot.MerchantChargedWei = big.NewInt(0)
	}

	if normalizedMerchant != "" && normalizedDevice != "" {
		deviceRecord, _, err := manager.PaymasterGetDeviceDay(normalizedMerchant, normalizedDevice, normalizedDay)
		if err != nil {
			return nil, err
		}
		if deviceRecord != nil {
			snapshot.DeviceTxCount = deviceRecord.TxCount
			snapshot.DeviceBudgetWei = new(big.Int).Set(deviceRecord.BudgetWei)
			snapshot.DeviceChargedWei = new(big.Int).Set(deviceRecord.ChargedWei)
		} else {
			snapshot.DeviceBudgetWei = big.NewInt(0)
			snapshot.DeviceChargedWei = big.NewInt(0)
		}
	} else {
		snapshot.DeviceBudgetWei = big.NewInt(0)
		snapshot.DeviceChargedWei = big.NewInt(0)
	}

	globalRecord, _, err := manager.PaymasterGetGlobalDay(normalizedDay)
	if err != nil {
		return nil, err
	}
	if globalRecord != nil {
		snapshot.GlobalTxCount = globalRecord.TxCount
		snapshot.GlobalBudgetWei = new(big.Int).Set(globalRecord.BudgetWei)
		snapshot.GlobalChargedWei = new(big.Int).Set(globalRecord.ChargedWei)
	} else {
		snapshot.GlobalBudgetWei = big.NewInt(0)
		snapshot.GlobalChargedWei = big.NewInt(0)
	}

	return snapshot, nil
}
