package common

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
)

// maxReportedParamBytes bounds how much of a stored value is logged and
// remembered when a malformed parameter is reported.
const maxReportedParamBytes = 256

// ParamText returns the text of a governed parameter as it is stored on chain.
//
// A param.update proposal is validated leniently but persisted verbatim, so
// the same number can be stored bare (123) or as a quoted decimal string
// ("123"), and a boolean as true or "true". ParamText trims surrounding
// whitespace and, when the whole value is a JSON string, returns that
// string's trimmed content, so every reader parses both spellings the same
// way. Any other value is returned trimmed and otherwise unchanged, which
// makes it a drop-in replacement for strings.TrimSpace(string(raw)).
func ParamText(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if len(text) >= 2 && text[0] == '"' {
		var unquoted string
		if err := json.Unmarshal([]byte(text), &unquoted); err == nil {
			return strings.TrimSpace(unquoted)
		}
	}
	return text
}

// ParamDecimal is ParamText for a numeric parameter. It also drops one
// leading '+', which governance's own numeric validation accepts (the string
// "+5" passes it), so every spelling a proposal may carry for a number can be
// read back by the code that consumes it.
func ParamDecimal(raw []byte) string {
	return strings.TrimPrefix(ParamText(raw), "+")
}

var reportedMalformedParams sync.Map

// ReportMalformedParam logs, loudly, that the stored value of a governed
// parameter could not be used and that its reader fell back to fallback
// instead. It is only for values that governance's own validation cannot have
// produced (corrupt or foreign data): the caller degrades deterministically
// from the stored bytes alone, and this call has no effect on state or on any
// transaction's outcome. Each distinct (name, value) pair is reported once per
// process so a hot path that re-reads the value on every account write cannot
// flood the log.
func ReportMalformedParam(name string, raw []byte, err error, fallback string) {
	if len(raw) > maxReportedParamBytes {
		raw = raw[:maxReportedParamBytes]
	}
	if _, seen := reportedMalformedParams.LoadOrStore(name+"\x00"+string(raw), struct{}{}); seen {
		return
	}
	slog.Error("governed parameter has a malformed stored value; using the default instead",
		slog.String("param", name),
		slog.String("stored", string(raw)),
		slog.Any("error", err),
		slog.String("default", fallback))
}
