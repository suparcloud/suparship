{{/*
Per-chart helpers delegate to the shared `suparship-common` library
chart, keyed off this chart's single component ("job").
*/}}

{{- define "job.fullname" -}}
{{- include "suparship-common.fullname" . -}}
{{- end }}

{{- define "job.labels" -}}
{{- include "suparship-common.componentLabels" (dict "root" . "component" "job") -}}
{{- end }}

{{- define "job.selectorLabels" -}}
{{- include "suparship-common.componentSelectorLabels" (dict "root" .) -}}
{{- end }}

{{- define "job.serviceAccountName" -}}
{{- include "suparship-common.serviceAccountName" . -}}
{{- end }}

{{- define "job.resources" -}}
{{- include "suparship-common.componentResources" (dict "root" . "component" "job") -}}
{{- end }}
