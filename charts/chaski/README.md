# chaski

![Version: 0.0.0](https://img.shields.io/badge/Version-0.0.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 0.0.0](https://img.shields.io/badge/AppVersion-0.0.0-informational?style=flat-square)

A stateless **webhook relay**: `POST` any JSON to a named route, gate it with a
[CEL](https://github.com/google/cel-go) expression, render fields with Go
templates, and relay the result to an
[apprise](https://github.com/unraid/apprise-go) notification (Pushover, ntfy,
Discord, gotify, Telegram, …) **or** a generic HTTP request.

**Homepage:** <https://github.com/home-operations/chaski>

## Installing

The chart is published as a Cosign-signed OCI artifact:

```bash
helm install chaski oci://ghcr.io/home-operations/charts/chaski --version <version>
```

Verify the signature (keyless, GitHub Actions OIDC):

```bash
cosign verify ghcr.io/home-operations/charts/chaski:<version> \
  --certificate-identity-regexp '^https://github.com/home-operations/chaski/\.github/workflows/release\.yaml@.*$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Configuration

Two halves:

- **Ops + secrets** map to `CHASKI_*` environment variables via the `config.*`
  (ports, logging, retry, timeouts) and `auth.*` (the inbound token) values.
- **Routes + targets** are the relay's behaviour. Set `config.routes` and
  `config.targets` and the chart renders a ConfigMap mounted at
  `config.mountPath`; or point `config.existingConfigMap` at your own (e.g. a
  `config.d` directory of fragments). The route/target YAML is emitted
  **verbatim** — the `{{ … }}` inside route fields is chaski's
  own CEL / Go-template syntax, not Helm's. Provider credentials are referenced
  as `{{ env "VAR" }}` and supplied from a Secret via `envFrom`.

Validate a config the way the server does at boot (the CI gate):

```bash
chaski validate -c /config/chaski.yaml
```

See the [route-config schema](https://github.com/home-operations/chaski/blob/main/config.schema.json) for the
field reference.

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| home-operations | <contact@home-operations.com> |  |

## Source Code

* <https://github.com/home-operations/chaski>

## Requirements

Kubernetes: `>=1.25.0-0`

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity rules for pod scheduling (templated). |
| auth.existingSecret | string | `""` | Use this existing Secret for the webhook token (and as a source for provider creds via `envFrom`) instead of rendering one. |
| auth.existingSecretKey | string | `"token"` | Key in `existingSecret` holding the webhook token. |
| auth.smtpAuth | string | `""` | Optional SMTP AUTH credentials as a `user:password,user2:password2` list (CHASKI_SMTP_AUTH), used only when `config.smtpEnabled`. When set, SMTP AUTH (PLAIN/LOGIN) is required and the value is rendered into the chart-managed Secret. With `existingSecret`, supply `CHASKI_SMTP_AUTH` via `envFrom`/`extraEnv` instead. NOTE: v1 has no TLS, so credentials travel in clear text — keep the listener internal. |
| auth.webhookToken | string | `""` | The global inbound webhook token (CHASKI_WEBHOOK_TOKEN). If set, it is rendered into a chart-managed Secret. Leave empty and use `existingSecret` to supply it from your own (e.g. SOPS/sealed) Secret. With neither, inbound token auth is disabled (per-route `verify` still applies). |
| config.configPath | string | `"/config/chaski.yaml"` | Path chaski reads its config from (CHASKI_CONFIG): a single file, or a directory of `*.yaml` fragments (config.d). The default matches the rendered ConfigMap. |
| config.existingConfigMap | string | `""` | Mount this existing ConfigMap instead of rendering one from `routes`/`targets` (e.g. for a config.d directory of fragments). Set `configPath` to `/config` for directory mode. |
| config.logFormat | string | `"json"` | Log format (CHASKI_LOG_FORMAT): json or text. |
| config.logLevel | string | `"info"` | Log level (CHASKI_LOG_LEVEL): debug, info, warn, or error. |
| config.logUnknownRoutes | bool | `false` | Log bodies POSTed to nonexistent routes, still answering 404 (CHASKI_LOG_UNKNOWN_ROUTES) — payload discovery before a route exists. Pre-verify bodies; enable deliberately, turn off when done. |
| config.maxBodyBytes | int | `1048576` | Inbound body cap in bytes (CHASKI_MAX_BODY_BYTES). |
| config.metricsEnabled | bool | `true` | Expose Prometheus metrics + the /healthz probe on `metricsPort` (CHASKI_METRICS_ENABLED). |
| config.metricsPort | int | `8081` | Metrics + health port (CHASKI_METRICS_PORT); kept off the public webhook port. |
| config.mountPath | string | `"/config"` | Where the config ConfigMap is mounted. |
| config.port | int | `8080` | Webhook receiver port (CHASKI_PORT); also the container/Service http port. |
| config.requestTimeout | string | `"15s"` | Whole-request deadline (CHASKI_REQUEST_TIMEOUT): decode + gate + render + send + retry. |
| config.retryAttempts | int | `3` | Global default retry attempts per target (CHASKI_RETRY_ATTEMPTS); a target's `retry.attempts` overrides it. |
| config.retryBackoff | string | `"200ms"` | Global default retry base backoff (CHASKI_RETRY_BACKOFF); a target's `retry.backoff` overrides it. |
| config.routes | object | `{}` | Route definitions, keyed by name (the URL path: POST /hooks/{name}). Emitted verbatim into the ConfigMap — `{{ ... }}` here is chaski's CEL/Go-template syntax, not Helm's. See config.schema.json for the field reference. |
| config.smtpEnabled | bool | `false` | Accept notifications over SMTP, relaying by the recipient localpart (sonarr@… → the route named `sonarr`). Off by default — it is an inbound relay path, so opt in explicitly (CHASKI_SMTP_ENABLED). When on, set `auth.smtpAuth` and keep the listener on a trusted network (v1 has no TLS). |
| config.smtpHostname | string | `"chaski"` | Hostname announced in the SMTP greeting (CHASKI_SMTP_HOSTNAME). |
| config.smtpMaxMessageBytes | int | `1048576` | Max inbound message size in bytes (CHASKI_SMTP_MAX_MESSAGE_BYTES). |
| config.smtpMaxRecipients | int | `50` | Max recipients per message (CHASKI_SMTP_MAX_RECIPIENTS). |
| config.smtpPort | int | `8025` | SMTP listener port (CHASKI_SMTP_PORT); also the container/Service smtp port when enabled. |
| config.targets | object | `{}` | Target (sink) definitions, keyed by name. Each is exactly one of `apprise:` or `http:`. Credentials belong in `{{ env "VAR" }}` references resolved from the environment (see `auth`/`envFrom`), never inline. |
| config.templates | object | `{}` | Shared named Go-template snippets, keyed by name. Callable from any route field with `{{ template "name" . }}` or `{{ include "name" . }}`. Emitted verbatim into the ConfigMap alongside routes/targets. |
| deploymentAnnotations | object | `{}` | Annotations added to the Deployment metadata, e.g. `reloader.stakater.com/auto: "true"` to roll the pod when a referenced ConfigMap/Secret changes (recommended when using `existingConfigMap`). |
| env | object | `{}` | Extra environment variables as a map (templated). Use for non-secret values referenced by `{{ env "…" }}` in targets, e.g. GOTIFY_HOST. |
| envFrom | list | `[]` | Sources of environment variables (templated). The idiomatic place to inject provider credentials from your own Secret(s), e.g. `- secretRef: { name: chaski-providers }`. |
| extraEnv | list | `[]` | Extra environment variables as a raw list (templated), e.g. valueFrom a Secret key for a single provider token. |
| fullnameOverride | string | `""` | Override the generated name used for every resource's `metadata.name` (the chart "fullname"). |
| image.digest | string | `""` | Pin the image by digest (sha256:…); set by the release pipeline. When set, it overrides the tag. |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy. |
| image.repository | string | `"ghcr.io/home-operations/chaski"` | Image repository. |
| image.tag | string | `""` | Overrides the image tag; defaults to the chart appVersion. |
| imagePullSecrets | list | `[]` | Image pull secrets for private registries. |
| initContainers | list | `[]` | Additional init containers (templated). A `chaski validate` init container against the mounted config is a good CI/rollout gate. |
| livenessProbe | object | `{"httpGet":{"path":"/healthz","port":"metrics"},"periodSeconds":20}` | Liveness probe. |
| monitoring.serviceMonitor.annotations | object | `{}` | ServiceMonitor annotations. |
| monitoring.serviceMonitor.enabled | bool | `false` | Create a Prometheus Operator ServiceMonitor (requires its CRDs and `config.metricsEnabled`). |
| monitoring.serviceMonitor.interval | string | `"30s"` | Scrape interval. |
| monitoring.serviceMonitor.labels | object | `{}` | ServiceMonitor labels. |
| monitoring.serviceMonitor.metricRelabelings | list | `[]` | Prometheus metric relabelings. |
| monitoring.serviceMonitor.path | string | `"/metrics"` | Metrics path. |
| monitoring.serviceMonitor.relabelings | list | `[]` | Prometheus relabelings. |
| monitoring.serviceMonitor.scrapeTimeout | string | `"10s"` | Scrape timeout. |
| nameOverride | string | `""` | Override the chart name used in the `app.kubernetes.io/name` label. |
| nodeSelector | object | `{}` | Node selector for pod scheduling (templated). |
| podAnnotations | object | `{}` | Annotations added to the pod. A `checksum/config` annotation is always added for the chart-managed ConfigMap so config edits roll the pod. |
| podDisruptionBudget.enabled | bool | `false` | Create a PodDisruptionBudget (meaningful only with replicaCount > 1). |
| podDisruptionBudget.maxUnavailable | string | `""` | maxUnavailable. |
| podDisruptionBudget.minAvailable | int | `1` | minAvailable (mutually exclusive with maxUnavailable). |
| podLabels | object | `{}` | Labels added to the pod. |
| podSecurityContext | object | `{"fsGroup":65532,"runAsGroup":65532,"runAsNonRoot":true,"runAsUser":65532,"seccompProfile":{"type":"RuntimeDefault"}}` | Pod-level securityContext. chaski runs as the image's nonroot user (uid 65532); no host or elevated access is needed. |
| priorityClassName | string | `""` | PriorityClass for the pod (templated); empty uses the cluster default. |
| readinessProbe | object | `{"httpGet":{"path":"/healthz","port":"metrics"},"periodSeconds":10}` | Readiness probe. |
| replicaCount | int | `1` | Number of replicas. chaski is stateless, so >1 is just more capacity/HA. |
| resources | object | `{}` | Container resource requests/limits. |
| securityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"privileged":false,"readOnlyRootFilesystem":true}` | Container securityContext. Drops all capabilities, read-only root filesystem, no privilege escalation. |
| service.annotations | object | `{}` | Service annotations. |
| service.externalTrafficPolicy | string | `""` | externalTrafficPolicy (only applied when type is not ClusterIP). |
| service.port | int | `8080` | Webhook (http) service port. |
| service.type | string | `"ClusterIP"` | Service type. |
| serviceAccount.annotations | object | `{}` | ServiceAccount annotations. |
| serviceAccount.automount | bool | `false` | Mount the API token. chaski never calls the Kubernetes API, so this is off by default. |
| serviceAccount.create | bool | `true` | Create a ServiceAccount. |
| serviceAccount.name | string | `""` | ServiceAccount name; empty uses the chart fullname. |
| startupProbe | object | `{"failureThreshold":30,"httpGet":{"path":"/healthz","port":"metrics"},"periodSeconds":2}` | Startup probe (GET /healthz on the metrics port). |
| strategy | object | `{"rollingUpdate":{"maxSurge":1,"maxUnavailable":0},"type":"RollingUpdate"}` | Deployment update strategy. RollingUpdate by default; chaski is stateless so a surge-then-drain rollout is safe. |
| terminationGracePeriodSeconds | int | `30` | Grace period for a clean shutdown (drain). |
| tests.image.pullPolicy | string | `"IfNotPresent"` | `helm test` image pull policy. |
| tests.image.repository | string | `"mirror.gcr.io/curlimages/curl"` | `helm test` connection-pod image; a gcr-mirrored curl, so the test never pulls from Docker Hub. |
| tests.image.tag | string | `"8.21.0@sha256:7c12af72ceb38b7432ab85e1a265cff6ae58e06f95539d539b654f2cfa64bb13"` | `helm test` image, pinned as `tag@sha256:digest` so Renovate bumps the tag and its digest together. |
| tolerations | list | `[]` | Tolerations for pod scheduling (templated). |
| topologySpreadConstraints | list | `[]` | Topology spread constraints (templated). |
| volumeMounts | list | `[]` | Additional volume mounts on the container (templated). |
| volumes | list | `[]` | Additional volumes on the pod (templated). |

---

_This README is generated by [helm-docs](https://github.com/norwoodj/helm-docs) from `Chart.yaml` and `values.yaml`. Edit those (or `README.md.gotmpl`) and run `mise run generate`._
