{{/*
Expand the name of the chart.
*/}}
{{- define "doppler-operator.name" -}}
doppler-operator
{{- end }}

{{/*
Fullname prefix used for all resources.
Matches the existing kustomize namePrefix: doppler-operator-
*/}}
{{- define "doppler-operator.fullname" -}}
doppler-operator
{{- end }}

{{/*
Common labels applied to all resources.
*/}}
{{- define "doppler-operator.labels" -}}
app.kubernetes.io/name: {{ include "doppler-operator.name" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
{{- end }}

{{/*
Selector labels for the controller manager.
*/}}
{{- define "doppler-operator.selectorLabels" -}}
control-plane: controller-manager
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "doppler-operator.serviceAccountName" -}}
{{ include "doppler-operator.fullname" . }}-controller-manager
{{- end }}
