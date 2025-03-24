{{/*
Expand the name of the chart.
*/}}
{{- define "grit-manager.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "grit-manager.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "grit-manager.labels" -}}
helm.sh/chart: {{ include "grit-manager.chart" . }}
{{ include "grit-manager.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "grit-manager.selectorLabels" -}}
app.kubernetes.io/name: {{ include "grit-manager.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}