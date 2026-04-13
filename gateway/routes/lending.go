package routes

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	lendingv1 "nhbchain/proto/lending/v1"
)

const lendingRequestLimit = 1 << 20 // 1 MiB

// lendingRoutes wires HTTP handlers to the LendingService gRPC API.
type lendingRoutes struct {
	client    lendingv1.LendingServiceClient
	marshal   protojson.MarshalOptions
	unmarshal protojson.UnmarshalOptions
	timeout   time.Duration
}

func newLendingRoutes(target *url.URL) (*lendingRoutes, error) {
	if target == nil {
		return nil, fmt.Errorf("nil lending target")
	}
	conn, err := dialLending(target)
	if err != nil {
		return nil, fmt.Errorf("dial lending service: %w", err)
	}
	return &lendingRoutes{
		client:    lendingv1.NewLendingServiceClient(conn),
		marshal:   protojson.MarshalOptions{EmitUnpopulated: true},
		unmarshal: protojson.UnmarshalOptions{DiscardUnknown: true},
		timeout:   10 * time.Second,
	}, nil
}

func (lr *lendingRoutes) mount(r chi.Router) {
	r.Get("/markets", lr.listMarkets)
	r.Post("/markets/get", lr.getMarket)
	r.Post("/positions/get", lr.getPosition)
	r.Post("/supply", lr.supplyAsset)
	r.Post("/withdraw", lr.withdrawAsset)
	r.Post("/borrow", lr.borrowAsset)
	r.Post("/repay", lr.repayAsset)
}

func (lr *lendingRoutes) context(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := lr.timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return context.WithTimeout(parent, timeout)
}

func (lr *lendingRoutes) listMarkets(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := lr.context(r.Context())
	defer cancel()

	resp, err := lr.client.ListMarkets(ctx, &lendingv1.ListMarketsRequest{})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeProtoJSON(w, lr.marshal, resp)
}

func (lr *lendingRoutes) getMarket(w http.ResponseWriter, r *http.Request) {
	req := &lendingv1.GetMarketRequest{}
	if err := lr.decodeRequest(r, req); err != nil {
		writeBadRequest(w, err)
		return
	}

	ctx, cancel := lr.context(r.Context())
	defer cancel()

	resp, err := lr.client.GetMarket(ctx, req)
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeProtoJSON(w, lr.marshal, resp)
}

func (lr *lendingRoutes) getPosition(w http.ResponseWriter, r *http.Request) {
	req := &lendingv1.GetPositionRequest{}
	if err := lr.decodeRequest(r, req); err != nil {
		writeBadRequest(w, err)
		return
	}

	ctx, cancel := lr.context(r.Context())
	defer cancel()

	resp, err := lr.client.GetPosition(ctx, req)
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeProtoJSON(w, lr.marshal, resp)
}

func (lr *lendingRoutes) supplyAsset(w http.ResponseWriter, r *http.Request) {
	req := &lendingv1.SupplyAssetRequest{}
	if err := lr.decodeRequest(r, req); err != nil {
		writeBadRequest(w, err)
		return
	}

	ctx, cancel := lr.context(r.Context())
	defer cancel()

	resp, err := lr.client.SupplyAsset(ctx, req)
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeProtoJSON(w, lr.marshal, resp)
}

func (lr *lendingRoutes) withdrawAsset(w http.ResponseWriter, r *http.Request) {
	req := &lendingv1.WithdrawAssetRequest{}
	if err := lr.decodeRequest(r, req); err != nil {
		writeBadRequest(w, err)
		return
	}

	ctx, cancel := lr.context(r.Context())
	defer cancel()

	resp, err := lr.client.WithdrawAsset(ctx, req)
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeProtoJSON(w, lr.marshal, resp)
}

func (lr *lendingRoutes) borrowAsset(w http.ResponseWriter, r *http.Request) {
	req := &lendingv1.BorrowAssetRequest{}
	if err := lr.decodeRequest(r, req); err != nil {
		writeBadRequest(w, err)
		return
	}

	ctx, cancel := lr.context(r.Context())
	defer cancel()

	resp, err := lr.client.BorrowAsset(ctx, req)
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeProtoJSON(w, lr.marshal, resp)
}

