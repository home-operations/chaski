{{- if and .Values.auth.webhookToken (not .Values.auth.existingSecret) }}
apiVersion: v1
kind: Secret
metadata:
  name: {{ include "chaski.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "chaski.labels" . | nindent 4 }}
type: Opaque
stringData:
  token: {{ .Values.auth.webhookToken | quote }}
{{- end }}
