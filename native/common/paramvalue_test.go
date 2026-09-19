package common

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestParamTextAcceptsBareAndQuotedSpellings(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"bare number", "20000000000000000000000", "20000000000000000000000"},
		{"quoted number", `"20000000000000000000000"`, "20000000000000000000000"},
		{"padded bare number", "  1250\n", "1250"},
		{"padded quoted number", " \t\"1250\"  ", "1250"},
		{"whitespace inside the quotes", `"  1250  "`, "1250"},
		{"bare boolean", "true", "true"},
		{"quoted boolean", `"true"`, "true"},
		{"quoted word", `"ZNHB"`, "ZNHB"},
		{"escaped quoted number", `"12\u0035"`, "125"},
		{"empty", "", ""},
		{"blank", "  \n\t", ""},
		{"empty quoted string", `""`, ""},
		{"lone quote is left alone", `"`, `"`},
		{"unterminated quote is left alone", `"123`, `"123`},
		{"trailing garbage after the string is left alone", `"123" x`, `"123" x`},
		{"quotes inside a bare word are left alone", `12"3`, `12"3`},
		{"non-numeric text", "abc", "abc"},
		{"signed text is untouched", "-5", "-5"},
	}
	for _, tt := range tests {
		if got := ParamText([]byte(tt.raw)); got != tt.want {
			t.Errorf("%s: ParamText(%q) = %q, want %q", tt.name, tt.raw, got, tt.want)
		}
	}
}

// TestParamTextMatchesTrimSpaceForUnquotedValues pins the compatibility claim
// the readers rely on: for anything that is not a JSON string, ParamText is
// exactly strings.TrimSpace(string(raw)), so swapping it into a reader cannot
// change what that reader used to accept.
func TestParamTextMatchesTrimSpaceForUnquotedValues(t *testing.T) {
	for _, raw := range []string{"0", "7", "1250", "+5", "-5", "007", "1e3", "1.5", "true", "false", "ZNHB", "", " ", "\n12\n", "{\"a\":1}", "[1,2]"} {
		if got, want := ParamText([]byte(raw)), strings.TrimSpace(raw); got != want {
			t.Errorf("ParamText(%q) = %q, want %q", raw, got, want)
		}
	}
}

// Governance's numeric validation strips one '+' and then hands the rest to
// big.Int.SetString, which accepts a sign of its own, so the quoted string
// "++5" passes proposal validation and is stored verbatim. Every reader must be
// able to read it back, including the ones that parse with strconv.ParseUint,
// which rejects any sign: the reader therefore gets the number with every
// leading plus removed.
func TestParamDecimalDropsEveryLeadingPlus(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"1250", "1250"},
		{`"1250"`, "1250"},
		{"+1250", "1250"},
		{`"+1250"`, "1250"},
		{`" +1250 "`, "1250"},
		{"++5", "5"},
		{`"++5"`, "5"},
		{`" ++5 "`, "5"},
		{"+++5", "5"},
		{`"++++++++5"`, "5"},
		{"+-5", "-5"},
		{"-5", "-5"},
		{`"-5"`, "-5"},
		{"+", ""},
		{"++", ""},
		{"", ""},
		{`""`, ""},
	}
	for _, tt := range tests {
		if got := ParamDecimal([]byte(tt.raw)); got != tt.want {
			t.Errorf("ParamDecimal(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

var paramNameCounter atomic.Uint64

// uniqueParamName returns a parameter name that no earlier call in this process
// used: a malformed value is reported once per process, so a fixed name would
// be swallowed on a repeated run (-count).
func uniqueParamName(prefix string) string {
	return fmt.Sprintf("%s.%d", prefix, paramNameCounter.Add(1))
}

type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]slog.Record(nil), h.records...)
}

func TestReportMalformedParamIsLoudAndDeduplicated(t *testing.T) {
	handler := &captureHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	name := uniqueParamName("test.paramvalue.report")
	raw := []byte(`"not-a-number"`)
	cause := errors.New("bad value")

	ReportMalformedParam(name, raw, cause, "42")
	ReportMalformedParam(name, raw, cause, "42")
	ReportMalformedParam(name, raw, cause, "42")
	records := handler.snapshot()
	if len(records) != 1 {
		t.Fatalf("expected the same malformed value to be reported once, got %d records", len(records))
	}
	if records[0].Level != slog.LevelError {
		t.Fatalf("expected an error-level record, got %s", records[0].Level)
	}
	attrs := map[string]string{}
	records[0].Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	if attrs["param"] != name || attrs["stored"] != string(raw) || attrs["default"] != "42" || !strings.Contains(attrs["error"], "bad value") {
		t.Fatalf("unexpected log attributes: %v", attrs)
	}

	// A different stored value is a different problem and is reported again.
	ReportMalformedParam(name, []byte("another"), cause, "42")
	if got := len(handler.snapshot()); got != 2 {
		t.Fatalf("expected a distinct malformed value to be reported, got %d records", got)
	}
}

func TestReportMalformedParamBoundsWhatItRemembers(t *testing.T) {
	handler := &captureHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	huge := bytes.Repeat([]byte("x"), 10*maxReportedParamBytes)
	ReportMalformedParam(uniqueParamName("test.paramvalue.huge"), huge, errors.New("bad"), "1")
	records := handler.snapshot()
	if len(records) != 1 {
		t.Fatalf("expected one record, got %d", len(records))
	}
	records[0].Attrs(func(a slog.Attr) bool {
		if a.Key == "stored" && len(a.Value.String()) > maxReportedParamBytes {
			t.Fatalf("logged %d bytes of the stored value, want at most %d", len(a.Value.String()), maxReportedParamBytes)
		}
		return true
	})
}
