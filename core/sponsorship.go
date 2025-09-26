package core

import (
	"fmt"
	"math/big"

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
	SponsorshipStatusReady               SponsorshipStatus = "ready"
)

// SponsorshipAssessment summarises the pre-flight checks for a paymaster
// sponsored transaction. Callers may surface the status and reason to clients.
type SponsorshipAssessment struct {
	Status   SponsorshipStatus
	Reason   string
	Sponsor  common.Address
	GasCost  *big.Int
	GasPrice *big.Int
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

	assessment.Status = SponsorshipStatusReady
	assessment.Reason = ""
	return assessment, nil
}
