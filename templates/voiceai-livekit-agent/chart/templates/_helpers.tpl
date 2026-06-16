{{/*
Chart name (overridable).
*/}}
{{- define "voiceai-livekit-agent.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully qualified release name (standard Helm pattern).
*/}}
{{- define "voiceai-livekit-agent.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "voiceai-livekit-agent.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels applied to every object.
*/}}
{{- define "voiceai-livekit-agent.commonLabels" -}}
helm.sh/chart: {{ include "voiceai-livekit-agent.chart" . }}
app.kubernetes.io/name: {{ include "voiceai-livekit-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- with .Chart.AppVersion }}
app.kubernetes.io/version: {{ . | quote }}
{{- end }}
app.biglysales.com/caller: {{ .Values.caller.name }}
{{- end -}}

{{/* ---- Agent component ---- */}}

{{- define "voiceai-livekit-agent.agent.fullname" -}}
{{- printf "%s-server" (include "voiceai-livekit-agent.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "voiceai-livekit-agent.agent.selectorLabels" -}}
app.kubernetes.io/name: {{ include "voiceai-livekit-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: agent
{{- end -}}

{{- define "voiceai-livekit-agent.agent.labels" -}}
{{ include "voiceai-livekit-agent.commonLabels" . }}
app.kubernetes.io/component: agent
app.biglysales.com/component-type: livekit-agent
{{- end -}}

{{/* ---- Capacity-manager component ---- */}}

{{- define "voiceai-livekit-agent.cm.fullname" -}}
{{- printf "%s-cm" (include "voiceai-livekit-agent.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "voiceai-livekit-agent.cm.selectorLabels" -}}
app.kubernetes.io/name: {{ include "voiceai-livekit-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: capacity-manager
{{- end -}}

{{- define "voiceai-livekit-agent.cm.labels" -}}
{{ include "voiceai-livekit-agent.commonLabels" . }}
app.kubernetes.io/component: capacity-manager
{{- end -}}

{{/*
Resolvers for opinionated-but-overridable infra. Each returns the value the
platform overlay set (`.Values.X`) or the baked production default. Nil-safe:
the keys are intentionally absent from values.yaml. Bools use hasKey (Helm's
`default` would clobber a deliberate `false`).
*/}}
{{- define "voiceai-livekit-agent.priorityClassName" -}}
{{- (.Values.agent | default dict).priorityClassName | default "high-priority-preempting" -}}
{{- end -}}

{{- define "voiceai-livekit-agent.hostNetwork" -}}
{{- if hasKey (.Values.agent | default dict) "hostNetwork" -}}
{{- .Values.agent.hostNetwork -}}
{{- else -}}true{{- end -}}
{{- end -}}

{{- define "voiceai-livekit-agent.releaseChannel" -}}
{{- .Values.releaseChannel | default "default" -}}
{{- end -}}

{{- define "voiceai-livekit-agent.prometheusServer" -}}
{{- (.Values.monitoring | default dict).prometheusServer | default "http://kube-prometheus-stack-prometheus.monitoring.svc:9090" -}}
{{- end -}}

{{- define "voiceai-livekit-agent.statsdHost" -}}
{{- (((.Values.monitoring | default dict).statsd) | default dict).host | default "prometheus-statsd-exporter.monitoring.svc.cluster.local" -}}
{{- end -}}

{{- define "voiceai-livekit-agent.statsdPort" -}}
{{- (((.Values.monitoring | default dict).statsd) | default dict).port | default 8125 -}}
{{- end -}}

{{- define "voiceai-livekit-agent.otelCollectorPort" -}}
{{- (.Values.monitoring | default dict).otelCollectorPort | default 4317 -}}
{{- end -}}

{{- define "voiceai-livekit-agent.valkeyUrl" -}}
{{- .Values.valkeyUrl | default "redis://valkey-internal:6379/0" -}}
{{- end -}}

{{- define "voiceai-livekit-agent.busyboxImage" -}}
{{- ((.Values.agent | default dict).signalWatcher | default dict).image | default "acrsharedsouthcentralusbiglysales.azurecr.io/busybox:1.37.0-uclibc" -}}
{{- end -}}

{{/*
Downward-API env shared by every container (pod/node identity).
*/}}
{{- define "voiceai-livekit-agent.downwardEnv" -}}
- name: K8S_POD_NAME
  valueFrom:
    fieldRef:
      fieldPath: metadata.name
- name: K8S_POD_UID
  valueFrom:
    fieldRef:
      fieldPath: metadata.uid
- name: K8S_NODE_NAME
  valueFrom:
    fieldRef:
      fieldPath: spec.nodeName
- name: K8S_NAMESPACE
  valueFrom:
    fieldRef:
      fieldPath: metadata.namespace
- name: K8S_POD_IP
  valueFrom:
    fieldRef:
      fieldPath: status.podIP
- name: K8S_NODE_IP
  valueFrom:
    fieldRef:
      fieldPath: status.hostIP
{{- end -}}
