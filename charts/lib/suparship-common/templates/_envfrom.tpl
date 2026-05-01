{{/*
suparship-common.envFrom

envFrom block fed by .Values.suparship.envFromConfigMaps[] and
.Values.suparship.envFromSecrets[] — the precedence-ordered hierarchy
the publisher computes (org → env-type → project → app → app-env →
cluster). All entries are marked optional: pods start even before a
level is populated.

The whole `envFrom:` key is suppressed when both lists are empty so
kubectl doesn't reject the manifest on apply.

Defensive against `.Values.suparship` being unset entirely.

Usage:
    {{- include "suparship-common.envFrom" . | nindent 10 }}
*/}}
{{- define "suparship-common.envFrom" -}}
{{- $sup := (.Values.suparship | default (dict)) -}}
{{- $cms := ($sup.envFromConfigMaps | default (list)) -}}
{{- $secrets := ($sup.envFromSecrets | default (list)) -}}
{{- if or $cms $secrets -}}
envFrom:
{{- range $cms }}
  - configMapRef:
      name: {{ . }}
      optional: true
{{- end }}
{{- range $secrets }}
  - secretRef:
      name: {{ . }}
      optional: true
{{- end }}
{{- end -}}
{{- end }}
