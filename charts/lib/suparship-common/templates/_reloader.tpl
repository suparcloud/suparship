{{/*
suparship-common.reloaderAnnotation

Stakater Reloader annotation, gated on .Values.reloader. When the value
is truthy ("true", true, "1", etc.), emits

    reloader.stakater.com/auto: "true"

Otherwise emits nothing. Charts that opt out of Reloader (or that
target clusters where it isn't installed) can simply set
`reloader: ""` or `reloader: false` in values.

Usage in a Deployment / StatefulSet metadata block:

    metadata:
      annotations:
        {{- include "suparship-common.reloaderAnnotation" . | nindent 4 }}
*/}}
{{- define "suparship-common.reloaderAnnotation" -}}
{{- if .Values.reloader -}}
reloader.stakater.com/auto: "true"
{{- end -}}
{{- end }}
