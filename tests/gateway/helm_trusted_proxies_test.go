package gateway_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"text/template"

	"gopkg.in/yaml.v3"

	gatewayconfig "nhbchain/gateway/config"
	"nhbchain/gateway/middleware"
)

// The gateway counts requests per connecting address. Behind an ingress that
// address is the ingress pod unless the pod range is listed in trustedProxies.
// These tests cover the chart wiring that makes that list reach the gateway and
// the chart's refusal to render an ingress-fronted gateway without it. They do
// not run Helm: the values are merged and the config rendered with text/template
// and stand-ins for the few Helm functions the chart uses.

func chartPath(rel string) string {
	return filepath.Join("..", "..", "deploy", "helm", filepath.FromSlash(rel))
}

func readChartFile(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(chartPath(rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return data
}

func readChartYAML(t *testing.T, rel string) map[string]any {
	t.Helper()
	out := map[string]any{}
	if err := yaml.Unmarshal(readChartFile(t, rel), &out); err != nil {
		t.Fatalf("decode %s: %v", rel, err)
	}
	return out
}

// mergeValues overlays b on a the way Helm merges a values file over chart
// defaults: maps merge key by key, every other value is replaced.
func mergeValues(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if bm, ok := v.(map[string]any); ok {
			if am, ok := out[k].(map[string]any); ok {
				out[k] = mergeValues(am, bm)
				continue
			}
		}
		out[k] = v
	}
	return out
}

func gatewayChartValues(t *testing.T, env string) map[string]any {
	t.Helper()
	return mergeValues(
		readChartYAML(t, "gateway/values.yaml"),
		readChartYAML(t, "values/"+env+"/gateway.yaml"),
	)
}

func stubHelmFuncs() template.FuncMap {
	return template.FuncMap{
		"toJson": func(v any) (string, error) {
			raw, err := json.Marshal(v)
			return string(raw), err
		},
		"fail": func(msg string) (string, error) { return "", errors.New(msg) },
		// The helpers file also defines name and label templates that use these;
		// they are never executed here but must exist for the file to parse.
		"default":    func(d, v any) any { return v },
		"trunc":      func(n int, s string) string { return s },
		"trimSuffix": func(suffix, s string) string { return s },
		"replace":    func(old, repl, s string) string { return s },
		"quote":      func(v any) string { return "" },
		"include":    func(name string, data any) string { return "" },
	}
}

func renderGatewayConfig(t *testing.T, values map[string]any) string {
	t.Helper()
	content, _ := values["config"].(map[string]any)["content"].(string)
	if content == "" {
		t.Fatalf("config.content is empty")
	}
	tpl, err := template.New("config").Funcs(stubHelmFuncs()).Option("missingkey=error").Parse(content)
	if err != nil {
		t.Fatalf("parse config.content: %v", err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, map[string]any{"Values": values}); err != nil {
		t.Fatalf("render config.content: %v", err)
	}
	return buf.String()
}

func TestShippedGatewayValuesDeliverTrustedProxiesToTheGateway(t *testing.T) {
	want := []string{"10.20.0.0/16", "192.0.2.7"}
	for _, env := range []string{"dev", "staging", "prod"} {
		t.Run(env, func(t *testing.T) {
			values := gatewayChartValues(t, env)
			values["trustedProxies"] = []any{want[0], want[1]}
			rendered := renderGatewayConfig(t, values)

			path := filepath.Join(t.TempDir(), "gateway.yaml")
			if err := os.WriteFile(path, []byte(rendered), 0o600); err != nil {
				t.Fatalf("write rendered config: %v", err)
			}
			cfg, err := gatewayconfig.Load(path)
			if err != nil {
				t.Fatalf("load rendered config: %v\n%s", err, rendered)
			}
			if !reflect.DeepEqual(cfg.TrustedProxies, want) {
				t.Fatalf("gateway config trustedProxies = %v, want %v", cfg.TrustedProxies, want)
			}
			if _, err := middleware.ParseTrustedProxies(cfg.TrustedProxies); err != nil {
				t.Fatalf("gateway rejects the rendered trustedProxies: %v", err)
			}
		})
	}
}

func TestShippedIngressValuesCarryTheTrustedProxiesPrecondition(t *testing.T) {
	for _, env := range []string{"staging", "prod"} {
		t.Run(env, func(t *testing.T) {
			own := readChartYAML(t, "values/"+env+"/gateway.yaml")
			if _, ok := own["trustedProxies"]; !ok {
				t.Fatalf("values/%s/gateway.yaml must carry a trustedProxies entry so the precondition sits next to the ingress settings", env)
			}
			merged := gatewayChartValues(t, env)
			ingress, _ := merged["ingress"].(map[string]any)
			if enabled, _ := ingress["enabled"].(bool); !enabled {
				t.Fatalf("values/%s/gateway.yaml no longer enables the ingress; revisit this test", env)
			}
		})
	}
}

func gatewayValidate(t *testing.T, ingressEnabled bool, proxies []any) error {
	t.Helper()
	helpers := readChartFile(t, "gateway/templates/_helpers.tpl")
	tpl, err := template.New("helpers").Funcs(stubHelmFuncs()).Parse(string(helpers))
	if err != nil {
		t.Fatalf("parse _helpers.tpl: %v", err)
	}
	data := map[string]any{
		"Values": map[string]any{
			"ingress":        map[string]any{"enabled": ingressEnabled},
			"trustedProxies": proxies,
		},
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "gateway.validate", data); err != nil {
		return err
	}
	if strings.TrimSpace(buf.String()) != "" {
		t.Fatalf("gateway.validate must render nothing when it accepts the values, got %q", buf.String())
	}
	return nil
}

func TestGatewayChartRefusesAnIngressWithoutTrustedProxies(t *testing.T) {
	for _, proxies := range [][]any{nil, {}} {
		err := gatewayValidate(t, true, proxies)
		if err == nil {
			t.Fatalf("an ingress-fronted gateway with trustedProxies %v must be refused", proxies)
		}
		if !strings.Contains(err.Error(), "trustedProxies") {
			t.Fatalf("refusal should name trustedProxies, got: %v", err)
		}
	}
	if err := gatewayValidate(t, true, []any{"10.20.0.0/16"}); err != nil {
		t.Fatalf("an ingress-fronted gateway with a trusted proxy range was refused: %v", err)
	}
	if err := gatewayValidate(t, false, nil); err != nil {
		t.Fatalf("a gateway without an ingress must not need trustedProxies: %v", err)
	}
}

func TestGatewayChartAppliesTheTrustedProxiesCheckToTheDeployment(t *testing.T) {
	deployment := string(readChartFile(t, "gateway/templates/deployment.yaml"))
	if !strings.Contains(deployment, `include "gateway.validate" .`) {
		t.Fatalf("templates/deployment.yaml must include gateway.validate so the check runs on every render")
	}
}
