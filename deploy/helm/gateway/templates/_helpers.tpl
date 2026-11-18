{{- define "gateway.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "gateway.fullname" -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/*
Refuses to render an ingress-fronted gateway that has no trusted proxy range.
The gateway counts requests per connecting address, so behind an ingress an
empty trustedProxies list puts every client in one rate-limit bucket per
ingress pod.
*/}}
{{- define "gateway.validate" -}}
{{- if and .Values.ingress.enabled (not .Values.trustedProxies) -}}
{{- fail "trustedProxies must list the address range of the ingress controller (and of any load balancer in front of it) when ingress.enabled is true: with an empty list every client behind the ingress shares one rate-limit bucket per ingress pod. Set it in the values file or with --set; see docs/ops/security.md, Gateway rate limits behind a proxy." -}}
{{- end -}}
{{- end -}}

{{- define "gateway.labels" -}}
app.kubernetes.io/name: {{ include "gateway.name" . }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end -}}
