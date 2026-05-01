{{/*
Per-chart helpers delegate to the shared `suparship-common` library
chart. Wrappers exist so chart templates can reference
`web-service.<name>` consistently and so chart-specific helpers can
layer on top.
*/}}

{{- define "web-service.fullname" -}}
{{- include "suparship-common.fullname" . -}}
{{- end }}

{{- define "web-service.labels" -}}
{{- include "suparship-common.componentLabels" (dict "root" . "component" "web") -}}
{{- end }}

{{- define "web-service.selectorLabels" -}}
{{- include "suparship-common.componentSelectorLabels" (dict "root" .) -}}
{{- end }}

{{- define "web-service.serviceAccountName" -}}
{{- include "suparship-common.serviceAccountName" . -}}
{{- end }}

{{- define "web-service.resources" -}}
{{- include "suparship-common.componentResources" (dict "root" . "component" "web") -}}
{{- end }}
