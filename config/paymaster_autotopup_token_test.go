package config

import "testing"

// The automatic paymaster top-up replenishes the asset sponsored gas is paid
// in, so an unset token means NHB. ZNHB is still accepted for an operator that
// names it, and anything else is refused.
func TestPaymasterAutoTopUpTokenDefaultsToTheGasAsset(t *testing.T) {
	global := defaultGlobalConfig()
	if got := global.Paymaster.AutoTopUp.Token; got != "NHB" {
		t.Fatalf("default auto top-up token = %q, want NHB", got)
	}
	for _, tc := range []struct {
		token   string
		want    string
		wantErr bool
	}{
		{token: "", want: "NHB"},
		{token: " nhb ", want: "NHB"},
		{token: "NHB", want: "NHB"},
		{token: "ZNHB", want: "ZNHB"},
		{token: "znhb", want: "ZNHB"},
		{token: "USDX", wantErr: true},
	} {
		g := defaultGlobalConfig()
		g.Paymaster.AutoTopUp.Token = tc.token
		got, err := g.PaymasterAutoTopUpConfig()
		if tc.wantErr {
			if err == nil {
				t.Fatalf("token %q: expected an error", tc.token)
			}
			continue
		}
		if err != nil {
			t.Fatalf("token %q: %v", tc.token, err)
		}
		if got.Token != tc.want {
			t.Fatalf("token %q parsed as %q, want %q", tc.token, got.Token, tc.want)
		}
	}
}
