package modules

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"nhbchain/consensus/potso/evidence"
	"nhbchain/core"
	"nhbchain/crypto"
)

type PotsoEvidenceModule struct {
	node *core.Node
}

func NewPotsoEvidenceModule(node *core.Node) *PotsoEvidenceModule {
	return &PotsoEvidenceModule{node: node}
}

type submitEvidenceParams struct {
	Type        string          `json:"type"`
	Offender    string          `json:"offender"`
	Heights     []uint64        `json:"heights"`
	Reporter    string          `json:"reporter"`
	ReporterSig string          `json:"reporterSig"`
	Details     json.RawMessage `json:"details,omitempty"`
	Timestamp   int64           `json:"timestamp"`
}

type SubmitEvidenceResult struct {
	Hash   string `json:"hash"`
	Status string `json:"status"`
}

type getEvidenceParams struct {
	Hash string `json:"hash"`
}

type EvidenceRecord struct {
	Hash        string          `json:"hash"`
	Type        string          `json:"type"`
	Offender    string          `json:"offender"`
	Heights     []uint64        `json:"heights"`
	Details     json.RawMessage `json:"details,omitempty"`
	Reporter    string          `json:"reporter"`
	ReporterSig string          `json:"reporterSig"`
	Timestamp   int64           `json:"timestamp"`
	ReceivedAt  int64           `json:"receivedAt"`
}

type listEvidenceParams struct {
	Offender   string           `json:"offender,omitempty"`
	Type       string           `json:"type,omitempty"`
	FromHeight *uint64          `json:"fromHeight,omitempty"`
	ToHeight   *uint64          `json:"toHeight,omitempty"`
	Page       *listPageRequest `json:"page,omitempty"`
}

type listPageRequest struct {
	Offset *int `json:"offset,omitempty"`
	Limit  *int `json:"limit,omitempty"`
}

type ListEvidenceResult struct {
	Records    []EvidenceRecord `json:"records"`
	NextOffset *int             `json:"nextOffset,omitempty"`
}

func (m *PotsoEvidenceModule) Submit(raw json.RawMessage) (*SubmitEvidenceResult, *ModuleError) {
	if m == nil || m.node == nil {
		return nil, &ModuleError{HTTPStatus: http.StatusInternalServerError, Code: codeServerError, Message: "evidence module not initialised"}
	}
	var params submitEvidenceParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "invalid parameter object", Data: err.Error()}
	}
	if strings.TrimSpace(params.Type) == "" {
		return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "type is required"}
	}
	evType, err := evidence.ParseType(params.Type)
	if err != nil {
		return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: err.Error()}
	}
	offender, err := decodeBech32(strings.TrimSpace(params.Offender))
	if err != nil {
		return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "invalid offender", Data: err.Error()}
	}
	reporter, err := decodeBech32(strings.TrimSpace(params.Reporter))
	if err != nil {
		return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "invalid reporter", Data: err.Error()}
	}
	sigBytes, err := decodeHexString(params.ReporterSig)
	if err != nil {
		return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "invalid reporterSig", Data: err.Error()}
	}
	evidencePayload := evidence.Evidence{
		Type:        evType,
		Offender:    offender,
		Heights:     append([]uint64(nil), params.Heights...),
		Details:     append([]byte(nil), params.Details...),
		Reporter:    reporter,
		ReporterSig: sigBytes,
		Timestamp:   params.Timestamp,
	}
	receipt, err := m.node.PotsoSubmitEvidence(evidencePayload)
	if err != nil {
		return nil, &ModuleError{HTTPStatus: http.StatusInternalServerError, Code: codeServerError, Message: err.Error()}
	}
	if receipt == nil {
		return nil, &ModuleError{HTTPStatus: http.StatusInternalServerError, Code: codeServerError, Message: "empty receipt"}
	}
	if receipt.Status == evidence.ReceiptStatusRejected {
		message := "evidence rejected"
		if receipt.Reason != nil && receipt.Reason.Message != "" {
			message = receipt.Reason.Message
		}
		data := map[string]string{"hash": formatHash(receipt.Hash)}
		return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: message, Data: data}
	}
	result := &SubmitEvidenceResult{Hash: formatHash(receipt.Hash), Status: string(receipt.Status)}
	return result, nil
}

