{{/*
suparship-common.resources

CPU + memory requests/limits derived from a `size` preset string.
Designed for stateless web/worker components — workloads that need
guaranteed memory (request==limit) or non-standard sizing should
write their own resources block instead of using a preset.

Accepts either a string size (e.g. include "suparship-common.resources" "small")
or a dict {size: "small"} for forward compatibility.

Sizes: small | medium | large. Default: medium (production baseline).
*/}}
{{- define "suparship-common.resources" -}}
{{- $size := "" -}}
{{- if kindIs "string" . -}}
{{- $size = . -}}
{{- else if kindIs "map" . -}}
{{- $size = .size | default "" -}}
{{- end -}}
{{- if not $size -}}{{- $size = "medium" -}}{{- end -}}
{{- if eq $size "small" -}}
limits:
  cpu: "250m"
  memory: "256Mi"
requests:
  cpu: "50m"
  memory: "128Mi"
{{- else if eq $size "large" -}}
limits:
  cpu: "1000m"
  memory: "1Gi"
requests:
  cpu: "250m"
  memory: "512Mi"
{{- else -}}
limits:
  cpu: "500m"
  memory: "512Mi"
requests:
  cpu: "100m"
  memory: "256Mi"
{{- end -}}
{{- end }}

{{/*
suparship-common.componentResources

Convenience: takes a context that has .Values.components.<componentName>.resources.size
and renders the matching preset. The component name is passed as a string in
the second dict key.

Usage:
    {{- include "suparship-common.componentResources" (dict "root" . "component" "web") | nindent 12 }}
*/}}
{{- define "suparship-common.componentResources" -}}
{{- $root := .root -}}
{{- $component := .component -}}
{{- $c := index $root.Values.components $component -}}
{{- $size := (($c.resources).size) | default "medium" -}}
{{- include "suparship-common.resources" $size -}}
{{- end }}
