{{/* The labels that find a service's pods: its name, in this release. */}}
{{- define "payments.selector" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
{{- end }}

{{/* Every object's labels: the selector's, and what made it. */}}
{{- define "payments.labels" -}}
{{ include "payments.selector" . }}
app.kubernetes.io/part-of: payments
app.kubernetes.io/version: {{ .root.Values.image.tag | quote }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
{{- end }}

{{/* A service's environment: everyone's, then its own, which wins. */}}
{{- define "payments.env" -}}
{{ upper .name }}_DRAIN_DELAY: {{ .root.Values.drainDelay | quote }}
{{- range $key, $value := merge (dict) .svc.env .root.Values.env }}
{{ $key }}: {{ $value | quote }}
{{- end }}
{{- end }}
