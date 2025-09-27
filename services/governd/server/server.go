package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"nhbchain/crypto"
	"nhbchain/native/governance"
	govv1 "nhbchain/proto/gov/v1"
	cons "nhbchain/sdk/consensus"
	govsdk "nhbchain/sdk/gov"
	"nhbchain/services/governd/config"
)

// Service implements the governance query and message APIs.
type Service struct {
	govv1.UnimplementedQueryServer
	govv1.UnimplementedMsgServer

	consensus *cons.Client
	signer    *crypto.PrivateKey
	chainID   string
	fee       config.FeeConfig

	nonceMu sync.Mutex
	nonce   uint64
}

// New constructs a governance service using the supplied dependencies.
func New(client *cons.Client, signer *crypto.PrivateKey, cfg config.Config) *Service {
	return &Service{
		consensus: client,
		signer:    signer,
		chainID:   cfg.ChainID,
		fee:       cfg.Fee,
		nonce:     cfg.NonceStart,
	}
}

// GetProposal implements gov.v1.Query.GetProposal.
func (s *Service) GetProposal(ctx context.Context, req *govv1.GetProposalRequest) (*govv1.GetProposalResponse, error) {
	if req == nil || req.GetId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "proposal id required")
	}
	proposal, err := s.fetchProposal(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	if proposal == nil {
		return nil, status.Errorf(codes.NotFound, "proposal %d not found", req.GetId())
	}
	return &govv1.GetProposalResponse{Proposal: proposal}, nil
}

// ListProposals implements gov.v1.Query.ListProposals.
func (s *Service) ListProposals(ctx context.Context, req *govv1.ListProposalsRequest) (*govv1.ListProposalsResponse, error) {
	pageSize := req.GetPageSize()
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	var start uint64
	if token := strings.TrimSpace(req.GetPageToken()); token != "" {
		parsed, err := parseUint(token)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid page token: %v", err)
		}
		start = parsed
	} else {
		latest, err := s.latestProposalID(ctx)
		if err != nil {
			return nil, err
		}
		start = latest
	}
	if start == 0 {
		return &govv1.ListProposalsResponse{}, nil
	}
	filter := req.GetStatusFilter()
	proposals := make([]*govv1.Proposal, 0, pageSize)
	current := start
	for current >= 1 && len(proposals) < int(pageSize) {
		proposal, err := s.fetchProposal(ctx, current)
		if err != nil {
			return nil, err
		}
		if proposal != nil {
			if filter == govv1.ProposalStatus_PROPOSAL_STATUS_UNSPECIFIED || proposal.GetStatus() == filter {
				proposals = append(proposals, proposal)
			}
		}
		if current == 0 {
			break
		}
		current--
	}
	resp := &govv1.ListProposalsResponse{Proposals: proposals}
	if current >= 1 {
		resp.NextPageToken = fmt.Sprintf("%d", current)
	}
	return resp, nil
}

// GetTally implements gov.v1.Query.GetTally.
func (s *Service) GetTally(ctx context.Context, req *govv1.GetTallyRequest) (*govv1.GetTallyResponse, error) {
	if req == nil || req.GetId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "proposal id required")
	}
	tally, err := s.fetchTally(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	if tally == nil {
		return nil, status.Errorf(codes.NotFound, "tally for proposal %d not found", req.GetId())
	}
	return &govv1.GetTallyResponse{Tally: tally}, nil
}

// SubmitProposal implements gov.v1.Msg.SubmitProposal.
func (s *Service) SubmitProposal(ctx context.Context, req *govv1.MsgSubmitProposal) (*govv1.MsgSubmitProposalResponse, error) {
	msg, err := govsdk.NewMsgSubmitProposal(req.GetProposer(), req.GetTitle(), req.GetDescription(), req.GetDeposit())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid proposal: %v", err)
	}
	txHash, err := s.broadcast(ctx, msg)
	if err != nil {
		return nil, err
	}
	return &govv1.MsgSubmitProposalResponse{TxHash: txHash}, nil
}

// Vote implements gov.v1.Msg.Vote.
func (s *Service) Vote(ctx context.Context, req *govv1.MsgVote) (*govv1.MsgVoteResponse, error) {
	msg, err := govsdk.NewMsgVote(req.GetVoter(), req.GetProposalId(), req.GetOption())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid vote: %v", err)
	}
	txHash, err := s.broadcast(ctx, msg)
	if err != nil {
		return nil, err
	}
	return &govv1.MsgVoteResponse{TxHash: txHash}, nil
}

