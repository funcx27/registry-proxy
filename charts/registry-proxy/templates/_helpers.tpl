{{/*
Expand the name of the chart.
*/}}
{{- define "registry-proxy.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this
(by the DNS naming spec).
*/}}
{{- define "registry-proxy.fullname" -}}
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
{{- define "registry-proxy.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "registry-proxy.labels" -}}
helm.sh/chart: {{ include "registry-proxy.chart" . }}
{{ include "registry-proxy.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: registry
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "registry-proxy.selectorLabels" -}}
app.kubernetes.io/name: {{ include "registry-proxy.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Service account name.
*/}}
{{- define "registry-proxy.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "registry-proxy.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Container image reference.
*/}}
{{- define "registry-proxy.image" -}}
{{- if .Values.image.digest }}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest }}
{{- else }}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) }}
{{- end }}
{{- end }}

{{/*
Name of the Secret holding upstream credentials.
*/}}
{{- define "registry-proxy.upstreamAuthSecretName" -}}
{{- if .Values.upstreamAuth.existingSecret }}
{{- .Values.upstreamAuth.existingSecret }}
{{- else }}
{{- printf "%s-upstream-auth" (include "registry-proxy.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{/*
Absolute path passed to skopeo through REGISTRY_AUTH_FILE.
The standard Docker config JSON key is always used.
*/}}
{{- define "registry-proxy.upstreamAuthFile" -}}
{{- printf "%s/.dockerconfigjson" (trimSuffix "/" .Values.upstreamAuth.mountPath) }}
{{- end }}

{{/*
ConfigMap containing proxy-mapping.yaml.
*/}}
{{- define "registry-proxy.proxyMappingConfigMapName" -}}
{{- default (include "registry-proxy.fullname" .) .Values.config.existingProxyMappingConfigMap }}
{{- end }}

{{/*
ConfigMap containing the pre-pull image list.
*/}}
{{- define "registry-proxy.prePullConfigMapName" -}}
{{- default (include "registry-proxy.fullname" .) .Values.prePull.existingConfigMap }}
{{- end }}

{{/*
Build and validate the Docker config JSON used for upstream registry auth.
Returns the JSON string, or an empty string when existingSecret is used.
*/}}
{{- define "registry-proxy.upstreamAuthConfig" -}}
{{- if .Values.upstreamAuth.registries -}}
{{- if .Values.upstreamAuth.dockerconfigjson -}}
{{- fail "configure only one of upstreamAuth.registries, upstreamAuth.dockerconfigjson, or upstreamAuth.dockerConfigFile" -}}
{{- end -}}
{{- if .Values.upstreamAuth.dockerConfigFile -}}
{{- fail "configure only one of upstreamAuth.registries, upstreamAuth.dockerconfigjson, or upstreamAuth.dockerConfigFile" -}}
{{- end -}}
{{- $auths := dict -}}
{{- range $registry, $account := .Values.upstreamAuth.registries -}}
{{- if not (kindIs "map" $account) -}}
{{- fail (printf "upstreamAuth.registries[%q] must contain username and password" $registry) -}}
{{- end -}}
{{- if not (and $account.username $account.password) -}}
{{- fail (printf "upstreamAuth.registries[%q] requires both username and password" $registry) -}}
{{- end -}}
{{- $encodedAuth := printf "%s:%s" (toString $account.username) (toString $account.password) | b64enc -}}
{{- $_ := set $auths $registry (dict "auth" $encodedAuth) -}}
{{- end -}}
{{- dict "auths" $auths | toJson -}}
{{- else if .Values.upstreamAuth.dockerconfigjson -}}
{{- if .Values.upstreamAuth.dockerConfigFile -}}
{{- fail "configure only one of upstreamAuth.registries, upstreamAuth.dockerconfigjson, or upstreamAuth.dockerConfigFile" -}}
{{- end -}}
{{- .Values.upstreamAuth.dockerconfigjson -}}
{{- else if .Values.upstreamAuth.dockerConfigFile -}}
{{- $dockerConfigJSON := .Files.Get .Values.upstreamAuth.dockerConfigFile -}}
{{- if not $dockerConfigJSON -}}
{{- fail (printf "upstreamAuth.dockerConfigFile %q is empty or was not found in the chart package" .Values.upstreamAuth.dockerConfigFile) -}}
{{- end -}}
{{- $dockerConfigJSON -}}
{{- else -}}
{{- fail "configure upstreamAuth.registries, --set-file upstreamAuth.dockerconfigjson=<path>, or upstreamAuth.dockerConfigFile when upstreamAuth.enabled=true and upstreamAuth.existingSecret is empty" -}}
{{- end -}}
{{- end -}}
