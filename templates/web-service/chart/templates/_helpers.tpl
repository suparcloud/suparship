{{/*
Generate the full resource name.
Uses fullnameOverride if set, otherwise falls back to release name.
*/}}
{{- define "web-service.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{/*
Common labels applied to all resources.
*/}}
{{- define "web-service.labels" -}}
app.kubernetes.io/name: {{ include "web-service.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end }}

{{/*
Selector labels — used by Deployment spec.selector.matchLabels and Service.
Intentionally minimal: only the app name is included so that renaming the
Helm release (which changes app.kubernetes.io/instance) does not require
recreating Deployments. Kubernetes selectors are immutable after creation.
*/}}
{{- define "web-service.selectorLabels" -}}
app.kubernetes.io/name: {{ include "web-service.fullname" . }}
{{- end }}

{{/*
ServiceAccount name — uses override or generated fullname.
*/}}
{{- define "web-service.serviceAccountName" -}}
{{- if .Values.serviceAccount.name }}
{{- .Values.serviceAccount.name }}
{{- else }}
{{- include "web-service.fullname" . }}
{{- end }}
{{- end }}

{{/*
Resource limits based on the size preset.
Template authors: add new sizes here when needed.
*/}}
{{- define "web-service.resources" -}}
{{- if eq .Values.size "large" }}
limits:
  cpu: "1000m"
  memory: "1Gi"
requests:
  cpu: "250m"
  memory: "512Mi"
{{- else if eq .Values.size "medium" }}
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
