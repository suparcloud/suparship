{{/*
Per-chart helpers delegate to the shared `suparship-common` library chart —
see templates/web-service/chart/templates/_helpers.tpl for the pattern.
*/}}

{{- define "cronjob.fullname" -}}
{{- include "suparship-common.fullname" . -}}
{{- end }}

{{- define "cronjob.labels" -}}
{{- include "suparship-common.componentLabels" (dict "root" . "component" "cron") -}}
{{- end }}

{{- define "cronjob.serviceAccountName" -}}
{{- include "suparship-common.serviceAccountName" . -}}
{{- end }}

{{- define "cronjob.resources" -}}
{{- include "suparship-common.componentResources" (dict "root" . "component" "cron") -}}
{{- end }}
