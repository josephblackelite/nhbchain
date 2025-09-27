package gov

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	govv1 "nhbchain/proto/gov/v1"
)

// Client wraps the governance service client with ergonomic helpers.
type Client struct {
	conn *grpc.ClientConn
	raw  govv1.GovernanceServiceClient
}

// Dial connects to a governance service endpoint.
func Dial(ctx context.Context, target string, opts ...grpc.DialOption) (*Client, error) {
	if len(opts) == 0 {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	conn, err := grpc.DialContext(ctx, target, opts...)
	if err != nil {
		return nil, err
	}
	return New(conn), nil
}

// New creates a typed client from an existing connection.
func New(conn *grpc.ClientConn) *Client {
	return &Client{
		conn: conn,
		raw:  govv1.NewGovernanceServiceClient(conn),
	}
}

// Close tears down the underlying connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Raw exposes the generated gRPC client.
func (c *Client) Raw() govv1.GovernanceServiceClient {
	if c == nil {
		return nil
	}
	return c.raw
}

// SubmitProposal creates a governance proposal and returns its metadata.
func (c *Client) SubmitProposal(ctx context.Context, proposer, title, summary string) (*govv1.Proposal, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	resp, err := c.raw.SubmitProposal(ctx, &govv1.SubmitProposalRequest{
		Proposer: proposer,
		Title:    title,
		Summary:  summary,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetProposal(), nil
}

// GetProposal returns a proposal by id.
func (c *Client) GetProposal(ctx context.Context, id uint64) (*govv1.Proposal, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	resp, err := c.raw.GetProposal(ctx, &govv1.GetProposalRequest{Id: &govv1.ProposalId{Value: id}})
	if err != nil {
		return nil, err
	}
	return resp.GetProposal(), nil
}

// ListProposals returns proposals filtered by status with pagination support.
func (c *Client) ListProposals(ctx context.Context, status string, pageSize uint32, pageToken string) ([]*govv1.Proposal, string, error) {
	if c == nil {
		return nil, "", grpc.ErrClientConnClosing
	}
	resp, err := c.raw.ListProposals(ctx, &govv1.ListProposalsRequest{
		StatusFilter: status,
		PageSize:     pageSize,
		PageToken:    pageToken,
	})
	if err != nil {
		return nil, "", err
	}
	return resp.GetProposals(), resp.GetNextPageToken(), nil
}

// SubmitVote casts a vote against the provided proposal.
func (c *Client) SubmitVote(ctx context.Context, voter string, proposalID uint64, option string) (*govv1.Vote, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	resp, err := c.raw.SubmitVote(ctx, &govv1.SubmitVoteRequest{
		Vote: &govv1.Vote{
			Voter:      voter,
			ProposalId: &govv1.ProposalId{Value: proposalID},
			Option:     option,
		},
	})
	if err != nil {
		return nil, err
	}
	return resp.GetVote(), nil
}
