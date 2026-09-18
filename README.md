# CPA CommandCode Provider

Command Code provider plugin for CLIProxyAPI.

## Features

- Incremental streaming chat completions.
- System/developer instructions translated to the upstream system field.
- Tool definitions, streamed tool calls, and tool-result round trips.
- Provider-free model IDs with upstream ID resolution and collision protection.
- Browser login and quota management integration.

## Build and test

Requires Go 1.26, CGO and a C compiler. Build for the same OS/architecture as the host.

```sh
go test -count=1 ./...
mkdir -p dist
go build -buildmode=c-shared -o dist/commandcode.so .
```

Linux/amd64 reproducible build environment:

```sh
docker run --rm --platform linux/amd64 -v "$PWD:/src" -w /src golang:1.26-bookworm sh -c 'go test -count=1 ./... && mkdir -p dist && go build -buildmode=c-shared -o dist/commandcode.so .'
```

Back up the installed plugin, replace its shared library in the host plugins directory, then restart the host. This repository does not deploy automatically. Configure credentials through the host login flow; no production credentials are included.

## Source provenance

The minimal CLIProxyAPI SDK snapshot is included in `third_party/cliproxyapi` with its upstream license. The local module replacement keeps builds independent of later SDK changes.

The generated model list contains 44 models. Unique model IDs omit the first provider segment; ambiguous names retain their full IDs. Existing clients should refresh their model list.

## Quota dashboard and release source

The quota resource serves a public HTML shell only. Account lists and quota JSON require a management bearer token, validated against a trusted management endpoint. Each account has independent cached quota data and refresh controls. The dashboard reuses same-origin management login on HTTPS/loopback; HTTP deployments on any host are allowed only inside a same-origin `management.html` iframe. HTTP transmits management credentials in plaintext; HTTPS is strongly recommended.

Build releases from this repository's complete committed tree (for example, `git archive HEAD`), including `quota_dashboard.html` and the pinned SDK. Avoid building from stale copies or selectively uploading source files: always use the complete current tree. Verify both quota cards and streaming/non-streaming cache usage after deployment. The host binary, panel HTML, credentials and compose configuration must remain unchanged.

### Management validation endpoint

The default is `http://127.0.0.1:8317`. For another internal port, HTTPS listener,
or reverse-proxy prefix, set `COMMANDCODE_MANAGEMENT_BASE_URL` in the **CPA host
process environment** before starting it, for example:

```sh
export COMMANDCODE_MANAGEMENT_BASE_URL=http://127.0.0.1:9321
```

Use a trusted endpoint of this same CPA instance, without `/v0/management/config`
(the plugin appends that path). HTTPS certificates must be valid. Redirects are
not followed. Browser-supplied Host/Origin headers cannot change this destination.
Docker published ports need no change if CPA still listens internally on 8317.
Rebuild and replace the plugin to apply these fixes; existing binaries are unchanged.