// SetPauses implements gov.v1.Msg.SetPauses.
func (s *Service) SetPauses(ctx context.Context, req *govv1.MsgSetPauses) (*govv1.MsgSetPausesResponse, error) {
	msg, err := govsdk.NewMsgSetPauses(req.GetAuthority(), req.GetPauses())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pause payload: %v", err)
	}
	txHash, err := s.broadcast(ctx, msg)
	if err != nil {
		return nil, err
	}
	return &govv1.MsgSetPausesResponse{TxHash: txHash}, nil
}

// Deposit implements gov.v1.Msg.Deposit.
func (s *Service) Deposit(ctx context.Context, req *govv1.MsgDeposit) (*govv1.MsgDepositResponse, error) {
	msg, err := govsdk.NewMsgDeposit(req.GetDepositor(), req.GetProposalId(), req.GetAmount())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid deposit: %v", err)
	}
	txHash, err := s.broadcast(ctx, msg)
	if err != nil {
		return nil, err
	}
	return &govv1.MsgDepositResponse{TxHash: txHash}, nil
}

func (s *Service) fetchProposal(ctx context.Context, id uint64) (*govv1.Proposal, error) {
	if s == nil || s.consensus == nil {
		return nil, status.Error(codes.Unavailable, "consensus client unavailable")
	}
	value, _, err := s.consensus.QueryState(ctx, "gov", fmt.Sprintf("proposals/%d", id))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query proposal: %v", err)
	}
	if len(value) == 0 {
		return nil, nil
	}
	var proposal governance.Proposal
	if err := json.Unmarshal(value, &proposal); err != nil {
		return nil, status.Errorf(codes.Internal, "decode proposal: %v", err)
	}
	return convertProposal(&proposal), nil
}

func (s *Service) latestProposalID(ctx context.Context) (uint64, error) {
	if s == nil || s.consensus == nil {
		return 0, status.Error(codes.Unavailable, "consensus client unavailable")
	}
	value, _, err := s.consensus.QueryState(ctx, "gov", "proposals/latest")
	if err != nil {
		return 0, status.Errorf(codes.Internal, "query latest proposal id: %v", err)
	}
	if len(value) == 0 {
		return 0, nil
	}
	var resp struct {
		Latest uint64 `json:"latest"`
	}
	if err := json.Unmarshal(value, &resp); err != nil {
		return 0, status.Errorf(codes.Internal, "decode latest proposal id: %v", err)
	}
	return resp.Latest, nil
}

func (s *Service) fetchTally(ctx context.Context, id uint64) (*govv1.ProposalTally, error) {
	if s == nil || s.consensus == nil {
		return nil, status.Error(codes.Unavailable, "consensus client unavailable")
	}
	value, _, err := s.consensus.QueryState(ctx, "gov", fmt.Sprintf("tallies/%d", id))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query tally: %v", err)
	}
	if len(value) == 0 {
		return nil, nil
	}
	var resp struct {
		ProposalID uint64            `json:"proposal_id"`
		Status     string            `json:"status"`
		Tally      *governance.Tally `json:"tally"`
	}
	if err := json.Unmarshal(value, &resp); err != nil {
		return nil, status.Errorf(codes.Internal, "decode tally: %v", err)
	}
	if resp.Tally == nil {
		return nil, nil
	}
	statusEnum, err := parseStatus(resp.Status)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "invalid status: %v", err)
	}
	return convertTally(resp.ProposalID, statusEnum, resp.Tally), nil
}

