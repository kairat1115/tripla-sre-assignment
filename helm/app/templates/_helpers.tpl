{{- define "app.fullname" -}}
{{- default .Release.Name .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "app.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- include "app.selectorLabels" . }}
{{- end }}

{{- define "app.selectorLabels" -}}
app.kubernetes.io/name: {{ tpl (default .Chart.Name .Values.nameOverride) . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{ $version := default .Chart.AppVersion .Values.image.tag -}}
{{ if $version }}
app.kubernetes.io/version: {{ $version | quote }}
{{ end }}
{{- end }}

{{- define "app.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "app.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
