{{/*
Expand the name of the chart.
*/}}
{{- define "troublemaker.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "troublemaker.fullname" -}}
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
Create chart name and version as used by the chart label.
*/}}
{{- define "troublemaker.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "troublemaker.labels" -}}
helm.sh/chart: {{ include "troublemaker.chart" . }}
{{ include "troublemaker.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "troublemaker.selectorLabels" -}}
app.kubernetes.io/name: {{ include "troublemaker.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Service account name
*/}}
{{- define "troublemaker.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "troublemaker.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
PriorityClass name: explicit priorityClassName wins, otherwise the created one.
*/}}
{{- define "troublemaker.priorityClassName" -}}
{{- if .Values.priorityClassName }}
{{- .Values.priorityClassName }}
{{- else if .Values.priorityClass.create }}
{{- default (include "troublemaker.fullname" .) .Values.priorityClass.name }}
{{- end }}
{{- end }}
