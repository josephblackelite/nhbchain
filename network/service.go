package network

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"nhbchain/p2p"
	networkpb "nhbchain/proto/network"
)

// Service exposes the gRPC server that consensusd connects to.
type Service struct {
	networkpb.UnimplementedNetworkServer
	relay *Relay
}

// NewService wraps the provided relay for gRPC registration.
func NewService(relay *Relay) *Service {
	return &Service{relay: relay}
}

func (s *Service) Gossip(stream networkpb.Network_GossipServer) error {
	return s.relay.GossipStream(stream)
}

func (s *Service) GetView(ctx context.Context, _ *networkpb.GetViewRequest) (*networkpb.GetViewResponse, error) {
	view, listen, err := s.relay.View()
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return &networkpb.GetViewResponse{View: encodeView(view, listen)}, nil
}

func (s *Service) ListPeers(ctx context.Context, _ *networkpb.ListPeersRequest) (*networkpb.ListPeersResponse, error) {
	peers, err := s.relay.Peers()
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	encoded := make([]*networkpb.PeerNetInfo, 0, len(peers))
	for i := range peers {
		encoded = append(encoded, encodePeerNetInfo(&peers[i]))
	}
	return &networkpb.ListPeersResponse{Peers: encoded}, nil
}

func (s *Service) DialPeer(ctx context.Context, req *networkpb.DialPeerRequest) (*networkpb.DialPeerResponse, error) {
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
	return &networkpb.DialPeerResponse{}, nil
}

func (s *Service) BanPeer(ctx context.Context, req *networkpb.BanPeerRequest) (*networkpb.BanPeerResponse, error) {
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
	return &networkpb.BanPeerResponse{}, nil
}

func encodeView(view p2p.NetworkView, listen []string) *networkpb.NetworkView {
	boot := append([]string{}, view.Bootnodes...)
	persistent := append([]string{}, view.Persistent...)
	seeds := make([]*networkpb.SeedInfo, 0, len(view.Seeds))
	for i := range view.Seeds {
		seed := view.Seeds[i]
		seeds = append(seeds, &networkpb.SeedInfo{
			NodeId:    seed.NodeID,
			Address:   seed.Address,
			Source:    seed.Source,
			NotBefore: seed.NotBefore,
			NotAfter:  seed.NotAfter,
		})
	}
	return &networkpb.NetworkView{
		NetworkId:   view.NetworkID,
		GenesisHash: append([]byte(nil), []byte(view.Genesis)...),
		Counts: &networkpb.NetworkCounts{
			Total:    int32(view.Counts.Total),
			Inbound:  int32(view.Counts.Inbound),
			Outbound: int32(view.Counts.Outbound),
		},
		Limits: &networkpb.NetworkLimits{
			MaxPeers:       int32(view.Limits.MaxPeers),
			MaxInbound:     int32(view.Limits.MaxInbound),
			MaxOutbound:    int32(view.Limits.MaxOutbound),
			RateMsgsPerSec: view.Limits.Rate,
			Burst:          view.Limits.Burst,
			BanScore:       int32(view.Limits.BanScore),
			GreyScore:      int32(view.Limits.GreyScore),
		},
		Self: &networkpb.NetworkSelf{
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

func encodePeerNetInfo(info *p2p.PeerNetInfo) *networkpb.PeerNetInfo {
	if info == nil {
		return nil
	}
	response := &networkpb.PeerNetInfo{
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