func (m *PotsoEvidenceModule) Get(raw json.RawMessage) (*EvidenceRecord, *ModuleError) {
	if m == nil || m.node == nil {
		return nil, &ModuleError{HTTPStatus: http.StatusInternalServerError, Code: codeServerError, Message: "evidence module not initialised"}
	}
	var params getEvidenceParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "invalid parameter object", Data: err.Error()}
	}
	hash, err := decodeEvidenceHash(params.Hash)
	if err != nil {
		return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "invalid hash", Data: err.Error()}
	}
	record, ok, err := m.node.PotsoEvidenceByHash(hash)
	if err != nil {
		return nil, &ModuleError{HTTPStatus: http.StatusInternalServerError, Code: codeServerError, Message: err.Error()}
	}
	if !ok || record == nil {
		return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "evidence not found"}
	}
	dto := convertRecord(record)
	return &dto, nil
}

func (m *PotsoEvidenceModule) List(raw json.RawMessage) (*ListEvidenceResult, *ModuleError) {
	if m == nil || m.node == nil {
		return nil, &ModuleError{HTTPStatus: http.StatusInternalServerError, Code: codeServerError, Message: "evidence module not initialised"}
	}
	var params listEvidenceParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "invalid parameter object", Data: err.Error()}
		}
	}
	var filter evidence.Filter
	if strings.TrimSpace(params.Offender) != "" {
		offender, err := decodeBech32(params.Offender)
		if err != nil {
			return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "invalid offender", Data: err.Error()}
		}
		filter.Offender = &offender
	}
	if strings.TrimSpace(params.Type) != "" {
		evType, err := evidence.ParseType(params.Type)
		if err != nil {
			return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: err.Error()}
		}
		filter.Type = evType
	}
	if params.FromHeight != nil {
		filter.FromHeight = params.FromHeight
	}
	if params.ToHeight != nil {
		filter.ToHeight = params.ToHeight
	}
	if params.Page != nil {
		if params.Page.Offset != nil {
			filter.Offset = *params.Page.Offset
		}
		if params.Page.Limit != nil {
			filter.Limit = *params.Page.Limit
		}
	}
	records, nextOffset, err := m.node.PotsoEvidenceList(filter)
	if err != nil {
		return nil, &ModuleError{HTTPStatus: http.StatusInternalServerError, Code: codeServerError, Message: err.Error()}
	}
	result := &ListEvidenceResult{Records: make([]EvidenceRecord, 0, len(records))}
	for _, record := range records {
		dto := convertRecord(record)
		result.Records = append(result.Records, dto)
	}
	if nextOffset >= 0 {
		result.NextOffset = &nextOffset
	}
	return result, nil
}

func convertRecord(record *evidence.Record) EvidenceRecord {
	if record == nil {
		return EvidenceRecord{}
	}
	offender := formatAddress(record.Evidence.Offender)
	reporter := formatAddress(record.Evidence.Reporter)
	var details json.RawMessage
	if len(record.Evidence.Details) > 0 {
		details = append(json.RawMessage(nil), record.Evidence.Details...)
	}
	return EvidenceRecord{
		Hash:        formatHash(record.Hash),
		Type:        string(record.Evidence.Type),
		Offender:    offender,
		Heights:     append([]uint64(nil), record.Evidence.Heights...),
		Details:     details,
		Reporter:    reporter,
		ReporterSig: "0x" + hex.EncodeToString(record.Evidence.ReporterSig),
		Timestamp:   record.Evidence.Timestamp,
		ReceivedAt:  record.ReceivedAt,
	}
}

func decodeBech32(value string) ([20]byte, error) {
	var out [20]byte
	decoded, err := crypto.DecodeAddress(strings.TrimSpace(value))
	if err != nil {
		return out, err
	}
	copy(out[:], decoded.Bytes())
	return out, nil
}

func decodeHexString(value string) ([]byte, error) {
	cleaned := strings.TrimSpace(value)
	cleaned = strings.TrimPrefix(cleaned, "0x")
	if cleaned == "" {
		return nil, fmt.Errorf("hex value required")
	}
	if len(cleaned)%2 == 1 {
		cleaned = "0" + cleaned
	}
	return hex.DecodeString(cleaned)
}

func decodeEvidenceHash(value string) ([32]byte, error) {
	var out [32]byte
	bytes, err := decodeHexString(value)
	if err != nil {
		return out, err
	}
	if len(bytes) != len(out) {
		return out, fmt.Errorf("hash must be %d bytes", len(out))
	}
	copy(out[:], bytes)
	return out, nil
}

func formatHash(hash [32]byte) string {
	return "0x" + hex.EncodeToString(hash[:])
}

func formatAddress(addr [20]byte) string {
	return crypto.NewAddress(crypto.NHBPrefix, addr[:]).String()
}
