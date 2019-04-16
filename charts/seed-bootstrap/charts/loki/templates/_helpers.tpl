{{/*
Expand the name of the chart.
*/}}
{{- define "loki.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "configSecret" }}
auth_enabled: {{ .Values.loki.config.auth_enabled }}

server:
  http_listen_port: {{ .Values.loki.port }}

limits_config:
  enforce_metric_name: false

ingester:
  lifecycler:
    ring:
      store: {{ .Values.loki.config.ingester.lifecycler.ring.store }}
      replication_factor: {{ .Values.loki.config.ingester.lifecycler.ring.replication_factor }}
  chunk_idle_period: 15m

{{- if .Values.loki.config.schema_configs }}
schema_config:
  configs:
{{- range .Values.loki.config.schema_configs }}
  - from: {{ .from }}
    store: {{ .store }}
    object_store: {{ .object_store }}
    schema: {{ .schema }}
    index:
      prefix: {{ .index.prefix }}
      period: {{ .index.period }}
{{- end -}}
{{- end -}}

{{- with .Values.loki.config.storage_config }}
storage_config:
{{ toYaml . | indent 2 }}
{{- end }}

{{- end}}
