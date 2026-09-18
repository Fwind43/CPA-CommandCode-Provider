"""Secure loopback OAuth bridge for CLIProxyAPI CommandCode."""
from __future__ import annotations
import argparse, hmac, http.client, http.server, json, os, re, threading, urllib.parse

MAX_URL=8192
MAX_BODY=16384
STATE_RE=re.compile(r"^[A-Za-z0-9_-]{43}$")
ALLOWED={"apiKey","state","userId","userName","keyName"}
TRUSTED_ORIGINS={"https://commandcode.ai","https://staging.commandcode.ai"}
OFFICIAL="https://commandcode.ai/studio/auth/cli"


def configure_endpoint(url, allow_http=False):
    u = urllib.parse.urlsplit(url)
    if (u.scheme not in {"https", "http"} or not u.hostname
            or u.username is not None or u.password is not None or u.query or u.fragment):
        raise ValueError("Use an HTTP(S) server base URL without credentials, query or fragment")
    if u.scheme == "http" and not allow_http:
        raise ValueError("HTTP exposes OAuth credentials in transit; explicitly pass --allow-http")
    port = u.port  # validate before accepting browser requests
    return u.scheme, u.hostname, port, u.path.rstrip("/") + "/v0/resource/plugins/commandcode/callback"


def forward_callback(endpoint, headers):
    scheme, host, port, path = endpoint
    cls = http.client.HTTPSConnection if scheme == "https" else http.client.HTTPConnection
    conn = cls(host, port, timeout=20)
    try:
        conn.request("GET", path, headers=headers)
        resp = conn.getresponse()
        resp.read(4096)
        # Never follow redirects carrying credential headers.
        return 200 <= resp.status < 300
    finally:
        conn.close()

