{{- if and (or .Values.auth.webhookToken .Values.auth.smtpAuth) (not .Values.auth.existingSecret) }}
apiVersion: v1
kind: Secret
metadata:
  name: {{ include "chaski.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "chaski.labels" . | nindent 4 }}
type: Opaque
stringData:
  {{- if .Values.auth.webhookToken }}
  token: {{ .Values.auth.webhookToken | quote }}
  {{- end }}
  {{- if .Values.auth.smtpAuth }}
  smtpAuth: {{ .Values.auth.smtpAuth | quote }}
  {{- end }}
{{- end }}
