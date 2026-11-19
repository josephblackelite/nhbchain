package p2p

import "nhbchain/observability/logging"

// validConfigDigest reports whether v is a digest a peer may have sent: exactly 64
// lower-case hex characters. Whatever else arrives in the field is treated as no
// digest, and is never logged.
func validConfigDigest(v string) bool {
	if len(v) != 64 {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// checkPeerConfigDigest compares the digest of consensus settings a peer sent with
// this node's own and says, in the log, when they differ. It returns whether they
// did. It does nothing when either side has no digest, and it never refuses,
// disconnects or penalises the peer: the field is outside the signature, so it is
// evidence for an operator, not a basis for a decision. The digest values are not
// logged, only the fact.
func (s *Server) checkPeerConfigDigest(peerID, remote string) bool {
	own := s.cfg.ConsensusConfigDigest
	if !validConfigDigest(own) || !validConfigDigest(remote) || own == remote {
		return false
	}
	s.log().Error("Peer runs different consensus settings than this node: blocks may be accepted by one and refused by the other. Compare the start-up digest lines of both nodes (nhb-snapshot check-parity).",
		logging.MaskField("peer_id", peerID))
	return true
}
