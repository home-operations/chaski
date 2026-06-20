{{- if not .Values.config.existingConfigMap }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ include "chaski.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "chaski.labels" . | nindent 4 }}
data:
  # Emitted verbatim via toYaml, never Helm tpl: route fields carry chaski's own
  # CEL and Go-template syntax, which Helm must not try to evaluate.
  chaski.yaml: |
{{ toYaml (dict "routes" (.Values.config.routes | default dict) "targets" (.Values.config.targets | default dict)) | indent 4 }}
{{- end }}
