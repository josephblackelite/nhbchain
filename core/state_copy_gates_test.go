package core

import (
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

// Every activation setting a StateProcessor carries has to survive Copy: the
// copies a block is built, validated and committed on come from it, and a field
// left out of Copy's struct literal is the zero value there, which for a setting
// whose off value is the largest number means on from height 0. The fields are
// found by name (an "ActivationHeight", "FromHeight" or "FromEpoch" uint64) so a
// setting added later is covered without this test being touched.
func TestStateProcessorCopyCarriesEveryActivationSetting(t *testing.T) {
	sp := newStakingStateProcessor(t)

	value := reflect.ValueOf(sp).Elem()
	typ := value.Type()
	var names []string
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Type.Kind() != reflect.Uint64 {
			continue
		}
		if !strings.Contains(field.Name, "ActivationHeight") && !strings.Contains(field.Name, "FromHeight") && !strings.Contains(field.Name, "FromEpoch") {
			continue
		}
		settable := reflect.NewAt(field.Type, unsafe.Pointer(value.Field(i).UnsafeAddr())).Elem()
		settable.SetUint(uint64(1_000 + i))
		names = append(names, field.Name)
	}
	if len(names) < 6 {
		t.Fatalf("found only %d activation settings on the state processor (%v); the name match no longer finds them", len(names), names)
	}

	copied, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	copiedValue := reflect.ValueOf(copied).Elem()
	for _, name := range names {
		want := value.FieldByName(name).Uint()
		got := copiedValue.FieldByName(name).Uint()
		if got != want {
			t.Errorf("Copy left %s at %d, want %d: add it to the literal in StateProcessor.Copy", name, got, want)
		}
	}
}
