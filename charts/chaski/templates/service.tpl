apiVersion: v1
kind: Service
metadata:
  name: {{ include "chaski.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "chaski.labels" . | nindent 4 }}
  {{- with .Values.service.annotations }}
  annotations:
    {{- tpl (toYaml .) $ | nindent 4 }}
  {{- end }}
spec:
  type: {{ .Values.service.type }}
  {{- if and .Values.service.externalTrafficPolicy (ne .Values.service.type "ClusterIP") }}
  externalTrafficPolicy: {{ .Values.service.externalTrafficPolicy }}
  {{- end }}
  selector:
    {{- include "chaski.selectorLabels" . | nindent 4 }}
  ports:
    - name: http
      port: {{ .Values.service.port }}
      targetPort: http
      protocol: TCP
    {{- if .Values.config.metricsEnabled }}
    - name: metrics
      port: {{ .Values.config.metricsPort }}
      targetPort: metrics
      protocol: TCP
    {{- end }}
    {{- if .Values.config.smtpEnabled }}
    - name: smtp
      port: {{ .Values.config.smtpPort }}
      targetPort: smtp
      protocol: TCP
    {{- end }}
