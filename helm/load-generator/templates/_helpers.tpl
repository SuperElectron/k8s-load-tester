{{/*
Target URL for the load generator.

By default the generator aims at the api-golang service inside whatever namespace
this release is installed into, so the chart works in any namespace with no edits.
Set env.apiUrl in values.yaml to a full URL to point it at something else entirely
(a different cluster, a staging host, an external endpoint).
*/}}
{{- define "load-generator.apiUrl" -}}
{{- if .Values.env.apiUrl -}}
{{- .Values.env.apiUrl -}}
{{- else -}}
{{- printf "http://%s.%s.svc.cluster.local:%v" .Values.env.apiService .Release.Namespace .Values.env.apiPort -}}
{{- end -}}
{{- end -}}
