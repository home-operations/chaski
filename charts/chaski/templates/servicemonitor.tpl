{{- if .Values.monitoring.serviceMonitor.enabled }}
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: {{ include "chaski.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "chaski.labels" . | nindent 4 }}
    {{- with .Values.monitoring.serviceMonitor.labels }}
    {{- tpl (toYaml .) $ | nindent 4 }}
    {{- end }}
  {{- with .Values.monitoring.serviceMonitor.annotations }}
  annotations:
    {{- tpl (toYaml .) $ | nindent 4 }}
  {{- end }}
spec:
  selector:
    matchLabels:
      {{- include "chaski.selectorLabels" . | nindent 6 }}
  endpoints:
    # Scrapes the dedicated metrics port; requires config.metricsEnabled.
    - port: metrics
      path: {{ .Values.monitoring.serviceMonitor.path }}
      interval: {{ .Values.monitoring.serviceMonitor.interval }}
      scrapeTimeout: {{ .Values.monitoring.serviceMonitor.scrapeTimeout }}
      {{- with .Values.monitoring.serviceMonitor.metricRelabelings }}
      metricRelabelings:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
      {{- with .Values.monitoring.serviceMonitor.relabelings }}
      relabelings:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
{{- end }}
