package cluster

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type lendingPosition struct {
	Account      string `json:"account"`
	Supplied     int64  `json:"supplied"`
	Borrowed     int64  `json:"borrowed"`
	HealthFactor string `json:"health_factor"`
}

type lendingResponse struct {
	Position lendingPosition `json:"position"`
}

type swapResponse struct {
	Account string `json:"account"`
	Balance int64  `json:"balance"`
}

type proposalStatus string

const (
	proposalStatusVoting  proposalStatus = "voting"
	proposalStatusApplied proposalStatus = "applied"
)

type proposal struct {
	ID          int            `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Status      proposalStatus `json:"status"`
	YesVotes    int            `json:"yes_votes"`
	NoVotes     int            `json:"no_votes"`
	Votes       map[string]string
}

type proposalResponse struct {
	Proposal proposal `json:"proposal"`
}

type applyResponse struct {
	ProposalID int   `json:"proposal_id"`
	Height     int64 `json:"height"`
}

type consensusSnapshot struct {
	Height    int64 `json:"height"`
	Proposals []int `json:"proposals"`
}

type State struct {
	mu sync.Mutex

	lendingPositions map[string]lendingPosition
	lendingRequests  map[string]lendingResponse

	swapBalances map[string]int64
	swapRequests map[string]swapResponse

	proposals       map[int]*proposal
	proposalSeq     int
	proposalRequest map[string]proposalResponse
	voteRequest     map[string]proposalResponse
	applyRequest    map[string]applyResponse

	consensusHeight  int64
	appliedProposals []int
}

func newState() *State {
	return &State{
		lendingPositions: make(map[string]lendingPosition),
		lendingRequests:  make(map[string]lendingResponse),
		swapBalances:     make(map[string]int64),
		swapRequests:     make(map[string]swapResponse),
		proposals:        make(map[int]*proposal),
		proposalRequest:  make(map[string]proposalResponse),
		voteRequest:      make(map[string]proposalResponse),
		applyRequest:     make(map[string]applyResponse),
		appliedProposals: make([]int, 0, 8),
	}
}

func (s *State) supply(account, reqID string, amount int64) (lendingResponse, error) {
	if err := validateAccount(account); err != nil {
		return lendingResponse{}, err
	}
	if amount <= 0 {
		return lendingResponse{}, fmt.Errorf("amount must be positive")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if reqID != "" {
		if resp, ok := s.lendingRequests[reqID]; ok {
			return resp, nil
		}
	}

	pos := s.getPosition(account)
	pos.Supplied += amount
	pos.HealthFactor = computeHealth(pos.Supplied, pos.Borrowed)
	s.lendingPositions[account] = pos

	resp := lendingResponse{Position: pos}
	if reqID != "" {
		s.lendingRequests[reqID] = resp
	}
	return resp, nil
}

func (s *State) borrow(account, reqID string, amount int64) (lendingResponse, error) {
	if err := validateAccount(account); err != nil {
		return lendingResponse{}, err
	}
	if amount <= 0 {
		return lendingResponse{}, fmt.Errorf("amount must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if reqID != "" {
		if resp, ok := s.lendingRequests[reqID]; ok {
			return resp, nil
		}
	}

	pos := s.getPosition(account)
	available := pos.Supplied - pos.Borrowed
	if available < amount {
		return lendingResponse{}, fmt.Errorf("insufficient collateral: available %d", available)
	}
	pos.Borrowed += amount
	pos.HealthFactor = computeHealth(pos.Supplied, pos.Borrowed)
	s.lendingPositions[account] = pos

	resp := lendingResponse{Position: pos}
	if reqID != "" {
		s.lendingRequests[reqID] = resp
	}
	return resp, nil
}

func (s *State) repay(account, reqID string, amount int64) (lendingResponse, error) {
	if err := validateAccount(account); err != nil {
		return lendingResponse{}, err
	}
	if amount <= 0 {
		return lendingResponse{}, fmt.Errorf("amount must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if reqID != "" {
		if resp, ok := s.lendingRequests[reqID]; ok {
			return resp, nil
		}
	}

	pos := s.getPosition(account)
	if pos.Borrowed == 0 {
		return lendingResponse{}, fmt.Errorf("nothing to repay")
	}
	if amount > pos.Borrowed {
		amount = pos.Borrowed
	}
	pos.Borrowed -= amount
	pos.HealthFactor = computeHealth(pos.Supplied, pos.Borrowed)
	s.lendingPositions[account] = pos

	resp := lendingResponse{Position: pos}
	if reqID != "" {
		s.lendingRequests[reqID] = resp
	}
	return resp, nil
}

func (s *State) position(account string) lendingResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	pos := s.getPosition(account)
	return lendingResponse{Position: pos}
}

func (s *State) mint(account, reqID string, amount int64) (swapResponse, error) {
	if err := validateAccount(account); err != nil {
		return swapResponse{}, err
	}
	if amount <= 0 {
		return swapResponse{}, fmt.Errorf("amount must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if reqID != "" {
		if resp, ok := s.swapRequests[reqID]; ok {
			return resp, nil
		}
	}

	balance := s.swapBalances[account]
	balance += amount
	s.swapBalances[account] = balance
	resp := swapResponse{Account: account, Balance: balance}
	if reqID != "" {
		s.swapRequests[reqID] = resp
	}
	return resp, nil
}

func (s *State) redeem(account, reqID string, amount int64) (swapResponse, error) {
	if err := validateAccount(account); err != nil {
		return swapResponse{}, err
	}
	if amount <= 0 {
		return swapResponse{}, fmt.Errorf("amount must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if reqID != "" {
		if resp, ok := s.swapRequests[reqID]; ok {
			return resp, nil
		}
	}

	balance := s.swapBalances[account]
	if amount > balance {
		return swapResponse{}, fmt.Errorf("insufficient balance: %d", balance)
	}
	balance -= amount
	s.swapBalances[account] = balance
	resp := swapResponse{Account: account, Balance: balance}
	if reqID != "" {
		s.swapRequests[reqID] = resp
	}
	return resp, nil
}

func (s *State) balance(account string) swapResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	balance := s.swapBalances[account]
	return swapResponse{Account: account, Balance: balance}
}

func (s *State) propose(reqID, title, description string) (proposalResponse, error) {
	if strings.TrimSpace(title) == "" {
		return proposalResponse{}, fmt.Errorf("title required")
	}
	if strings.TrimSpace(description) == "" {
		return proposalResponse{}, fmt.Errorf("description required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if reqID != "" {
		if resp, ok := s.proposalRequest[reqID]; ok {
			return resp, nil
		}
	}

	s.proposalSeq++
	proposal := &proposal{
		ID:          s.proposalSeq,
		Title:       title,
		Description: description,
		Status:      proposalStatusVoting,
		Votes:       make(map[string]string),
	}
	s.proposals[proposal.ID] = proposal

	resp := proposalResponse{Proposal: *proposal}
	if reqID != "" {
		s.proposalRequest[reqID] = resp
	}
	return resp, nil
}

func (s *State) vote(reqID string, id int, voter, option string) (proposalResponse, error) {
	if err := validateAccount(voter); err != nil {
		return proposalResponse{}, err
	}
	option = strings.ToLower(strings.TrimSpace(option))
	if option != "yes" && option != "no" {
		return proposalResponse{}, fmt.Errorf("invalid vote option: %s", option)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if reqID != "" {
		if resp, ok := s.voteRequest[reqID]; ok {
			return resp, nil
		}
	}

	prop, ok := s.proposals[id]
	if !ok {
		return proposalResponse{}, fmt.Errorf("proposal %d not found", id)
	}
	if prop.Status != proposalStatusVoting {
		return proposalResponse{}, fmt.Errorf("proposal %d not in voting period", id)
	}

	prev, hadPrev := prop.Votes[voter]
	if hadPrev {
		if prev == option {
			resp := proposalResponse{Proposal: *prop}
			if reqID != "" {
				s.voteRequest[reqID] = resp
			}
			return resp, nil
		}
		if prev == "yes" {
			prop.YesVotes--
		} else if prev == "no" {
			prop.NoVotes--
		}
	}
	prop.Votes[voter] = option
	if option == "yes" {
		prop.YesVotes++
	} else {
		prop.NoVotes++
	}

	resp := proposalResponse{Proposal: *prop}
	if reqID != "" {
		s.voteRequest[reqID] = resp
	}
	return resp, nil
}

func (s *State) apply(reqID string, id int) (applyResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if reqID != "" {
		if resp, ok := s.applyRequest[reqID]; ok {
			return resp, nil
		}
	}

	prop, ok := s.proposals[id]
	if !ok {
		return applyResponse{}, fmt.Errorf("proposal %d not found", id)
	}
	if prop.Status != proposalStatusVoting {
		if prop.Status == proposalStatusApplied {
			resp := applyResponse{ProposalID: id, Height: s.consensusHeight}
			if reqID != "" {
				s.applyRequest[reqID] = resp
			}
			return resp, nil
		}
		return applyResponse{}, fmt.Errorf("proposal %d cannot be applied", id)
	}
	if prop.YesVotes <= prop.NoVotes {
		return applyResponse{}, fmt.Errorf("proposal %d lacks approval", id)
	}

	s.consensusHeight++
	prop.Status = proposalStatusApplied
	s.appliedProposals = append(s.appliedProposals, prop.ID)

	resp := applyResponse{ProposalID: prop.ID, Height: s.consensusHeight}
	if reqID != "" {
		s.applyRequest[reqID] = resp
	}
	return resp, nil
}

func (s *State) proposalByID(id int) (proposalResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prop, ok := s.proposals[id]
	if !ok {
		return proposalResponse{}, fmt.Errorf("proposal %d not found", id)
	}
	return proposalResponse{Proposal: *prop}, nil
}

func (s *State) consensusState() consensusSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	proposals := make([]int, len(s.appliedProposals))
	copy(proposals, s.appliedProposals)
	sort.Ints(proposals)
	return consensusSnapshot{Height: s.consensusHeight, Proposals: proposals}
}

func (s *State) getPosition(account string) lendingPosition {
	pos, ok := s.lendingPositions[account]
	if !ok {
		pos = lendingPosition{Account: account, Supplied: 0, Borrowed: 0, HealthFactor: computeHealth(0, 0)}
	}
	return pos
}

func validateAccount(account string) error {
	account = strings.TrimSpace(account)
	if account == "" {
		return fmt.Errorf("account required")
	}
	if len(account) < 4 {
		return fmt.Errorf("account must be at least 4 characters")
	}
	return nil
}

func computeHealth(supplied, borrowed int64) string {
	if borrowed <= 0 {
		return "safe"
	}
	ratio := float64(supplied) / float64(borrowed)
	return strconv.FormatFloat(ratio, 'f', 2, 64)
}
