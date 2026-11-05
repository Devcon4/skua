{{- define "skua.image" -}}
{{- if .Values.image.digest -}}
{{ .Values.image.repository }}@{{ .Values.image.digest }}
{{- else -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}
{{- end -}}

{{- define "skua.runnerVersions" -}}
{{- $v := semver .Chart.AppVersion -}}
{{- printf ">=%d.%d.0-0 <%d.%d.0" $v.Major $v.Minor $v.Major (add1 $v.Minor) -}}
{{- end -}}

{{- define "skua.runNamespace" -}}
{{- .Values.config.runNamespace | default .Release.Namespace -}}
{{- end -}}

{{- define "skua.sandboxNamespace" -}}
{{- .Values.config.sandboxNamespace | default "skua-sandboxes" -}}
{{- end -}}

{{- define "skua.agentSandboxNamespace" -}}
{{- dig "agentSandbox" "namespace" "" .Values.config | default "agent-sandbox-system" -}}
{{- end -}}

{{- define "skua.operatorKeyNamespace" -}}
{{- dig "gateway" "operatorKeySecret" "namespace" "" .Values.config | default "skua-system" -}}
{{- end -}}

{{- define "skua.config" -}}
{{- $c := deepCopy .Values.config -}}
{{- if not $c.runNamespace -}}
{{- $_ := set $c "runNamespace" .Release.Namespace -}}
{{- end -}}
{{- $runner := $c.runner | default dict -}}
{{- if not $runner.image -}}
{{- $_ := set $runner "image" (include "skua.image" .) -}}
{{- end -}}
{{- if not $runner.supportedVersions -}}
{{- $_ := set $runner "supportedVersions" (include "skua.runnerVersions" .) -}}
{{- end -}}
{{- $_ := set $c "runner" $runner -}}
{{- $api := $c.api | default dict -}}
{{- if not $api.runnerURL -}}
{{- $_ := set $api "runnerURL" (printf "http://skua-api.%s.svc" .Release.Namespace) -}}
{{- end -}}
{{- if not $api.egress -}}
{{- $_ := set $api "egress" (dict "namespace" .Release.Namespace "podLabels" (dict "app.kubernetes.io/name" "skua-api") "port" 8080) -}}
{{- end -}}
{{- $_ := set $c "api" $api -}}
{{- toYaml $c -}}
{{- end -}}

{{- define "skua.labels" -}}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: skua
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{- define "skua.podLabels" -}}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end -}}

{{- define "skua.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: 65532
runAsGroup: 65532
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "skua.containerSecurityContext" -}}
runAsNonRoot: true
runAsUser: 65532
readOnlyRootFilesystem: true
allowPrivilegeEscalation: false
capabilities:
  drop: ["ALL"]
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "skua.operatorSubject" -}}
- kind: ServiceAccount
  name: skua-operator
  namespace: {{ .Release.Namespace }}
{{- end -}}

{{- define "skua.apiSubject" -}}
- kind: ServiceAccount
  name: skua-api
  namespace: {{ .Release.Namespace }}
{{- end -}}
