{{/*
Expand the name of the chart.
*/}}
{{- define "chaski.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name (truncated to the 63-char DNS limit).
*/}}
{{- define "chaski.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart name and version as used by the chart label.
*/}}
{{- define "chaski.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "chaski.labels" -}}
helm.sh/chart: {{ include "chaski.chart" . }}
{{ include "chaski.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "chaski.selectorLabels" -}}
app.kubernetes.io/name: {{ include "chaski.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Service account name to use.
*/}}
{{- define "chaski.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "chaski.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Image reference: a digest pin wins, otherwise repository:tag, defaulting the tag
to the chart appVersion. The release pipeline pins the digest at publish time.
*/}}
{{- define "chaski.image" -}}
{{- $repo := .Values.image.repository -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" $repo .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" $repo (.Values.image.tag | default .Chart.AppVersion) -}}
{{- end -}}
{{- end }}

{{/*
Image for the `helm test` connection pod. tests.image.tag is pinned as
`tag@sha256:digest`, so this yields repository:tag@digest.
*/}}
{{- define "chaski.testImage" -}}
{{- $img := .Values.tests.image -}}
{{- printf "%s:%s" $img.repository $img.tag -}}
{{- end }}

{{/*
Name of the ConfigMap holding the routes/targets config: an existing one if
given, otherwise the chart-managed one.
*/}}
{{- define "chaski.configMapName" -}}
{{- default (include "chaski.fullname" .) .Values.config.existingConfigMap }}
{{- end }}