func (s *Service) broadcast(ctx context.Context, payload proto.Message) (string, error) {
	if s == nil || s.consensus == nil {
		return "", status.Error(codes.Unavailable, "consensus client unavailable")
	}
	nonce := s.reserveNonce()
	envelope, err := cons.NewTx(payload, nonce, s.chainID, s.fee.Amount, s.fee.Denom, s.fee.Payer, "")
	if err != nil {
		return "", status.Errorf(codes.Internal, "build envelope: %v", err)
	}
	signed, err := cons.Sign(envelope, s.signer)
	if err != nil {
		return "", status.Errorf(codes.Internal, "sign envelope: %v", err)
	}
	if err := s.consensus.SubmitEnvelope(ctx, signed); err != nil {
		return "", status.Errorf(codes.Internal, "submit envelope: %v", err)
	}
	raw, err := proto.Marshal(signed)
	if err != nil {
		return "", status.Errorf(codes.Internal, "marshal signed envelope: %v", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func (s *Service) reserveNonce() uint64 {
	s.nonceMu.Lock()
	defer s.nonceMu.Unlock()
	nonce := s.nonce
	s.nonce++
	return nonce
}

func convertProposal(proposal *governance.Proposal) *govv1.Proposal {
	if proposal == nil {
		return nil
	}
	pb := &govv1.Proposal{
		Id:             proposal.ID,
		Title:          proposal.Title,
		Summary:        proposal.Summary,
		MetadataUri:    proposal.MetadataURI,
		Proposer:       proposal.Submitter.String(),
		Status:         statusToProto(proposal.Status),
		Target:         proposal.Target,
		ProposedChange: proposal.ProposedChange,
		Queued:         proposal.Queued,
	}
	if proposal.Deposit != nil {
		pb.Deposit = proposal.Deposit.String()
	}
	if !proposal.SubmitTime.IsZero() {
		pb.SubmitTime = timestamppb.New(proposal.SubmitTime)
	}
	if !proposal.VotingStart.IsZero() {
		pb.VotingStartTime = timestamppb.New(proposal.VotingStart)
	}
	if !proposal.VotingEnd.IsZero() {
		pb.VotingEndTime = timestamppb.New(proposal.VotingEnd)
	}
	if !proposal.TimelockEnd.IsZero() {
		pb.TimelockEndTime = timestamppb.New(proposal.TimelockEnd)
	}
	return pb
}

func convertTally(proposalID uint64, status govv1.ProposalStatus, tally *governance.Tally) *govv1.ProposalTally {
	if tally == nil {
		return nil
	}
	return &govv1.ProposalTally{
		ProposalId:       proposalID,
		Status:           status,
		TurnoutBps:       tally.TurnoutBps,
		QuorumBps:        tally.QuorumBps,
		YesPowerBps:      tally.YesPowerBps,
		NoPowerBps:       tally.NoPowerBps,
		AbstainPowerBps:  tally.AbstainPowerBps,
		YesRatioBps:      tally.YesRatioBps,
		PassThresholdBps: tally.PassThresholdBps,
		TotalBallots:     tally.TotalBallots,
	}
}

func statusToProto(status governance.ProposalStatus) govv1.ProposalStatus {
	switch status {
	case governance.ProposalStatusDepositPeriod:
		return govv1.ProposalStatus_PROPOSAL_STATUS_DEPOSIT_PERIOD
	case governance.ProposalStatusVotingPeriod:
		return govv1.ProposalStatus_PROPOSAL_STATUS_VOTING_PERIOD
	case governance.ProposalStatusPassed:
		return govv1.ProposalStatus_PROPOSAL_STATUS_PASSED
	case governance.ProposalStatusRejected:
		return govv1.ProposalStatus_PROPOSAL_STATUS_REJECTED
	case governance.ProposalStatusFailed:
		return govv1.ProposalStatus_PROPOSAL_STATUS_FAILED
	case governance.ProposalStatusExpired:
		return govv1.ProposalStatus_PROPOSAL_STATUS_EXPIRED
	case governance.ProposalStatusExecuted:
		return govv1.ProposalStatus_PROPOSAL_STATUS_EXECUTED
	default:
		return govv1.ProposalStatus_PROPOSAL_STATUS_UNSPECIFIED
	}
}

func parseStatus(status string) (govv1.ProposalStatus, error) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "deposit_period":
		return govv1.ProposalStatus_PROPOSAL_STATUS_DEPOSIT_PERIOD, nil
	case "voting_period":
		return govv1.ProposalStatus_PROPOSAL_STATUS_VOTING_PERIOD, nil
	case "passed":
		return govv1.ProposalStatus_PROPOSAL_STATUS_PASSED, nil
	case "rejected":
		return govv1.ProposalStatus_PROPOSAL_STATUS_REJECTED, nil
	case "failed":
		return govv1.ProposalStatus_PROPOSAL_STATUS_FAILED, nil
	case "expired":
		return govv1.ProposalStatus_PROPOSAL_STATUS_EXPIRED, nil
	case "executed":
		return govv1.ProposalStatus_PROPOSAL_STATUS_EXECUTED, nil
	case "unspecified", "":
		return govv1.ProposalStatus_PROPOSAL_STATUS_UNSPECIFIED, nil
	default:
		return govv1.ProposalStatus_PROPOSAL_STATUS_UNSPECIFIED, fmt.Errorf("unknown status %q", status)
	}
}

func parseUint(input string) (uint64, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return 0, fmt.Errorf("value required")
	}
	value, ok := new(big.Int).SetString(trimmed, 10)
	if !ok || value.Sign() < 0 {
		return 0, fmt.Errorf("invalid unsigned integer")
	}
	if !value.IsUint64() {
		return 0, fmt.Errorf("value exceeds uint64")
	}
	return value.Uint64(), nil
}
