{{/*
Per-chart helpers delegate to the shared `suparship-common` library chart —
see templates/web-service/chart/templates/_helpers.tpl for the pattern.
*/}}

{{- define "worker.fullname" -}}
{{- include "suparship-common.fullname" . -}}
{{- end }}

{{- define "worker.labels" -}}
{{- include "suparship-common.componentLabels" (dict "root" . "component" "worker") -}}
{{- end }}

{{- define "worker.selectorLabels" -}}
{{- include "suparship-common.componentSelectorLabels" (dict "root" .) -}}
{{- end }}

{{- define "worker.serviceAccountName" -}}
{{- include "suparship-common.serviceAccountName" . -}}
{{- end }}

{{- define "worker.resources" -}}
{{- include "suparship-common.componentResources" (dict "root" . "component" "worker") -}}
{{- end }}
