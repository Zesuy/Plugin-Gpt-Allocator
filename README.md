# CPA Route Allocator

`cpa-route-allocator` is a native CLIProxyAPI plugin for importing credentials, applying group defaults, and assigning preconfigured Mihomo Listener/Selector routes.

The plugin runs inside CLIProxyAPI. It reuses the existing CPA port and Management API authentication; it does not start another HTTP server.

## Current milestone

- CLIProxyAPI native ABI v1 and RPC schema v3 registration
- embedded Management Center page at `/v0/resource/plugins/cpa-route-allocator/upload`
- authenticated state, settings, group, Listener/Selector, and import-preview routes
- atomic local state with Mihomo secret redaction
- initial sub2api/CPA-style JSON discovery and conversion
- stable identity using provider, full email, account, and workspace information
- CPA Auth persistence through `host.auth.save`
- idempotent updates that keep the original group and route
- LRU free-slot assignment plus share-least, reject, and default-route shortage policies

Live Mihomo reconciliation and the full management UI are the next milestones.

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

## Routes

Static UI:

```text
GET /v0/resource/plugins/cpa-route-allocator/upload
```

Authenticated Management API:

```text
GET  /v0/management/plugins/cpa-route-allocator/state
PUT  /v0/management/plugins/cpa-route-allocator/settings
PUT  /v0/management/plugins/cpa-route-allocator/groups
PUT  /v0/management/plugins/cpa-route-allocator/route-slots
GET  /v0/management/plugins/cpa-route-allocator/mihomo/status
POST /v0/management/plugins/cpa-route-allocator/route-slots/sync
POST /v0/management/plugins/cpa-route-allocator/route-slots/select
POST /v0/management/plugins/cpa-route-allocator/import/preview
POST /v0/management/plugins/cpa-route-allocator/upload
POST /v0/management/plugins/cpa-route-allocator/import
```

Management routes use the normal CPA Management Key.
