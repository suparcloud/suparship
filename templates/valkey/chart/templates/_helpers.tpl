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
Upstream Valkey primary Service name.

The Bitnami valkey subchart names its primary service
`<release>-valkey-primary` in standalone mode (and replication mode).
Wrapper Release.Name comes from the publisher pattern
`<consumer-app>-addon-<addon-name>`, e.g. `hello-addon-cache`. The
final service host is therefore `<consumer-app>-addon-<addon-name>-valkey-primary`.

Local installs (helm install --name foo) where Release.Name doesn't
match the publisher pattern still work — the host derives from the
release name in either case.
*/}}
{{- define "valkey.serviceHost" -}}
{{- printf "%s-valkey-primary" .Release.Name -}}
{{- end }}

{{- define "valkey.connectionURL" -}}
{{- printf "redis://%s:6379/0" (include "valkey.serviceHost" .) -}}
{{- end }}
