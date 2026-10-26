{{/* Names and labels. */}}
{{- define "openarcanum.fullname" -}}
{{- printf "%s" .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- end -}}

{{- define "openarcanum.labels" -}}
app.kubernetes.io/name: openarcanum
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "openarcanum.selector" -}}
app.kubernetes.io/name: openarcanum
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "openarcanum.image" -}}
{{- $name := index (.root.Values.image.names | default dict) .component | default .component -}}
{{- printf "%s-%s:%s" .root.Values.image.repository $name (.root.Values.image.tag | default .root.Chart.AppVersion) -}}
{{- end -}}

{{/* The chart's maintainer nodes: meta.nodes, or one unnamed node (JSON). */}}
{{- define "openarcanum.metaNodes" -}}
{{- if .Values.meta.nodes }}{{ toJson .Values.meta.nodes }}{{ else }}{{ toJson (list (dict "name" "" "role" "root")) }}{{ end -}}
{{- end -}}

{{/* A maintainer node's object names: <release>-meta, or <release>-meta-<name>. */}}
{{- define "openarcanum.metaName" -}}
{{- if .node.name }}{{ printf "%s-meta-%s" (include "openarcanum.fullname" .root) .node.name }}{{ else }}{{ printf "%s-meta" (include "openarcanum.fullname" .root) }}{{ end -}}
{{- end -}}

{{- define "openarcanum.metaSelector" -}}
{{ include "openarcanum.selector" (dict "root" .root "component" "meta") }}
{{- if .node.name }}
meta-node: {{ .node.name }}
{{- end }}
{{- end -}}

{{/* The chart's maintainer nodes, as the servers inside the cluster reach them (comma-separated). */}}
{{- define "openarcanum.metaURLs" -}}
{{- $root := . -}}
{{- $urls := list -}}
{{- range $n := include "openarcanum.metaNodes" . | fromJsonArray -}}
{{- $domains := include "openarcanum.metaDomains" (dict "root" $root "node" $n) | fromJsonArray -}}
{{- if and (ne $root.Values.meta.tls.mode "off") $domains -}}
{{- /* With TLS the certificate names the public domain, not the Service. */ -}}
{{- $urls = append $urls (printf "https://%s" (include "openarcanum.urlHost" (first $domains))) -}}
{{- else -}}
{{- $urls = append $urls (printf "http://%s.%s.svc.cluster.local:%v" (include "openarcanum.metaName" (dict "root" $root "node" $n)) $root.Release.Namespace $root.Values.meta.port) -}}
{{- end -}}
{{- end -}}
{{- join "," $urls -}}
{{- end -}}

{{/* A maintainer node's TLS names: its tlsDomains, or meta.tls.domains (JSON). */}}
{{- define "openarcanum.metaDomains" -}}
{{- toJson (.node.tlsDomains | default .root.Values.meta.tls.domains | default list) -}}
{{- end -}}

{{/* A host for a URL: IPv6 addresses in brackets. */}}
{{- define "openarcanum.urlHost" -}}
{{- if contains ":" . }}{{ printf "[%s]" . }}{{ else }}{{ . }}{{ end -}}
{{- end -}}

{{- define "openarcanum.relayURL" -}}
{{- if .Values.relay.publicURL -}}
{{- .Values.relay.publicURL -}}
{{- else if and (ne .Values.relay.tls.mode "off") .Values.relay.tls.domains -}}
{{- printf "wss://%s/ws" (include "openarcanum.urlHost" (first .Values.relay.tls.domains)) -}}
{{- else -}}
{{- required "relay.publicURL is required: the relay URL that both clients and the community server can reach (docs/kubernetes.md), or turn on relay.tls with domains" .Values.relay.publicURL -}}
{{- end -}}
{{- end -}}

