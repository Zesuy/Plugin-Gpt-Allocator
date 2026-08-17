# CPA Route Allocator

`cpa-route-allocator` is a native CLIProxyAPI plugin for importing credentials, applying group defaults, and assigning preconfigured Mihomo Listener/Selector routes.

The plugin runs inside CLIProxyAPI. It reuses the existing CPA port and Management API authentication; it does not start another HTTP server.

## Current milestone

- CLIProxyAPI native ABI v1 and RPC schema v3 registration
- embedded Management Center page at `/v0/resource/plugins/cpa-route-allocator/upload` with overview, import, and configuration views
- authenticated state, settings, full group and Listener/Selector CRUD, Selector discovery, and import-preview routes
- atomic local state with Mihomo secret redaction
- initial sub2api/CPA-style JSON discovery and conversion
- stable identity using provider, full email, account, and workspace information
- CPA Auth persistence through `host.auth.save`
- idempotent updates that keep the original group and route
- LRU free-slot assignment plus share-least, reject, and default-route shortage policies
- imports without an available Mihomo controller are retained as `pending` instead of pretending the route is ready
- existing CPA Auth discovery and adoption, plus safe unmanage (the Auth file is retained)
- per-credential enable/disable switch delegated to CPA's auth-files status API
- credential overview grouped by allocator group
- self-use Codex quota check per credential, with independent primary/secondary windows and cached errors
- short toast feedback in the UI; credential and Listener edits use accessible modal dialogs
- CPA `usage.handle` summaries split into SSE, WebSocket, other-stream, and non-stream buckets
- Mihomo `/proxies` alive/history data exposed during route sync for node health display
- on-demand public egress IP detection through `chatgpt.com/cdn-cgi/trace`

## Build

Linux AMD64 is the local development target:

```bash
make test
make build
```

The plugin is written to `dist/cpa-route-allocator.so`.

## Install in CLIProxyAPI

Copy the dynamic library to the platform plugin directory:

```text
plugins/linux/amd64/cpa-route-allocator.so
```

Enable both the plugin host and this plugin in the CLIProxyAPI configuration:

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-route-allocator:
      enabled: true
```

The default state location is:

```text
plugins/data/cpa-route-allocator/state.json
```

It can be overridden for local testing with `CPA_ROUTE_ALLOCATOR_STATE_PATH`.

Credential enable/disable uses CPA's own Management API through the local loopback
listener (`http://127.0.0.1:8317` by default). Set `CPA_ROUTE_ALLOCATOR_CPA_URL` if
the Management API listens elsewhere.

## Routes

Static UI:

```text
GET /v0/resource/plugins/cpa-route-allocator/upload
```

Authenticated Management API:

```text
GET  /v0/management/plugins/cpa-route-allocator/state
PUT  /v0/management/plugins/cpa-route-allocator/settings
GET  /v0/management/plugins/cpa-route-allocator/groups
POST /v0/management/plugins/cpa-route-allocator/groups
PUT  /v0/management/plugins/cpa-route-allocator/groups
PUT  /v0/management/plugins/cpa-route-allocator/groups/order
DELETE /v0/management/plugins/cpa-route-allocator/groups
GET  /v0/management/plugins/cpa-route-allocator/route-slots
POST /v0/management/plugins/cpa-route-allocator/route-slots
PUT  /v0/management/plugins/cpa-route-allocator/route-slots
DELETE /v0/management/plugins/cpa-route-allocator/route-slots
GET  /v0/management/plugins/cpa-route-allocator/mihomo/status
GET  /v0/management/plugins/cpa-route-allocator/network/public-ip
GET  /v0/management/plugins/cpa-route-allocator/stats
GET  /v0/management/plugins/cpa-route-allocator/mihomo/selectors
POST /v0/management/plugins/cpa-route-allocator/route-slots/sync
POST /v0/management/plugins/cpa-route-allocator/route-slots/select
POST /v0/management/plugins/cpa-route-allocator/import/preview
POST /v0/management/plugins/cpa-route-allocator/upload
POST /v0/management/plugins/cpa-route-allocator/import
PUT  /v0/management/plugins/cpa-route-allocator/credentials/alias
POST /v0/management/plugins/cpa-route-allocator/credentials/move
PUT  /v0/management/plugins/cpa-route-allocator/credentials/status
POST /v0/management/plugins/cpa-route-allocator/credentials/quota
GET  /v0/management/plugins/cpa-route-allocator/credentials/local
POST /v0/management/plugins/cpa-route-allocator/credentials/adopt
DELETE /v0/management/plugins/cpa-route-allocator/credentials/managed
```

Management routes use the normal CPA Management Key.
