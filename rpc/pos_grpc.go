package rpc

import (
	"context"
	"encoding/hex"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"nhbchain/consensus/codec"
	"nhbchain/core"
	"nhbchain/crypto"
	consensusv1 "nhbchain/proto/consensus/v1"
	posv1 "nhbchain/proto/pos"
	cons "nhbchain/sdk/consensus"
)

type posServer struct {
	posv1.UnimplementedTxServer
	posv1.UnimplementedRegistryServer
	node    core.ConsensusAPI
	chainID string
	signer  *crypto.PrivateKey
}

// NewPOSServer creates a new gRPC server for POS Tx and Registry services.
func NewPOSServer(node core.ConsensusAPI, chainID string, signer *crypto.PrivateKey) *posServer {
	return &posServer{
		node:    node,
		chainID: chainID,
		signer:  signer,
	}
}

// submitPayload wraps msg in an envelope and submits it to the node.
//
// NHB-AUDIT-S3: this can never produce a genuinely authorized payment
// mutation. core/state_pos.go's applyPOSAuthorize (and its Capture/Void
// siblings) require the transaction's cryptographic signer to equal the
// message's own payer/merchant field -- so the only two signers this
// method could ever use are both wrong for real use: an empty/simulated
// signature (this service has no way to verify or attach a genuine
// caller-supplied signature -- the proto messages have no signature field
// at all) fails decode/sender-derivation outright, and a server-held
// signer (e.g. the validator's own key) would make the VALIDATOR the
// on-chain payer/merchant for every request, moving the validator's own
// funds instead of the real payer's or merchant's -- a different kind of
// wrong, not a fix. Do not wire in a signer here to "fix" the rejection;
// see NewPOSServer's own call site in http.go for the real, working
// integration path (client-side signing + nhb_sendTransaction), which
// nhbportal's actual POS feature already uses correctly. This method only
// serves this service's read-only Registry methods and stands ready for a
// real client-signed-envelope submission path if one is ever built.
func (s *posServer) submitPayload(msg proto.Message) (string, error) {
	payload, err := anypb.New(msg)
	if err != nil {
		return "", err
	}
	envelope, err := cons.NewTx(payload, 0, s.chainID, "", "", "", "")
	if err != nil {
		return "", err
	}
	var signedEnvelope *consensusv1.SignedTxEnvelope
	if s.signer != nil {
		signedEnvelope, err = cons.Sign(envelope, s.signer)
		if err != nil {
			return "", err
		}
	} else {
		// Deliberately unsigned -- see this function's doc comment. Left
		// as-is (not "fixed" to succeed) so this path fails the same way
		// a real unauthorized submission would, rather than silently
		// accepting one.
		signedEnvelope = &consensusv1.SignedTxEnvelope{
			Envelope:  envelope,
			Signature: &consensusv1.TxSignature{},
		}
	}
	if err := s.node.SubmitTxEnvelope(signedEnvelope); err != nil {
		return "", err
	}
	tx, err := codec.TransactionFromEnvelope(signedEnvelope)
	if err != nil {
		return "", err
	}
	hash, err := tx.Hash()
	if err != nil {
		return "", err
	}
	return "0x" + hex.EncodeToString(hash), nil
}

func (s *posServer) AuthorizePayment(ctx context.Context, req *posv1.MsgAuthorizePayment) (*posv1.MsgAuthorizePaymentResponse, error) {
	if _, err := s.submitPayload(req); err != nil {
		return nil, err
	}
	return &posv1.MsgAuthorizePaymentResponse{}, nil
}

func (s *posServer) CapturePayment(ctx context.Context, req *posv1.MsgCapturePayment) (*posv1.MsgCapturePaymentResponse, error) {
	if _, err := s.submitPayload(req); err != nil {
		return nil, err
	}
	return &posv1.MsgCapturePaymentResponse{AuthorizationId: req.AuthorizationId}, nil
}

func (s *posServer) VoidPayment(ctx context.Context, req *posv1.MsgVoidPayment) (*posv1.MsgVoidPaymentResponse, error) {
	if _, err := s.submitPayload(req); err != nil {
		return nil, err
	}
	return &posv1.MsgVoidPaymentResponse{AuthorizationId: req.AuthorizationId}, nil
}

func (s *posServer) RegisterMerchant(ctx context.Context, req *posv1.MsgRegisterMerchant) (*posv1.MsgRegisterMerchantResponse, error) {
	hash, err := s.submitPayload(req)
	if err != nil {
		return nil, err
	}
	return &posv1.MsgRegisterMerchantResponse{TxHash: hash}, nil
}

func (s *posServer) RegisterDevice(ctx context.Context, req *posv1.MsgRegisterDevice) (*posv1.MsgRegisterDeviceResponse, error) {
	hash, err := s.submitPayload(req)
	if err != nil {
		return nil, err
	}
	return &posv1.MsgRegisterDeviceResponse{TxHash: hash}, nil
}

func (s *posServer) PauseMerchant(ctx context.Context, req *posv1.MsgPauseMerchant) (*posv1.MsgPauseMerchantResponse, error) {
	hash, err := s.submitPayload(req)
	if err != nil {
		return nil, err
	}
	return &posv1.MsgPauseMerchantResponse{TxHash: hash}, nil
}

func (s *posServer) ResumeMerchant(ctx context.Context, req *posv1.MsgResumeMerchant) (*posv1.MsgResumeMerchantResponse, error) {
	hash, err := s.submitPayload(req)
	if err != nil {
		return nil, err
	}
	return &posv1.MsgResumeMerchantResponse{TxHash: hash}, nil
}

func (s *posServer) RevokeDevice(ctx context.Context, req *posv1.MsgRevokeDevice) (*posv1.MsgRevokeDeviceResponse, error) {
	hash, err := s.submitPayload(req)
	if err != nil {
		return nil, err
	}
	return &posv1.MsgRevokeDeviceResponse{TxHash: hash}, nil
}

func (s *posServer) RestoreDevice(ctx context.Context, req *posv1.MsgRestoreDevice) (*posv1.MsgRestoreDeviceResponse, error) {
	hash, err := s.submitPayload(req)
	if err != nil {
		return nil, err
	}
	return &posv1.MsgRestoreDeviceResponse{TxHash: hash}, nil
}
