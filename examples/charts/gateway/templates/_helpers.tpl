{{/*
Expand the name of the chart.
*/}}
{{- define "gateway.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name, truncated to the 63-char DNS limit.
*/}}
{{- define "gateway.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart label value.
*/}}
{{- define "gateway.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "gateway.labels" -}}
helm.sh/chart: {{ include "gateway.chart" . }}
app.kubernetes.io/name: {{ include "gateway.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
The effective listener list: .Values.listeners verbatim when set, otherwise a
production default http/https pair for "*.<domain>" with routes allowed from
every namespace and TLS from the certificate secret.
*/}}
{{- define "gateway.listeners" -}}
{{- if .Values.listeners }}
{{- toYaml .Values.listeners }}
{{- else }}
- name: http
  port: 80
  protocol: HTTP
  hostname: {{ printf "*.%s" .Values.domain | quote }}
  allowedRoutes:
    namespaces:
      from: All
- name: https
  port: 443
  protocol: HTTPS
  hostname: {{ printf "*.%s" .Values.domain | quote }}
  tls:
    mode: Terminate
    certificateRefs:
      - name: {{ .Values.certificate.secretName }}
  allowedRoutes:
    namespaces:
      from: All
{{- end }}
{{- end }}
