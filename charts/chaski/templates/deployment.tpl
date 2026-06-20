apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "chaski.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "chaski.labels" . | nindent 4 }}
  {{- with .Values.deploymentAnnotations }}
  # Workload-level annotations — e.g. reloader.stakater.com/auto, which must sit
  # on the Deployment (not the pod) to roll it when a referenced object changes.
  annotations:
    {{- tpl (toYaml .) $ | nindent 4 }}
  {{- end }}
spec:
  replicas: {{ .Values.replicaCount }}
  {{- with .Values.strategy }}
  strategy:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  selector:
    matchLabels:
      {{- include "chaski.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "chaski.labels" . | nindent 8 }}
        {{- with .Values.podLabels }}
        {{- tpl (toYaml .) $ | nindent 8 }}
        {{- end }}
      annotations:
        # Roll the pod when the chart-managed config changes (no-op for an
        # existingConfigMap — use Reloader via deploymentAnnotations there).
        checksum/config: {{ include (print $.Template.BasePath "/configmap.tpl") . | sha256sum }}
        {{- with .Values.podAnnotations }}
        {{- tpl (toYaml .) $ | nindent 8 }}
        {{- end }}
    spec:
      {{- with .Values.imagePullSecrets }}
      imagePullSecrets:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
      serviceAccountName: {{ include "chaski.serviceAccountName" . }}
      automountServiceAccountToken: {{ .Values.serviceAccount.automount }}
      {{- with .Values.priorityClassName }}
      priorityClassName: {{ tpl . $ | quote }}
      {{- end }}
      terminationGracePeriodSeconds: {{ .Values.terminationGracePeriodSeconds }}
      securityContext:
        {{- tpl (toYaml .Values.podSecurityContext) $ | nindent 8 }}
      {{- with .Values.initContainers }}
      initContainers:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
      containers:
        - name: chaski
          image: {{ include "chaski.image" . | quote }}
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          securityContext:
            {{- tpl (toYaml .Values.securityContext) $ | nindent 12 }}
          env:
            - name: CHASKI_PORT
              value: {{ .Values.config.port | quote }}
            - name: CHASKI_METRICS_ENABLED
              value: {{ .Values.config.metricsEnabled | quote }}
            - name: CHASKI_METRICS_PORT
              value: {{ .Values.config.metricsPort | quote }}
            - name: CHASKI_CONFIG
              value: {{ .Values.config.configPath | quote }}
            - name: CHASKI_LOG_LEVEL
              value: {{ .Values.config.logLevel | quote }}
            - name: CHASKI_LOG_FORMAT
              value: {{ .Values.config.logFormat | quote }}
            - name: CHASKI_MAX_BODY_BYTES
              value: {{ .Values.config.maxBodyBytes | int64 | quote }}
            - name: CHASKI_REQUEST_TIMEOUT
              value: {{ .Values.config.requestTimeout | quote }}
            - name: CHASKI_RETRY_ATTEMPTS
              value: {{ .Values.config.retryAttempts | quote }}
            - name: CHASKI_RETRY_BACKOFF
              value: {{ .Values.config.retryBackoff | quote }}
            {{- if or .Values.auth.webhookToken .Values.auth.existingSecret }}
            - name: CHASKI_WEBHOOK_TOKEN
              valueFrom:
                secretKeyRef:
                  name: {{ .Values.auth.existingSecret | default (include "chaski.fullname" .) }}
                  key: {{ if .Values.auth.existingSecret }}{{ .Values.auth.existingSecretKey }}{{ else }}token{{ end }}
            {{- end }}
            {{- range $k, $v := .Values.env }}
            - name: {{ $k }}
              value: {{ tpl (toString $v) $ | quote }}
            {{- end }}
            {{- with .Values.extraEnv }}
            {{- tpl (toYaml .) $ | nindent 12 }}
            {{- end }}
          {{- with .Values.envFrom }}
          envFrom:
            {{- tpl (toYaml .) $ | nindent 12 }}
          {{- end }}
          ports:
            - name: http
              containerPort: {{ .Values.config.port }}
              protocol: TCP
            {{- if .Values.config.metricsEnabled }}
            - name: metrics
              containerPort: {{ .Values.config.metricsPort }}
              protocol: TCP
            {{- end }}
          {{- with .Values.startupProbe }}
          startupProbe:
            {{- tpl (toYaml .) $ | nindent 12 }}
          {{- end }}
          {{- with .Values.livenessProbe }}
          livenessProbe:
            {{- tpl (toYaml .) $ | nindent 12 }}
          {{- end }}
          {{- with .Values.readinessProbe }}
          readinessProbe:
            {{- tpl (toYaml .) $ | nindent 12 }}
          {{- end }}
          {{- with .Values.resources }}
          resources:
            {{- tpl (toYaml .) $ | nindent 12 }}
          {{- end }}
          volumeMounts:
            - name: config
              mountPath: {{ .Values.config.mountPath }}
              readOnly: true
            {{- with .Values.volumeMounts }}
            {{- tpl (toYaml .) $ | nindent 12 }}
            {{- end }}
      volumes:
        - name: config
          configMap:
            name: {{ include "chaski.configMapName" . }}
        {{- with .Values.volumes }}
        {{- tpl (toYaml .) $ | nindent 8 }}
        {{- end }}
      {{- with .Values.nodeSelector }}
      nodeSelector:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
      {{- with .Values.affinity }}
      affinity:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
      {{- with .Values.tolerations }}
      tolerations:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
      {{- with .Values.topologySpreadConstraints }}
      topologySpreadConstraints:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
