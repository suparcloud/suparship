{{/*
Generate the full resource name.
Uses app.name if set (injected by suparShip), otherwise falls back to release name.
*/}}
{{- define "color-app.fullname" -}}
{{- if .Values.app.name }}
{{- .Values.app.name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{/*
Common labels applied to all resources.
*/}}
{{- define "color-app.labels" -}}
app.kubernetes.io/name: {{ include "color-app.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ (.Values.components.web.image.tag) | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- if .Values.app.env }}
suparship.io/env: {{ .Values.app.env }}
{{- end }}
{{- end }}

{{/*
Selector labels — immutable after creation.
*/}}
{{- define "color-app.selectorLabels" -}}
app.kubernetes.io/name: {{ include "color-app.fullname" . }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "color-app.serviceAccountName" -}}
{{- if .Values.serviceAccount.name }}
{{- .Values.serviceAccount.name }}
{{- else }}
{{- include "color-app.fullname" . }}
{{- end }}
{{- end }}

{{/*
Resource limits based on the size preset.
*/}}
{{- define "color-app.resources" -}}
{{- $size := ((.Values.components.web.resources).size) | default "small" }}
{{- if eq $size "large" }}
limits:
  cpu: "1000m"
  memory: "1Gi"
requests:
  cpu: "250m"
  memory: "512Mi"
{{- else if eq $size "medium" }}
limits:
  cpu: "500m"
  memory: "512Mi"
requests:
  cpu: "100m"
  memory: "256Mi"
{{- else }}
limits:
  cpu: "250m"
  memory: "256Mi"
requests:
  cpu: "50m"
  memory: "128Mi"
{{- end }}
{{- end }}
