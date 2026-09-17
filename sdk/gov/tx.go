package gov

import (
	"fmt"
	"math/big"
	"strings"

	govv1 "nhbchain/proto/gov/v1"
)

func normalizeDeposit(amount string) (string, error) {
	trimmed := strings.TrimSpace(amount)
	if trimmed == "" {
		return "", fmt.Errorf("deposit amount required")
	}
	parsed, ok := new(big.Int).SetString(trimmed, 10)
	if !ok || parsed.Sign() <= 0 {
		return "", fmt.Errorf("deposit amount must be a positive integer")
	}
	return parsed.String(), nil
}

// NewMsgSubmitProposal ensures that the governance proposal fields satisfy the basic
// validation rules.
func NewMsgSubmitProposal(proposer, title, description, deposit string) (*govv1.MsgSubmitProposal, error) {
	trimmedProposer := strings.TrimSpace(proposer)
	if trimmedProposer == "" {
		return nil, fmt.Errorf("proposer address required")
	}
	trimmedTitle := strings.TrimSpace(title)
	if trimmedTitle == "" {
		return nil, fmt.Errorf("proposal title required")
	}
	trimmedDescription := strings.TrimSpace(description)
	if trimmedDescription == "" {
		return nil, fmt.Errorf("proposal description required")
	}
	normalizedDeposit, err := normalizeDeposit(deposit)
	if err != nil {
		return nil, err
	}
	return &govv1.MsgSubmitProposal{
		Proposer:    trimmedProposer,
		Title:       trimmedTitle,
		Description: trimmedDescription,
		Deposit:     normalizedDeposit,
	}, nil
}

// NewMsgVote validates a vote submission payload.
func NewMsgVote(voter string, proposalID uint64, option string) (*govv1.MsgVote, error) {
	trimmedVoter := strings.TrimSpace(voter)
	if trimmedVoter == "" {
		return nil, fmt.Errorf("voter address required")
	}
	if proposalID == 0 {
		return nil, fmt.Errorf("proposal id required")
	}
	trimmedOption := strings.TrimSpace(option)
	if trimmedOption == "" {
		return nil, fmt.Errorf("vote option required")
	}
	return &govv1.MsgVote{
		Voter:      trimmedVoter,
		ProposalId: proposalID,
		Option:     trimmedOption,
	}, nil
}

// NewMsgDeposit validates an additional deposit message.
func NewMsgDeposit(depositor string, proposalID uint64, amount string) (*govv1.MsgDeposit, error) {
	trimmedDepositor := strings.TrimSpace(depositor)
	if trimmedDepositor == "" {
		return nil, fmt.Errorf("depositor address required")
	}
	if proposalID == 0 {
		return nil, fmt.Errorf("proposal id required")
	}
	normalizedAmount, err := normalizeDeposit(amount)
	if err != nil {
		return nil, err
	}
	return &govv1.MsgDeposit{
		Depositor:  trimmedDepositor,
		ProposalId: proposalID,
		Amount:     normalizedAmount,
	}, nil
}

// NewMsgSetPauses validates a pause toggle request. The pauses payload must be supplied.
func NewMsgSetPauses(authority string, pauses *govv1.Pauses) (*govv1.MsgSetPauses, error) {
	trimmedAuthority := strings.TrimSpace(authority)
	if trimmedAuthority == "" {
		return nil, fmt.Errorf("authority address required")
	}
	if pauses == nil {
		return nil, fmt.Errorf("pauses configuration required")
	}
	return &govv1.MsgSetPauses{
		Authority: trimmedAuthority,
		Pauses:    pauses,
	}, nil
}
