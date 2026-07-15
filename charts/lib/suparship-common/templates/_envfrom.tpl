{{/*
suparship-common.envFrom

The pod's envFrom block. The platform↔chart contract is the two names
.Values.platform.configMapName and .Values.platform.secretName — the app's (or
this component's) primary ConfigMap + Secret. suparship renders whatever object
sits behind each name (app-wide, or a per-component curated subset); the chart
just envFroms them. Either name may be empty (e.g. a component that opts out of
app secrets) — that ref is then skipped.

.Values.suparship.envFromConfigMaps[] / envFromSecrets[] are ADDITIONAL sources
(addon connection secrets, per-component extras) appended after the base contract.
All entries are optional so pods start before a source is populated.

The whole `envFrom:` key is suppressed when nothing applies so kubectl doesn't
reject the manifest on apply. Defensive against `.Values.platform` /
`.Values.suparship` being unset entirely.

Usage:
    {{- include "suparship-common.envFrom" . | nindent 10 }}
*/}}
{{- define "suparship-common.envFrom" -}}
{{- $plat := (.Values.platform | default (dict)) -}}
{{- $configMap := ($plat.configMapName | default "") -}}
{{- $secret := ($plat.secretName | default "") -}}
{{- $sup := (.Values.suparship | default (dict)) -}}
{{- $cms := ($sup.envFromConfigMaps | default (list)) -}}
{{- $secrets := ($sup.envFromSecrets | default (list)) -}}
{{- if or $configMap $secret $cms $secrets -}}
envFrom:
{{- if $configMap }}
  - configMapRef:
      name: {{ $configMap }}
      optional: true
{{- end }}
{{- if $secret }}
  - secretRef:
      name: {{ $secret }}
      optional: true
{{- end }}
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
