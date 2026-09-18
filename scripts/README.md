# Local OAuth callback helper

Browser -> local callback helper -> CLIProxyAPI HTTP(S) endpoint.
Requires Python 3.10+ only. No SSH, root access, keychain or pip packages.

## Start before requesting a login

```sh
python scripts/commandcode_loopback_bridge.py --server https://proxy.example.com
```

Alternatively set `COMMANDCODE_SERVER_URL`. A reverse-proxy path prefix is supported.
For explicit plaintext HTTP use:

```sh
python scripts/commandcode_loopback_bridge.py --server http://127.0.0.1:8317 --allow-http
```

HTTP transmits API keys and session tokens without encryption. Prefer HTTPS,
particularly on public networks. HTTPS certificates are verified; there is no
insecure TLS switch. Redirects are not followed with credential headers.

1. Start the helper on the browser machine (default port 5959).
2. Request a new Command Code login in the management panel.
3. Open its local `/start?state=...&bridge_token=...` link in that browser.
4. Authorize on the official site. The helper checks state and forwards the
   callback to the configured server, then exits after a server response.

The configured server must be the one that created the login session. Optional
`--port` accepts 5959..5968 and must match the generated login link. Start only
one login at a time. After a delivery error restart the helper and request a new
login; do not reuse an old callback.

The existing plugin dispatches callbacks as GET with percent-encoded
`X-CommandCode-*` headers, not credential query parameters. Reverse proxies must
preserve those headers and must not log their values. The helper suppresses
request logs; do not share login or callback URLs. It listens only on IPv4 loopback.
No server deployment or management-key configuration is performed by this tool.
