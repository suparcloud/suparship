{{/*
suparship-common.fullname

Resource name. Uses .Values.app.name (publisher-injected) when set;
otherwise falls back to .Release.Name. Trimmed to 63 chars (DNS label).
*/}}
{{- define "suparship-common.fullname" -}}
{{- if .Values.app.name -}}
{{- .Values.app.name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end }}

{{/*
suparship-common.chart

Chart-version label (helm.sh/chart). "+" replaced with "_" for label
compatibility.
*/}}
{{- define "suparship-common.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/*
suparship-common.serviceAccountName

ServiceAccount name. Uses .Values.serviceAccount.name when set;
otherwise the fullname.
*/}}
{{- define "suparship-common.serviceAccountName" -}}
{{- if .Values.serviceAccount.name -}}
{{- .Values.serviceAccount.name -}}
{{- else -}}
{{- include "suparship-common.fullname" . -}}
{{- end -}}
{{- end }}
