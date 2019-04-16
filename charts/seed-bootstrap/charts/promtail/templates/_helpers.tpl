{{/*
Expand the name of the chart.
*/}}
{{- define "promtail.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}
