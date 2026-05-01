{{/*
Per-chart helpers delegate to the shared `suparship-common` library
chart. Wrappers exist so chart templates can reference
`color-app.<name>` consistently and so chart-specific helpers (none
today) can layer on top.
*/}}

{{- define "color-app.fullname" -}}
{{- include "suparship-common.fullname" . -}}
{{- end }}

{{- define "color-app.labels" -}}
{{- include "suparship-common.componentLabels" (dict "root" . "component" "web") -}}
{{- end }}

{{- define "color-app.selectorLabels" -}}
{{- include "suparship-common.componentSelectorLabels" (dict "root" .) -}}
{{- end }}

{{- define "color-app.serviceAccountName" -}}
{{- include "suparship-common.serviceAccountName" . -}}
{{- end }}

{{- define "color-app.resources" -}}
{{- include "suparship-common.componentResources" (dict "root" . "component" "web") -}}
{{- end }}
