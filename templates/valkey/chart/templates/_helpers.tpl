{{/*
Per-chart helpers delegate to the shared `suparship-common` library.
*/}}

{{- define "valkey.fullname" -}}
{{- include "suparship-common.fullname" . -}}
{{- end }}

{{- define "valkey.labels" -}}
{{- include "suparship-common.commonLabels" . -}}
{{- end }}

{{/*
Connection-Secret name. Publisher passes addon.secretName via values
(`<consumer-app>-addon-<addon-name>-conn`). Falls back to
`<release>-conn` when running the chart standalone (e.g. for chart-
validate's render pass).
*/}}
{{- define "valkey.connectionSecretName" -}}
{{- $explicit := .Values.addon.secretName -}}
{{- if $explicit -}}
{{- $explicit -}}
{{- else -}}
{{- printf "%s-conn" (include "valkey.fullname" .) -}}
{{- end -}}
{{- end }}

{{/*
Upstream Valkey master Service name. With a Bitnami valkey subchart
wired (step 3.5) this would be `<chart-fullname>-master`; for now
the wrapper produces only the contract Secret and the host points at
the same conventional name so the contract is correct out of the box
once the subchart is added.
*/}}
{{- define "valkey.serviceHost" -}}
{{- printf "%s-master" (include "valkey.fullname" .) -}}
{{- end }}

{{- define "valkey.connectionURL" -}}
{{- printf "redis://%s:6379/0" (include "valkey.serviceHost" .) -}}
{{- end }}
