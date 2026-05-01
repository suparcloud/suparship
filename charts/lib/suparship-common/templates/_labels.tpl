{{/*
suparship-common.commonLabels

Cross-chart labels that don't depend on a single component name. Use
this in multi-component charts where each component has its own name
and `app.kubernetes.io/name` is attached per Deployment by chart-local
helpers (the kustomize-compat `app:` label in voiceai-agent, for
example).

Emits: instance, managed-by, helm.sh/chart, suparship.io/env (when set).
*/}}
{{- define "suparship-common.commonLabels" -}}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ include "suparship-common.chart" . }}
{{- if .Values.app.env }}
suparship.io/env: {{ .Values.app.env }}
{{- end }}
{{- end }}

{{/*
suparship-common.componentLabels

Full standard label set keyed off a named component. Use for both
single-component charts (`component=web`) and multi-component charts
that want the standard label set per component (e.g. a chart with
`web` and `worker` Deployments each with their own labels).

Arguments (passed as a dict):
  root      .   — required; the root context with .Values, .Release, .Chart.
  component str — required; the key under .Values.components used to
                  derive `app.kubernetes.io/version` from
                  components.<component>.image.tag.
  name      str — optional; overrides `app.kubernetes.io/name`. Defaults
                  to suparship-common.fullname when absent. Multi-
                  component charts pass a per-component resource name
                  (e.g. voiceai-livekit-agent-server-<app>).

Usage:
  {{- include "suparship-common.componentLabels" (dict "root" . "component" "web") | nindent 4 }}
*/}}
{{- define "suparship-common.componentLabels" -}}
{{- $root := .root -}}
{{- $component := .component -}}
{{- $name := .name | default (include "suparship-common.fullname" $root) -}}
{{- $c := (index $root.Values.components $component) | default (dict) -}}
{{- $imgTag := (($c.image).tag) | default $root.Chart.AppVersion -}}
app.kubernetes.io/name: {{ $name }}
app.kubernetes.io/instance: {{ $root.Release.Name }}
app.kubernetes.io/version: {{ $imgTag | quote }}
app.kubernetes.io/managed-by: {{ $root.Release.Service }}
helm.sh/chart: {{ include "suparship-common.chart" $root }}
suparship.io/component: {{ $component }}
{{- if $root.Values.app.env }}
suparship.io/env: {{ $root.Values.app.env }}
{{- end }}
{{- end }}

{{/*
suparship-common.componentSelectorLabels

Minimal selector labels — only `app.kubernetes.io/name`. Selectors are
immutable post-creation, so this excludes anything that changes with
release renames (instance) or version bumps (version).

Arguments:
  root .   — required.
  name str — optional; overrides the resource name. Defaults to fullname.

Usage:
  {{- include "suparship-common.componentSelectorLabels" (dict "root" .) | nindent 6 }}
*/}}
{{- define "suparship-common.componentSelectorLabels" -}}
{{- $root := .root -}}
{{- $name := .name | default (include "suparship-common.fullname" $root) -}}
app.kubernetes.io/name: {{ $name }}
{{- end }}

{{/*
suparship-common.componentNameLabel

Just the `suparship.io/component: <name>` line, for cases where the
caller wants the per-component label without the rest of the standard
set. Multi-component charts use this on Pod template labels and
optionally on Deployment metadata labels (single-component charts get
the label for free via componentLabels).

Argument: a string (the component name).

Usage:
  {{- include "suparship-common.componentNameLabel" "livekit-agent" | nindent 8 }}
*/}}
{{- define "suparship-common.componentNameLabel" -}}
suparship.io/component: {{ . }}
{{- end }}

{{/*
DEPRECATED: suparship-common.standardLabels / standardSelectorLabels
are aliases of componentLabels / componentSelectorLabels with
component="web" (the conventional single-component name). Kept so
charts can migrate without a coordinated rename. Prefer the
component-keyed helpers in new code.
*/}}
{{- define "suparship-common.standardLabels" -}}
{{- include "suparship-common.componentLabels" (dict "root" . "component" "web") -}}
{{- end }}

{{- define "suparship-common.standardSelectorLabels" -}}
{{- include "suparship-common.componentSelectorLabels" (dict "root" .) -}}
{{- end }}
