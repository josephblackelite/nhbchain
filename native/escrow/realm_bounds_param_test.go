package escrow

import "testing"

// DC-02 regression: escrow.realm.MinThreshold / MaxThreshold are persisted
// exactly as the param.update proposal spelled them, and proposal validation
// accepts a quoted decimal string as well as a bare number. The reader must
// read both.
func TestRealmBoundsAcceptBareAndQuotedThresholdParams(t *testing.T) {
	spellings := func(v string) []string {
		return []string{v, `"` + v + `"`, " " + v + " ", `" ` + v + ` "`, `"+` + v + `"`}
	}
	for i, minSpelling := range spellings("2") {
		maxSpelling := spellings("5")[i]

		state := newMockState()
		state.params[ParamKeyRealmMinThreshold] = []byte(minSpelling)
		state.params[ParamKeyRealmMaxThreshold] = []byte(maxSpelling)
		min, max, allowed, err := newTestEngine(state).realmBounds()
		if err != nil {
			t.Fatalf("spellings %q/%q: %v", minSpelling, maxSpelling, err)
		}
		if min != 2 || max != 5 {
			t.Fatalf("spellings %q/%q: got min=%d max=%d, want 2 and 5", minSpelling, maxSpelling, min, max)
		}
		if len(allowed) == 0 {
			t.Fatalf("spellings %q/%q: expected the default schemes to stay allowed", minSpelling, maxSpelling)
		}
	}

	// An unusable value is still rejected rather than silently ignored.
	for _, raw := range []string{"0", `"0"`, "abc", `"abc"`, "-1"} {
		state := newMockState()
		state.params[ParamKeyRealmMinThreshold] = []byte(raw)
		if _, _, _, err := newTestEngine(state).realmBounds(); err == nil {
			t.Fatalf("expected min threshold %q to be rejected", raw)
		}
	}
}
