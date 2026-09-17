package main

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/types"
)

// registerValidatorPayload mirrors core/state_transition.go's applyStake
// decode target exactly (field order matters -- RLP list decoding is
// positional). RegisterValidator is only meaningful/settable on a
// self-stake (Validator left empty here, since this command always acts on
// the signer's own address).
type registerValidatorPayload struct {
	Validator         []byte `json:"validator,omitempty"`
	RegisterValidator bool   `json:"registerValidator,omitempty" rlp:"optional"`
}

// deregisterValidatorPayload mirrors applyUnstake's decode target, symmetric
// with registerValidatorPayload above.
type deregisterValidatorPayload struct {
	Validator           []byte `json:"validator,omitempty"`
	DeregisterValidator bool   `json:"deregisterValidator,omitempty" rlp:"optional"`
}

// registerValidator submits a signed TxTypeStake transaction with
// RegisterValidator=true, opting the signer's own address into explicit
// validator status (item 1 of the validator-eligibility redesign). This is
// deliberately a local, signed-transaction operation run with the
// validator's own key file -- mirroring setRewardBeneficiary's rationale --
// since only that key can authorize registering itself as a validator
// candidate, and matches this repo's documented "the validator's signing
// key and its funding wallet are deliberately kept separate" onboarding
// model (see docs/validators/onboarding.md): if the account already carries
// sufficient self-stake, pass "0" to flip the flag with no additional
// transfer (a "pure registration" call, supported by applyStake's
// pureRegister path); otherwise pass a positive amount (in base ZNHB
// units/wei, matching the existing `stake`/`un-stake` CLI convention) to
// self-stake and register in a single transaction.
//
// Returns 0 on success and 1 on any failure, matching setRewardBeneficiary's
// exit-code convention.
func registerValidator(amountStr string, keyFile string) int {
	privKey, err := loadPrivateKey(keyFile)
	if err != nil {
		fmt.Printf("Error loading private key: %v\n", err)
		return 1
	}
	pubAddr := privKey.PubKey().Address().String()

	amount, ok := new(big.Int).SetString(strings.TrimSpace(amountStr), 10)
	if !ok || amount.Sign() < 0 {
		fmt.Printf("Error: amount must be a non-negative base-10 integer (use 0 to register without adding stake)\n")
		return 1
	}

	account, err := fetchAccount(pubAddr)
	if err != nil {
		fmt.Printf("Error fetching account details: %v\n", err)
		return 1
	}

	payload := registerValidatorPayload{RegisterValidator: true}
	data, err := rlp.EncodeToBytes(payload)
	if err != nil {
		fmt.Printf("Error encoding payload: %v\n", err)
		return 1
	}

	tx := types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeStake,
		Nonce:    account.Nonce,
		Value:    amount,
		Data:     data,
		GasLimit: 50000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(privKey.PrivateKey); err != nil {
		fmt.Printf("Error signing transaction: %v\n", err)
		return 1
	}

	if _, err := sendTransaction(&tx); err != nil {
		fmt.Printf("Error sending transaction: %v\n", err)
		return 1
	}

	if amount.Sign() > 0 {
		fmt.Printf("Submitted self-stake of %s plus validator registration for %s.\n", amount.String(), pubAddr)
	} else {
		fmt.Printf("Submitted validator registration for %s (no additional stake).\n", pubAddr)
	}
	fmt.Println("Eligibility takes effect once own self-stake (minus any delegated-in total) meets the governed minimum and the account is heartbeat-active.")
	return 0
}

// deregisterValidator submits a signed TxTypeUnstake transaction with
// DeregisterValidator=true and zero value, symmetric with
// registerValidator. This is the "no way out" escape hatch item 4/5 require:
// it works even for an account that has already fully unstaked (where a
// value-bearing unstake would hard-error with "no active delegation"),
// since it never calls StakeUndelegate at all.
func deregisterValidator(keyFile string) int {
	privKey, err := loadPrivateKey(keyFile)
	if err != nil {
		fmt.Printf("Error loading private key: %v\n", err)
		return 1
	}
	pubAddr := privKey.PubKey().Address().String()

	account, err := fetchAccount(pubAddr)
	if err != nil {
		fmt.Printf("Error fetching account details: %v\n", err)
		return 1
	}

	payload := deregisterValidatorPayload{DeregisterValidator: true}
	data, err := rlp.EncodeToBytes(payload)
	if err != nil {
		fmt.Printf("Error encoding payload: %v\n", err)
		return 1
	}

	tx := types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeUnstake,
		Nonce:    account.Nonce,
		Value:    big.NewInt(0),
		Data:     data,
		GasLimit: 50000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(privKey.PrivateKey); err != nil {
		fmt.Printf("Error signing transaction: %v\n", err)
		return 1
	}

	if _, err := sendTransaction(&tx); err != nil {
		fmt.Printf("Error sending transaction: %v\n", err)
		return 1
	}

	fmt.Printf("Submitted validator de-registration for %s.\n", pubAddr)
	fmt.Println("This address will no longer be eligible for the BFT validator set or automatic governance voting membership.")
	return 0
}