{{/*
TLS settings as OPENARCANUM_<NODE>_* variables (docs/kubernetes.md "TLS for
browsers"): dict "prefix" (OPENARCANUM_RELAY), "tls" (the values), "domains",
"cache" (acme_cache_dir, or "" for the node's default).
*/}}
{{- define "openarcanum.tlsEnv" -}}
{{- if ne .tls.mode "off" }}
- name: {{ .prefix }}_TLS_MODE
  value: {{ .tls.mode | quote }}
{{- if .domains }}
- name: {{ .prefix }}_TLS_DOMAINS
  value: {{ join "," .domains | quote }}
{{- end }}
{{- if .tls.email }}
- name: {{ .prefix }}_ACME_EMAIL
  value: {{ .tls.email | quote }}
{{- end }}
{{- if and (eq .tls.mode "acme") .cache }}
- name: {{ .prefix }}_ACME_CACHE_DIR
  value: {{ .cache | quote }}
{{- end }}
{{- if eq .tls.mode "files" }}
- name: {{ .prefix }}_TLS_CERT_FILE
  value: /tls/tls.crt
- name: {{ .prefix }}_TLS_KEY_FILE
  value: /tls/tls.key
{{- end }}
{{- end }}
{{- end -}}

{{/* acme needs domains, files a Secret. */}}
{{- define "openarcanum.checkTLS" -}}
{{- if and (eq .tls.mode "acme") (not .domains) -}}
{{- fail (printf "%s.tls.mode acme needs %s.tls.domains: the names (or public IP addresses) the CA checks on port 443" .what .what) -}}
{{- end -}}
{{- if and (eq .tls.mode "files") (not .tls.secretName) -}}
{{- fail (printf "%s.tls.mode files needs %s.tls.secretName: a kubernetes.io/tls Secret with tls.crt and tls.key" .what .what) -}}
{{- end -}}
{{- end -}}

{{/* relay.tls.mode files: the Secret with tls.crt and tls.key. */}}
{{- define "openarcanum.relayTLSVolume" -}}
{{- if eq .Values.relay.tls.mode "files" }}
- name: relay-tls
  secret:
    secretName: {{ .Values.relay.tls.secretName }}
    defaultMode: 0440
{{- end }}
{{- end -}}

{{/* A Service port: 443 with TLS on, else the server's port. */}}
{{- define "openarcanum.servicePort" -}}
{{- if ne .tls.mode "off" }}443{{ else }}{{ .port }}{{ end -}}
{{- end -}}

{{/* Comma-separated lists, as the servers read them from OPENARCANUM_* variables. */}}
{{- define "openarcanum.registryURLs" -}}
{{- if .Values.relay.registryURLs }}{{ join "," .Values.relay.registryURLs }}{{ else }}{{ include "openarcanum.metaURLs" . }}{{ end -}}
{{- end -}}

{{- define "openarcanum.communityRelays" -}}
{{- if .Values.community.relays }}{{ join "," .Values.community.relays }}{{ else }}{{ include "openarcanum.relayURL" . }}{{ end -}}
{{- end -}}

{{- define "openarcanum.communityMaintainers" -}}
{{- if .Values.community.maintainers }}{{ join "," .Values.community.maintainers }}{{ else }}{{ include "openarcanum.metaURLs" . }}{{ end -}}
{{- end -}}

{{- define "openarcanum.podSecurity" -}}
runAsNonRoot: true
runAsUser: {{ .Values.securityContext.runAsUser }}
runAsGroup: {{ .Values.securityContext.runAsGroup }}
fsGroup: {{ .Values.securityContext.fsGroup }}
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "openarcanum.containerSecurity" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: ["ALL"]
{{- end -}}

{{- define "openarcanum.extraEnv" -}}
{{- range $k, $v := . }}
- name: {{ $k }}
  value: {{ $v | quote }}
{{- end }}
{{- end -}}

