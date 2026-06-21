# chaski

A small, stateless **webhook relay** for Kubernetes. `POST` any JSON to a named
route; chaski gates it with a [CEL](https://github.com/google/cel-go)
expression, renders fields with Go templates, and relays the result to a
configured target — an [apprise](https://github.com/unraid/apprise-go)
notification (Pushover, ntfy, Discord, Gotify, Telegram, … 100+ services) or a
plain HTTP request.

> Named after the Inca Empire's _chasqui_ relay runners, who passed messages
> station-to-station along the royal roads.

## Features

- **Routing by path** — `POST /notify/{route}`; each route is configured
  independently and can fan out to several targets concurrently.
- **CEL gate** — a per-route `whenExpr` decides whether a request is relayed.
- **Go-template fields** — `title`, `message`, per-provider `params`, and HTTP
  `headers` are rendered per request with [sprout](https://github.com/go-sprout/sprout)
  helpers and `{{ env "…" }}` (no filesystem or network access).
- **Two target kinds** — an apprise notification or a generic HTTP forward.
- **SMTP ingestion (optional)** — accept notifications as email so appliances
  that can only send mail reach the same routes; off by default.
- **Signature verification** — optional per-route HMAC or shared-token check
  over the raw body, with a built-in `github` preset.
- **Layered config** — a single YAML file or a `config.d` directory of
  fragments merged additively; secrets injected with `{{ env "…" }}` in any
  field.
- **Safe by default** — optional bearer-token auth, body-size limits,
  per-request deadlines, retry with backoff, and a `?dryRun=1` preview that
  never sends.
- **Operable** — Prometheus metrics, a health probe, structured logs, graceful
  shutdown, and a published JSON Schema for editor validation.

## Usage

Define targets and routes in a config file:

```yaml
# chaski.yaml
targets:
  pushover:
    apprise:
      url: '{{ env "PUSHOVER_URL" }}' # pover://user@token

routes:
  alertmanager:
    target: pushover
    whenExpr: payload.status == "firing"
    title: "[{{ .payload.commonLabels.severity }}] {{ .payload.commonLabels.alertname }}"
    message: "{{ .payload.commonAnnotations.summary }}"
```

Run chaski and send it a webhook. Inbound token auth is off by default — fine
for cluster-internal senders; set `CHASKI_WEBHOOK_TOKEN` to require a bearer
token (see [Configuration](#configuration)):

```sh
CHASKI_CONFIG=./chaski.yaml chaski

curl -X POST http://localhost:8080/notify/alertmanager \
  -H "Content-Type: application/json" \
  -d '{"status":"firing","commonLabels":{"severity":"critical","alertname":"HighCPU"}}'
```

Append `?dryRun=1` to preview the matched targets and rendered fields without
sending — including `"fired": false` when the `whenExpr` gate doesn't match, so
you can see _why_ a route wouldn't act. Every response also carries an
`X-Chaski-Result` header (`relayed`, `skipped:gate`, `skipped:no_targets`,
`render_error`, …) so the outcome is visible even on a bodyless status. Check a
config before deploying it (the same checks the server runs at boot):

```sh
chaski validate -c ./chaski.yaml
```

Render a route against a saved sample body offline — no running server — to see
the rendered fields (and catch a wrong field path or typo'd key before deploy):

```sh
chaski validate -c ./chaski.yaml --payload sample.json --route alertmanager
```

## Targets

A route relays to one or more named targets; a target is exactly one of two
kinds.

### apprise

A notification through [apprise-go](https://github.com/unraid/apprise-go): the
URL scheme picks the provider and carries its credentials. A route's `params`
are URL-encoded onto the target URL's query, so the keys you set are whatever
the chosen provider recognizes — Pushover, for example, reads `priority`,
`sound`, `url`, and `url_title`.

```yaml
targets:
  pushover:
    apprise:
      url: '{{ env "PUSHOVER_URL" }}' # pover://user@token

routes:
  backup:
    target: pushover
    title: "Backup {{ .payload.status }}"
    message: "{{ .payload.detail }}"
    params:
      priority: high # low | normal | high | emergency (or -2..2)
      sound: falling
      url: "{{ .payload.dashboard }}"
      url_title: Open dashboard
```

`title` is the notification title, `message` the body; an empty or omitted
`message` skips the send (a bodyless notification is pointless). See the
[apprise-go provider](https://github.com/unraid/apprise-go) source for the query
keys each service accepts.

### http

A generic HTTP request — only `url` is required:

```yaml
targets:
  ops-bridge:
    http:
      url: https://ops.example.internal/ingest # required
      method: POST # default: POST
      headers: # static or templated values
        Authorization: '{{ env "OPS_TOKEN" }}'
        Content-Type: application/json
      timeout: 5s # default: CHASKI_REQUEST_TIMEOUT
      retry: # default: the CHASKI_RETRY_* env
        attempts: 3
        backoff: 1s

routes:
  forward:
    target: ops-bridge
    # No `message`: the inbound request body is forwarded verbatim. Set `message`
    # to send a rendered body instead. A route's `headers` merge onto the
    # target's (the target wins a name clash).
```

A 2xx is success, a 4xx is permanent (not retried), and a 5xx or transport
error is retried with exponential backoff + jitter, bounded by the deadline.

## Templating

`whenExpr` is [CEL](https://github.com/google/cel-go); every other field is a Go
[`text/template`](https://pkg.go.dev/text/template) with
[sprout](https://docs.atom.codes/sprout) helpers and `{{ env "…" }}`. Both see
the same variables: `payload`, `headers`, `query`, `method`, `route`, `now`.

A richer **apprise** route — a severity-driven title, a bullet per alert, and a
priority/sound chosen from the payload:

```yaml
routes:
  alertmanager:
    target: pushover
    whenExpr: payload.status == "firing"
    title: '[{{ .payload.commonLabels.severity | default "info" | toUpper }}] {{ .payload.commonLabels.alertname }} ({{ len .payload.alerts }} firing)'
    message: |-
      {{ len .payload.alerts }} firing — {{ .payload.commonLabels.alertname }}
      {{ range .payload.alerts }}
      • {{ .annotations.summary | trunc 140 }}{{ end }}
    params:
      priority: '{{ ternary "2" "0" (eq .payload.commonLabels.severity "critical") }}'
      sound: '{{ ternary "alien" "pushover" (eq .payload.commonLabels.severity "critical") }}'
      url: '{{ .payload.commonAnnotations.runbook_url | default "" }}'
      url_title: Runbook
```

> Renders `[CRITICAL] HighMemory (2 firing)` — `default` supplies a fallback,
> `toUpper` caps it, `len` counts, `range` lists each alert, `trunc` bounds the
> length, and `ternary`/`eq` map the severity to a Pushover priority and sound.

A richer **http** target — Basic auth assembled from two secrets, a normalized
JSON body, and a content-hash idempotency key:

```yaml
targets:
  bridge:
    http:
      url: https://hooks.example.internal/ingest
      headers:
        Content-Type: application/json
        Authorization: 'Basic {{ printf "%s:%s" (env "BRIDGE_USER") (env "BRIDGE_PASS") | base64Encode }}'

routes:
  forward:
    target: bridge
    message: |-
      {{- $b := dict
            "event" (.payload.eventType | default "unknown" | toLower)
            "repo"  (dig "repository" "full_name" .payload | default "n/a")
            "url"   (.payload.html_url | default "") -}}
      {{ toJSON $b }}
    headers:
      X-Idempotency-Key: "{{ .payload.id | toString | sha256Sum }}"
      X-Event: '{{ .payload.eventType | default "unknown" }}'
```

> Body → `{"event":"push","repo":"home-operations/chaski","url":"…"}` — `dict` +
> `toJSON` build the payload, `dig … | default` reads a nested field safely,
> `base64Encode` assembles Basic auth, and `sha256Sum` derives a stable key.

Helpers come from sprout's safe registries — `default`/`coalesce`/`ternary`,
strings (`trunc`, `toTitleCase`, `replace`, `trimAll`), `dict`/`dig`/`pluck`,
`toJSON`/`base64Encode`, `sha256Sum`, and time (`dateInZone`, `.now.UTC.Format`);
filesystem and network helpers are excluded. See the
[sprout registries](https://docs.atom.codes/sprout/registries) for the full set.

### Shared snippets

A top-level `templates:` block holds named Go-template snippets. Any route field
(`title`, `message`, `params.*`, `headers.*`) can reuse one with
`{{ template "name" . }}`, or `{{ include "name" . }}` when you need to pipe its
output. Snippets compose (one can call another) and render against the same
variables, so shared formatting lives in one place instead of being copied into
every route:

```yaml
templates:
  # A severity label reused by several routes.
  label: '[{{ .payload.severity | default "info" | toUpper }}]'

routes:
  alerts:
    target: pushover
    title: '{{ template "label" . }} {{ .payload.title }}'
    message: "{{ .payload.detail }}"
  audit:
    target: pushover
    title: '{{ include "label" . }} audit: {{ .payload.action }}'
```

Both routes render `[CRITICAL] …` from the one `label` snippet. Snippet wiring
is checked at boot / `chaski validate`, so the whole class of typos fails fast
rather than at request time: a malformed snippet, a reference to an undefined
snippet, and a reference cycle (`a` → `b` → `a`) are all rejected before the
server serves. An `include` name must be a string literal (`{{ include "label"
. }}`), like the `{{ template }}` action. `whenExpr` is CEL, so snippets don't
apply there — only to the Go-template fields.

## SMTP ingestion

chaski can also accept notifications over **SMTP**, so devices that can only send
email (printers, NAS boxes, UPSes, legacy monitoring) reach the same routes. It is
**off by default**; enable it with `CHASKI_SMTP_ENABLED=true`.

The recipient's localpart selects the route: mail to `printer@chaski` is handled by
the route named `printer`. An unknown recipient is rejected, so the listener is never
an open relay. The parsed message is exposed to `whenExpr` and templates as `payload`:

| Field                           | Meaning                                |
| ------------------------------- | -------------------------------------- |
| `payload.subject`               | decoded `Subject`                      |
| `payload.from`                  | first `From` address                   |
| `payload.to`                    | the recipient that selected this route |
| `payload.body`                  | the text part, falling back to HTML    |
| `payload.text` / `payload.html` | the individual parts                   |

```yaml
routes:
  printer:
    target: pushover
    title: "{{ .payload.subject }}"
    message: "{{ .payload.body }}"
```

Send it with any mail client:

```sh
swaks --server chaski:8025 --to printer@chaski --from device@lan \
  --header "Subject: Toner low" --body "tray 1 empty"
```

**Security.** v1 has no TLS, so run the listener on a trusted network (a ClusterIP
behind a NetworkPolicy). Set `CHASKI_SMTP_AUTH` to a `user:password,…` list to require
SMTP AUTH (PLAIN/LOGIN); with no TLS, those credentials travel in clear text. Per-route
`verify` (HMAC/token) does not apply to SMTP; AUTH and network isolation are the gate.
Attachments are not forwarded.

## Configuration

`whenExpr` is CEL; every other field is a Go template. Both expose the same
variables: `payload`, `headers`, `query`, `method`, `route`, and `now`.

Routes and targets are loaded from `CHASKI_CONFIG`; operational settings come
from the environment:

| Variable                 | Default               | Description                                    |
| ------------------------ | --------------------- | ---------------------------------------------- |
| `CHASKI_CONFIG`          | `/config/chaski.yaml` | Route config file or `config.d` directory      |
| `CHASKI_WEBHOOK_TOKEN`   | _(none)_              | Optional inbound bearer token; unset = no auth |
| `CHASKI_PORT`            | `8080`                | Webhook listener                               |
| `CHASKI_METRICS_PORT`    | `8081`                | Metrics + health listener                      |
| `CHASKI_MAX_BODY_BYTES`  | `1048576`             | Inbound body size cap                          |
| `CHASKI_REQUEST_TIMEOUT` | `15s`                 | Whole-request deadline                         |
| `CHASKI_RETRY_ATTEMPTS`  | `3`                   | Default per-target retry attempts              |
| `CHASKI_RETRY_BACKOFF`   | `200ms`               | Default retry backoff (exponential)            |
| `CHASKI_SMTP_ENABLED`    | `false`               | Enable the SMTP ingestion listener             |
| `CHASKI_SMTP_PORT`       | `8025`                | SMTP listener port (when enabled)              |
| `CHASKI_SMTP_AUTH`       | _(none)_              | `user:password,…` list; set ⇒ require AUTH     |

A JSON Schema for the route config is published at
[`config.schema.json`](config.schema.json) — point your editor's YAML language
server at it for completion and validation. The metrics port serves
`GET /metrics` (Prometheus) and `GET /healthz`.

Key metrics:

| Series                                 | Labels                      | Meaning                                              |
| -------------------------------------- | --------------------------- | ---------------------------------------------------- |
| `chaski_relays_total`                  | `route`, `result`           | Relay outcomes per route                             |
| `chaski_target_sends_total`            | `target`, `kind`, `outcome` | Per-target sends (`success`/`permanent`/`retryable`) |
| `chaski_target_retries_total`          | `target`, `kind`            | Retried send attempts per target                     |
| `chaski_webhook_rejected_total`        | `reason`                    | Inbound rejects (token/signature/body/…)             |
| `chaski_smtp_rejected_total`           | `reason`                    | SMTP rejects (`auth`/`recipient`)                    |
| `chaski_http_request_duration_seconds` | `method`                    | Inbound request latency                              |
| `chaski_build_info`                    | `version`, `commit`         | Running build (value `1`)                            |

## Deployment

A Helm chart is published as an OCI artifact and runs the distroless
`ghcr.io/home-operations/chaski` image:

```sh
helm install chaski oci://ghcr.io/home-operations/charts/chaski
```

It defaults to a RollingUpdate strategy and rolls pods automatically when the
config changes.

## Development

The toolchain is pinned with [mise](https://mise.jdx.dev); `mise tasks` lists
everything.

```sh
mise run build          # go build ./...
mise run test           # go test -race ./... with coverage
mise run lint           # golangci-lint
mise run helm-unittest  # chart template tests
```

## License

[AGPL-3.0](LICENSE).
