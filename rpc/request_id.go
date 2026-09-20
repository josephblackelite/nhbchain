package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// UnmarshalJSON reads a request and its id as JSON-RPC 2.0 allows: a number, a
// string or null. The id used to be an int, so a request carrying a string id
// (or a number that was not an integer) was refused as unparseable, and a
// client that identifies its calls by string could not use the node at all.
//
// The id is kept as it came, so the response echoes it: an integer stays an int,
// a string stays a string, and any other number keeps its exact text. A request
// with no id, or a null one, is answered with the id 0, as it always has been.
func (r *RPCRequest) UnmarshalJSON(data []byte) error {
	var aux struct {
		JSONRPC string            `json:"jsonrpc"`
		Method  string            `json:"method"`
		Params  []json.RawMessage `json:"params"`
		ID      json.RawMessage   `json:"id"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	id, err := decodeRequestID(aux.ID)
	if err != nil {
		return err
	}
	r.JSONRPC, r.Method, r.Params, r.ID = aux.JSONRPC, aux.Method, aux.Params, id
	return nil
}

func decodeRequestID(raw json.RawMessage) (interface{}, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0, nil
	}
	switch trimmed[0] {
	case '"':
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, err
		}
		return text, nil
	case '{', '[', 't', 'f':
		return nil, fmt.Errorf("id must be a number or a string")
	}
	var whole int
	if err := json.Unmarshal(trimmed, &whole); err == nil {
		return whole, nil
	}
	var number json.Number
	if err := json.Unmarshal(trimmed, &number); err != nil {
		return nil, err
	}
	return number, nil
}