{{/*
The relay container: the relay Deployment's, or, with relay.dedicated, a
second container in the community server's pod (docs/kubernetes.md
"A dedicated relay"). A dedicated relay reads the community server's key
from the file the server writes into the pod's shared serverkey volume,
keeps its network blocks on the relay-state volume, and registers with no
maintainer node.
*/}}
{{- define "openarcanum.relayContainer" -}}
- name: relay
  image: {{ include "openarcanum.image" (dict "root" . "component" "relay") }}
  imagePullPolicy: {{ .Values.image.pullPolicy }}
  securityContext:
    {{- include "openarcanum.containerSecurity" . | nindent 4 }}
  env:
    - name: OPENARCANUM_RELAY_LISTEN
      value: ":{{ .Values.relay.port }}"
    - name: OPENARCANUM_RELAY_PUBLIC_URL
      value: {{ include "openarcanum.relayURL" . | quote }}
    {{- if .Values.relay.dedicated }}
    - name: OPENARCANUM_RELAY_DEDICATED_SERVER_KEY_FILE
      value: /serverkey/server.pub
    - name: OPENARCANUM_RELAY_BLOCK_FILE
      value: /relay-state/blocks.json
    {{- else }}
    - name: OPENARCANUM_RELAY_REGISTRY_URLS
      value: {{ include "openarcanum.registryURLs" . | quote }}
    # The relay's identity in the registry; a new one after a restart
    # is fine (it registers again).
    - name: OPENARCANUM_RELAY_REGISTRY_KEY_FILE
      value: /state/registry.key
    {{- end }}
    - name: OPENARCANUM_RELAY_CALLS
      value: {{ .Values.relay.calls | quote }}
    - name: OPENARCANUM_RELAY_MEDIA_UDP_PORT
      value: {{ .Values.relay.mediaUDPPort | quote }}
    {{- with .Values.relay.mediaPublicIPs }}
    - name: OPENARCANUM_RELAY_MEDIA_PUBLIC_IPS
      value: {{ join "," . | quote }}
    {{- end }}
    - name: OPENARCANUM_RELAY_MEDIA_LOOPBACK
      value: {{ .Values.relay.mediaLoopback | quote }}
    - name: OPENARCANUM_RELAY_LOG_LEVEL
      value: {{ .Values.logLevel | quote }}
    {{- include "openarcanum.tlsEnv" (dict "prefix" "OPENARCANUM_RELAY" "tls" .Values.relay.tls "domains" .Values.relay.tls.domains "cache" (ternary "/relay-state/acme" "/acme/cache" .Values.relay.dedicated)) | nindent 4 }}
    {{- include "openarcanum.extraEnv" .Values.relay.env | nindent 4 }}
  ports:
    - name: ws
      containerPort: {{ .Values.relay.port }}
      protocol: TCP
    {{- if .Values.relay.calls }}
    - name: media
      containerPort: {{ .Values.relay.mediaUDPPort }}
      protocol: UDP
    {{- end }}
  {{- $scheme := ternary "" ", scheme: HTTPS" (eq .Values.relay.tls.mode "off") }}
  readinessProbe:
    httpGet: {path: /healthz, port: ws{{ $scheme }}}
    periodSeconds: 5
  livenessProbe:
    httpGet: {path: /healthz, port: ws{{ $scheme }}}
    periodSeconds: 20
    failureThreshold: 3
  volumeMounts:
    {{- if .Values.relay.dedicated }}
    - {name: serverkey, mountPath: /serverkey, readOnly: true}
    - {name: relay-state, mountPath: /relay-state}
    - {name: relay-tmp, mountPath: /tmp}
    {{- else }}
    - {name: state, mountPath: /state}
    - {name: tmp, mountPath: /tmp}
    {{- if eq .Values.relay.tls.mode "acme" }}
    - {name: acme, mountPath: /acme}
    {{- end }}
    {{- end }}
    {{- if eq .Values.relay.tls.mode "files" }}
    - {name: relay-tls, mountPath: /tls, readOnly: true}
    {{- end }}
  {{- with .Values.relay.resources }}
  resources:
    {{- toYaml . | nindent 4 }}
  {{- end }}
{{- end -}}

{{/* relay.dedicated needs the chart's community server, pinned to this relay only. */}}
{{- define "openarcanum.checkDedicated" -}}
{{- if .Values.relay.dedicated -}}
{{- if not (and .Values.relay.enabled .Values.community.enabled) -}}
{{- fail "relay.dedicated needs relay.enabled and community.enabled: the relay runs next to the chart's community server" -}}
{{- end -}}
{{- if .Values.community.relays -}}
{{- fail "relay.dedicated: leave community.relays empty, so the community is pinned to its dedicated relay only (docs/kubernetes.md)" -}}
{{- end -}}
{{- end -}}
{{- end -}}
