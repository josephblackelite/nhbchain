package gov

import (
	"context"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	govv1 "nhbchain/proto/gov/v1"
)

// Client wraps the governance query and transaction clients with ergonomic helpers.
type Client struct {
	conn  *grpc.ClientConn
	query govv1.QueryClient
	msg   govv1.MsgClient
}

// Dial connects to a governance service endpoint.
func Dial(ctx context.Context, target string, opts ...grpc.DialOption) (*Client, error) {
	if len(opts) == 0 {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	opts = append(opts,
		grpc.WithChainUnaryInterceptor(otelgrpc.UnaryClientInterceptor()),
		grpc.WithChainStreamInterceptor(otelgrpc.StreamClientInterceptor()),
	)
	conn, err := grpc.DialContext(ctx, target, opts...)
	if err != nil {
		return nil, err
	}
	return New(conn), nil
}

// New creates a typed client from an existing connection.
func New(conn *grpc.ClientConn) *Client {
	return &Client{
		conn:  conn,
		query: govv1.NewQueryClient(conn),
		msg:   govv1.NewMsgClient(conn),
	}
}

// Close tears down the underlying connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Query exposes the generated query client for advanced integrations.
func (c *Client) Query() govv1.QueryClient {
	if c == nil {
		return nil
	}
	return c.query
}

// Msg exposes the generated transaction client for advanced integrations.
func (c *Client) Msg() govv1.MsgClient {
	if c == nil {
		return nil
	}
	return c.msg
}

// GetProposal fetches a proposal by identifier.
func (c *Client) GetProposal(ctx context.Context, id uint64) (*govv1.Proposal, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	resp, err := c.query.GetProposal(ctx, &govv1.GetProposalRequest{Id: id})
	if err != nil {
		return nil, err
	}
	return resp.GetProposal(), nil
}

// ListProposals retrieves proposals using cursor-based pagination. The returned
// next token should be supplied verbatim to continue iteration.
func (c *Client) ListProposals(ctx context.Context, status govv1.ProposalStatus, pageSize uint32, pageToken string) ([]*govv1.Proposal, string, error) {
	if c == nil {
		return nil, "", grpc.ErrClientConnClosing
	}
	req := &govv1.ListProposalsRequest{
		PageSize:  pageSize,
		PageToken: pageToken,
	}
	if status != govv1.ProposalStatus_PROPOSAL_STATUS_UNSPECIFIED {
		req.StatusFilter = status
	}
	resp, err := c.query.ListProposals(ctx, req)
	if err != nil {
		return nil, "", err
	}
	return resp.GetProposals(), resp.GetNextPageToken(), nil
}

// GetTally retrieves the current tally for the supplied proposal identifier.
func (c *Client) GetTally(ctx context.Context, id uint64) (*govv1.ProposalTally, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	resp, err := c.query.GetTally(ctx, &govv1.GetTallyRequest{Id: id})
	if err != nil {
		return nil, err
	}
	return resp.GetTally(), nil
}

// SubmitProposal broadcasts a governance proposal transaction and returns the tx hash.
func (c *Client) SubmitProposal(ctx context.Context, msg *govv1.MsgSubmitProposal) (string, error) {
	if c == nil {
		return "", grpc.ErrClientConnClosing
	}
	resp, err := c.msg.SubmitProposal(ctx, msg)
	if err != nil {
		return "", err
	}
	return resp.GetTxHash(), nil
}

// Vote broadcasts a vote transaction.
func (c *Client) Vote(ctx context.Context, msg *govv1.MsgVote) (string, error) {
	if c == nil {
		return "", grpc.ErrClientConnClosing
	}
	resp, err := c.msg.Vote(ctx, msg)
	if err != nil {
		return "", err
	}
	return resp.GetTxHash(), nil
}

// Deposit broadcasts an additional deposit transaction for a proposal.
func (c *Client) Deposit(ctx context.Context, msg *govv1.MsgDeposit) (string, error) {
	if c == nil {
		return "", grpc.ErrClientConnClosing
	}
	resp, err := c.msg.Deposit(ctx, msg)
	if err != nil {
		return "", err
	}
	return resp.GetTxHash(), nil
}

// SetPauses broadcasts a pause toggle transaction.
func (c *Client) SetPauses(ctx context.Context, msg *govv1.MsgSetPauses) (string, error) {
	if c == nil {
		return "", grpc.ErrClientConnClosing
	}
	resp, err := c.msg.SetPauses(ctx, msg)
	if err != nil {
		return "", err
	}
	return resp.GetTxHash(), nil
}