func (lr *lendingRoutes) repayAsset(w http.ResponseWriter, r *http.Request) {
	req := &lendingv1.RepayAssetRequest{}
	if err := lr.decodeRequest(r, req); err != nil {
		writeBadRequest(w, err)
		return
	}

	ctx, cancel := lr.context(r.Context())
	defer cancel()

	resp, err := lr.client.RepayAsset(ctx, req)
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeProtoJSON(w, lr.marshal, resp)
}

func (lr *lendingRoutes) decodeRequest(r *http.Request, msg proto.Message) error {
	if r.Body == nil {
		return errors.New("missing request body")
	}
	defer r.Body.Close()

	reader := io.LimitReader(r.Body, lendingRequestLimit)
	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("read request body: %w", err)
	}
	if len(data) == 0 {
		return errors.New("request body is empty")
	}
	if err := lr.unmarshal.Unmarshal(data, msg); err != nil {
		return fmt.Errorf("decode request: %w", err)
	}
	return nil
}

func writeProtoJSON(w http.ResponseWriter, marshal protojson.MarshalOptions, msg proto.Message) {
	w.Header().Set("Content-Type", "application/json")
	payload, err := marshal.Marshal(msg)
	if err != nil {
		writeInternalError(w, fmt.Errorf("marshal response: %w", err))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func writeBadRequest(w http.ResponseWriter, err error) {
	writeJSONError(w, http.StatusBadRequest, err)
}

func writeInternalError(w http.ResponseWriter, err error) {
	writeJSONError(w, http.StatusInternalServerError, err)
}

func writeJSONError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = http.StatusText(status)
	}
	payload, marshalErr := json.Marshal(map[string]string{"error": message})
	if marshalErr != nil {
		replacer := strings.NewReplacer(
			"\\", "\\\\",
			"\"", "\\\"",
			"\n", "\\n",
			"\r", "\\r",
			"\t", "\\t",
		)
		fallback := fmt.Sprintf("{\"error\":\"%s\"}", replacer.Replace(message))
		payload = []byte(fallback)
	}
	_, _ = w.Write(payload)
}

func writeGRPCError(w http.ResponseWriter, err error) {
	if err == nil {
		writeInternalError(w, errors.New("unknown gRPC error"))
		return
	}
	st, ok := grpcstatus.FromError(err)
	if !ok {
		writeInternalError(w, err)
		return
	}
	statusCode := mapGRPCCode(st.Code())
	writeJSONError(w, statusCode, errors.New(st.Message()))
}

func mapGRPCCode(code codes.Code) int {
	switch code {
	case codes.OK:
		return http.StatusOK
	case codes.InvalidArgument, codes.FailedPrecondition, codes.OutOfRange:
		return http.StatusBadRequest
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.NotFound:
		return http.StatusNotFound
	case codes.AlreadyExists, codes.Aborted:
		return http.StatusConflict
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	case codes.DeadlineExceeded:
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}

func dialLending(target *url.URL) (*grpc.ClientConn, error) {
	scheme := strings.ToLower(target.Scheme)
	host := target.Hostname()
	if host == "" {
		return nil, fmt.Errorf("lending target host is empty")
	}
	port := target.Port()
	if port == "" {
		switch scheme {
		case "https", "grpcs":
			port = "443"
		case "http", "grpc":
			port = "80"
		}
	}
	address := target.Host
	if port != "" {
		address = net.JoinHostPort(host, port)
	}

	var creds credentials.TransportCredentials
	switch scheme {
	case "https", "grpcs":
		tlsConfig := &tls.Config{ServerName: host}
		creds = credentials.NewTLS(tlsConfig)
	case "http", "grpc":
		creds = insecure.NewCredentials()
	default:
		return nil, fmt.Errorf("unsupported lending target scheme %q", target.Scheme)
	}

	return grpc.Dial(address, grpc.WithTransportCredentials(creds))
}
