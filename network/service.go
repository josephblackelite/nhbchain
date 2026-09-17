package network

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"nhbchain/p2p"
	networkv1 "nhbchain/proto/network/v1"
)

// Service exposes the gRPC server that consensusd connects to.
type Service struct {
	networkv1.UnimplementedNetworkServiceServer
	relay               *Relay
	writeAuth           Authenticator
	readAuth            Authenticator
	allowAnonymousReads bool
}

// NewService wraps the provided relay for gRPC registration.
func NewService(relay *Relay, auth Authenticator, opts ...ServiceOption) (*Service, error) {
	svc := &Service{relay: relay, writeAuth: auth, readAuth: auth}
	for _, opt := range opts {
		if opt != nil {
			opt(svc)
		}
	}
	if svc.readAuth == nil && !svc.allowAnonymousReads {
		return nil, fmt.Errorf("network: read authenticator required unless anonymous reads are explicitly allowed")
	}
	return svc, nil
}

// ServiceOption mutates service construction defaults.
type ServiceOption func(*Service)

// WithReadAuthenticator overrides the authenticator used for read-only RPCs.
func WithReadAuthenticator(auth Authenticator) ServiceOption {
	return func(s *Service) {
		if s != nil {
			s.readAuth = auth
		}
	}
}

// WithAllowUnauthenticatedReads toggles acceptance of anonymous read RPCs.
func WithAllowUnauthenticatedReads(allow bool) ServiceOption {
	return func(s *Service) {
		if s != nil {
			s.allowAnonymousReads = allow
		}
	}
}

func (s *Service) Gossip(stream networkv1.NetworkService_GossipServer) error {
	if err := s.authorize(stream.Context()); err != nil {
		return err
	}
	return s.relay.GossipStream(stream)
}

func (s *Service) GetView(ctx context.Context, _ *networkv1.GetViewRequest) (*networkv1.GetViewResponse, error) {
	if err := s.authorizeRead(ctx); err != nil {
		return nil, err
	}
	view, listen, err := s.relay.View()
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return &networkv1.GetViewResponse{View: encodeView(view, listen)}, nil
}

func (s *Service) ListPeers(ctx context.Context, _ *networkv1.ListPeersRequest) (*networkv1.ListPeersResponse, error) {
	if err := s.authorizeRead(ctx); err != nil {
		return nil, err
	}
	peers, err := s.relay.Peers()
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	encoded := make([]*networkv1.PeerNetInfo, 0, len(peers))
	for i := range peers {
		encoded = append(encoded, encodePeerNetInfo(&peers[i]))
	}
	return &networkv1.ListPeersResponse{Peers: encoded}, nil
}

func (s *Service) DialPeer(ctx context.Context, req *networkv1.DialPeerRequest) (*networkv1.DialPeerResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "missing request")
	}
	if err := s.relay.Dial(req.GetTarget()); err != nil {
		switch {
		case errors.Is(err, p2p.ErrInvalidAddress), errors.Is(err, p2p.ErrDialTargetEmpty):
			return nil, status.Error(codes.InvalidArgument, err.Error())
		case errors.Is(err, p2p.ErrPeerUnknown):
			return nil, status.Error(codes.NotFound, err.Error())
		case errors.Is(err, p2p.ErrPeerBanned):
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		default:
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	return &networkv1.DialPeerResponse{}, nil
}

func (s *Service) BanPeer(ctx context.Context, req *networkv1.BanPeerRequest) (*networkv1.BanPeerResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "missing request")
	}
	duration := time.Duration(req.GetSeconds()) * time.Second
	if duration < 0 {
		duration = 0
	}
	if err := s.relay.Ban(req.GetNodeId(), duration); err != nil {
		switch {
		case errors.Is(err, p2p.ErrPeerUnknown):
			return nil, status.Error(codes.NotFound, err.Error())
		default:
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	return &networkv1.BanPeerResponse{}, nil
}

func (s *Service) authorize(ctx context.Context) error {
	if s == nil || s.writeAuth == nil {
		return nil
	}
	return s.writeAuth.Authorize(ctx)
}

func (s *Service) authorizeRead(ctx context.Context) error {
	if s == nil || s.readAuth == nil {
		return nil
	}
	return s.readAuth.Authorize(ctx)
}

func encodeView(view p2p.NetworkView, listen []string) *networkv1.NetworkView {
	boot := append([]string{}, view.Bootnodes...)
	persistent := append([]string{}, view.Persistent...)
	seeds := make([]*networkv1.SeedInfo, 0, len(view.Seeds))
	for i := range view.Seeds {
		seed := view.Seeds[i]
		seeds = append(seeds, &networkv1.SeedInfo{
			NodeId:    seed.NodeID,
			Address:   seed.Address,
			Source:    seed.Source,
			NotBefore: seed.NotBefore,
			NotAfter:  seed.NotAfter,
		})
	}
	return &networkv1.NetworkView{
		NetworkId:   view.NetworkID,
		GenesisHash: append([]byte(nil), []byte(view.Genesis)...),
		Counts: &networkv1.NetworkCounts{
			Total:    int32(view.Counts.Total),
			Inbound:  int32(view.Counts.Inbound),
			Outbound: int32(view.Counts.Outbound),
		},
		Limits: &networkv1.NetworkLimits{
			MaxPeers:       int32(view.Limits.MaxPeers),
			MaxInbound:     int32(view.Limits.MaxInbound),
			MaxOutbound:    int32(view.Limits.MaxOutbound),
			RateMsgsPerSec: view.Limits.Rate,
			Burst:          view.Limits.Burst,
			BanScore:       int32(view.Limits.BanScore),
			GreyScore:      int32(view.Limits.GreyScore),
		},
		Self: &networkv1.NetworkSelf{
			NodeId:          view.Self.NodeID,
			ProtocolVersion: view.Self.ProtocolVersion,
			ClientVersion:   view.Self.ClientVersion,
		},
		Bootnodes:       boot,
		PersistentPeers: persistent,
		Seeds:           seeds,
		ListenAddrs:     append([]string{}, listen...),
	}
}

func encodePeerNetInfo(info *p2p.PeerNetInfo) *networkv1.PeerNetInfo {
	if info == nil {
		return nil
	}
	response := &networkv1.PeerNetInfo{
		NodeId:       info.NodeID,
		Address:      info.Address,
		Direction:    info.Direction,
		State:        info.State,
		Score:        int32(info.Score),
		LastSeenUnix: info.LastSeen.Unix(),
		Fails:        int32(info.Fails),
	}
	if !info.BannedUntil.IsZero() {
		response.BannedUntilUnix = info.BannedUntil.Unix()
	}
	return response
}
