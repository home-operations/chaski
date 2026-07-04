apiVersion: v1
kind: Pod
metadata:
  name: {{ include "chaski.fullname" . }}-test-connection
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "chaski.labels" . | nindent 4 }}
  annotations:
    helm.sh/hook: test
    helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded
spec:
  restartPolicy: Never
  securityContext:
    {{- toYaml .Values.podSecurityContext | nindent 4 }}
  containers:
    - name: curl
      image: {{ include "chaski.testImage" . | quote }}
      imagePullPolicy: {{ .Values.tests.image.pullPolicy }}
      securityContext:
        {{- toYaml .Values.securityContext | nindent 8 }}
      command:
        - /bin/sh
        - -c
        - |
          set -eu
          url="http://{{ include "chaski.fullname" . }}:{{ .Values.service.port }}/readyz"
          echo "GET ${url}"
          body="$(curl -fsS "${url}")"
          echo "${body}"
          echo "${body}" | grep -q '"status":"ok"'