class Bridge(http.server.BaseHTTPRequestHandler):
    server_version="CommandCodeLoopback/3"
    def log_message(self,fmt,*args): return
    def reply(self,code,text,headers=None):
        raw=("<!doctype html><meta charset=utf-8><title>Command Code</title><body><h2>"+text+"</h2></body>").encode()
        self.send_response(code)
        origin=self.headers.get("Origin")
        if origin in TRUSTED_ORIGINS:
            self.send_header("Access-Control-Allow-Origin",origin)
            self.send_header("Vary","Origin")
        for k,v in (headers or {}).items(): self.send_header(k,v)
        self.send_header("Content-Type","text/html; charset=utf-8");self.send_header("Cache-Control","no-store")
        self.send_header("Content-Length",str(len(raw)));self.end_headers();self.wfile.write(raw)
    def trusted_origin(self,content_type):
        origin=self.headers.get("Origin")
        if not origin or origin in TRUSTED_ORIGINS: return True
        return (origin=="null" and content_type=="application/x-www-form-urlencoded"
                and self.headers.get("Sec-Fetch-Site")=="cross-site"
                and self.headers.get("Sec-Fetch-Mode")=="navigate"
                and self.headers.get("Sec-Fetch-Dest")=="document")
    transaction_lock = threading.Lock()
    def deliver(self,payload):
        if set(payload)!=ALLOWED or any(not isinstance(payload[k],str) or not payload[k] for k in ALLOWED):
            return self.reply(400,"Callback rejected")
        with self.transaction_lock:
            if not self.server.bound or getattr(self.server,"consumed",False):
                return self.reply(409,"Start this login from CLIProxyAPI first")
            state,token=self.server.bound
            if not hmac.compare_digest(payload["state"],state): return self.reply(403,"Callback rejected")
            self.server.consumed=True
        forwarded=dict(payload);forwarded["bridgeToken"]=token
        # The host dispatches plugin resource routes as GET only, so the credential
        # travels in percent-encoded headers instead of a JSON body.
        headers={"X-CommandCode-Api-Key":urllib.parse.quote(forwarded["apiKey"],safe=""),
                 "X-CommandCode-State":urllib.parse.quote(forwarded["state"],safe=""),
                 "X-CommandCode-Bridge-Token":urllib.parse.quote(forwarded["bridgeToken"],safe="")}
        for field,header in (("userId","X-CommandCode-User-Id"),("userName","X-CommandCode-User-Name"),("keyName","X-CommandCode-Key-Name")):
            if forwarded.get(field): headers[header]=urllib.parse.quote(forwarded[field],safe="")
        ok=forward_callback(self.server.endpoint, headers)
        self.reply(200 if ok else 502,"Authorization saved" if ok else "Authorization rejected; start a new login")
        threading.Thread(target=self.server.shutdown,daemon=True).start()
    def do_OPTIONS(self):
        try:
            if urllib.parse.urlsplit(self.path).path!="/callback": return self.reply(404,"Not found")
            origin=self.headers.get("Origin")
            if origin not in TRUSTED_ORIGINS: return self.reply(403,"Origin rejected")
            self.send_response(204)
            self.send_header("Access-Control-Allow-Origin",origin)
            self.send_header("Vary","Origin")
            self.send_header("Access-Control-Allow-Methods","POST, OPTIONS")
            self.send_header("Access-Control-Allow-Headers","Content-Type")
            self.send_header("Access-Control-Allow-Private-Network","true")
            self.send_header("Cache-Control","no-store");self.send_header("Content-Length","0");self.end_headers()
        except Exception:
            self.reply(500,"Request rejected")
    def do_POST(self):
        try:
            u=urllib.parse.urlsplit(self.path)
            if u.path!="/callback" or u.query: return self.reply(404,"Not found")
            content_type=self.headers.get("Content-Type","").split(";",1)[0].strip().lower()
            if not self.trusted_origin(content_type): return self.reply(403,"Origin rejected")
            if content_type not in {"application/json","application/x-www-form-urlencoded"}:
                return self.reply(415,"Unsupported content type")
            try: length=int(self.headers.get("Content-Length", ""))
            except ValueError: return self.reply(411,"Content length required")
            if length<1 or length>MAX_BODY: return self.reply(413,"Payload rejected")
            raw=self.rfile.read(length)
            if len(raw)!=length: return self.reply(400,"Callback rejected")
            if content_type=="application/json":
                payload=json.loads(raw.decode("utf-8"))
                if not isinstance(payload,dict): return self.reply(400,"Callback rejected")
            else:
                q=urllib.parse.parse_qs(raw.decode("utf-8"),keep_blank_values=True,strict_parsing=True,max_num_fields=8)
                if any(len(v)!=1 for v in q.values()): return self.reply(400,"Callback rejected")
                payload={k:v[0] for k,v in q.items()}
            self.deliver(payload)
        except (UnicodeDecodeError,json.JSONDecodeError,ValueError):
            self.reply(400,"Callback rejected")
        except Exception:
            self.reply(502,"Secure bridge failed; start a new login")
    def do_GET(self):
        try:
            if len(self.path)>MAX_URL: return self.reply(414,"Request rejected")
            u=urllib.parse.urlsplit(self.path)
            q=urllib.parse.parse_qs(u.query,keep_blank_values=True,strict_parsing=True,max_num_fields=8)
            if u.path=="/start":
                if self.server.bound: return self.reply(409,"Login session already started")
                if set(q)!={"state","bridge_token"} or any(len(v)!=1 for v in q.values()): return self.reply(400,"Login request rejected")
                state,token=q["state"][0],q["bridge_token"][0]
                if not STATE_RE.fullmatch(state) or not STATE_RE.fullmatch(token): return self.reply(400,"Login request rejected")
                self.server.bound=(state,token)
                callback=f"http://127.0.0.1:{self.server.server_port}/callback"
                target=OFFICIAL+"?"+urllib.parse.urlencode({"callback":callback,"state":state,"mode":"redirect"})
                return self.reply(302,"Continue to CommandCode",{"Location":target})
            if u.path!="/callback": return self.reply(404,"Not found")
            if not self.server.bound: return self.reply(409,"Start this login from CLIProxyAPI first")
            if any(k not in ALLOWED or len(v)!=1 for k,v in q.items()): return self.reply(400,"Callback rejected")
            self.deliver({k:q[k][0] for k in ALLOWED if k in q})
        except Exception:
            self.reply(502,"Secure bridge failed; start a new login")


def main():
    ap=argparse.ArgumentParser(description="Local Command Code OAuth callback helper (Python standard library only)")
    ap.add_argument("--server", default=os.environ.get("COMMANDCODE_SERVER_URL"), help="Server base URL, optionally including reverse-proxy prefix")
    ap.add_argument("--allow-http", action="store_true", help="Explicitly accept plaintext transport of OAuth credentials")
    ap.add_argument("--port",type=int,default=5959)
    a=ap.parse_args()
    if not a.server: ap.error("--server or COMMANDCODE_SERVER_URL is required")
    if not 5959<=a.port<=5968: ap.error("port must be 5959..5968")
    try: endpoint=configure_endpoint(a.server,a.allow_http)
    except ValueError as exc: ap.error(str(exc))
    if endpoint[0]=="http": print("WARNING: HTTP sends OAuth credentials without encryption; use only a trusted network/tunnel.",flush=True)
    srv=http.server.ThreadingHTTPServer(("127.0.0.1",a.port),Bridge)
    srv.endpoint=endpoint;srv.bound=None
    print(json.dumps({"ready":True,"listen":f"127.0.0.1:{a.port}","transport":endpoint[0],"secrets_logged":False}),flush=True)
    try:srv.serve_forever()
    except KeyboardInterrupt: pass
    finally:srv.server_close()
if __name__=="__main__":main()
