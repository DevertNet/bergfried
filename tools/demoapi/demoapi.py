#!/usr/bin/env python3
"""Canned-data stand-in for the daemon's app API (HTTP/JSON over a unix socket).

Lets you look at (and take screenshots of) the app without any VM or real data:

    python3 tools/demoapi/demoapi.py /private/tmp/bft-demo          # creates /private/tmp/bft-demo/app.sock
    BERGFRIED_HOME=/private/tmp/bft-demo app/build/Bergfried.app/Contents/MacOS/Bergfried

All names, hosts and files are made up. Read-only: every write returns 200 with an empty object.
"""
import json, os, socketserver, sys, time
from http.server import BaseHTTPRequestHandler

GB = 1 << 30
now = int(time.time())
KEY = "SHA256:Qm9yZGVhdXhEZW1vS2V5RmluZ2VycHJpbnQwMDAwMDA"
FP = "SHA256:ZGVtbyBob3N0IGtleSBmaW5nZXJwcmludCAwMDAwMDAwMA"


def worktree(name, running=True, rc=True, window=True, action=""):
    return {"name": name, "window": window, "running": running, "rc": rc,
            "rc_url": "https://claude.ai/code/session_demo" if rc else "", "rc_action": action}


def sandbox(name, status, worktrees, grants=0, pending=0, token=True, logged_in=True, oc=False):
    return {"name": name, "vm": "bergfried-" + name, "status": status, "repo": "", "template_version": 3,
            "created_unix": now - 86400 * 3, "cpus": 4, "memory": 6 * GB, "disk": 60 * GB, "worktrees": worktrees,
            "active_grants": grants, "pending_requests": pending, "claude_logged_in": logged_in if status == "Running" else None,
            "opencode_logged_in": logged_in if status == "Running" else None,
            "claude_installed": status == "Running", "opencode_installed": status == "Running",
            "opencode_running": oc, "opencode_port": 42123,
            "host_disk_used": int(7.4 * GB), "guest_disk_used": int(5.1 * GB), "guest_disk_size": 58 * GB,
            "has_github_token": token}


SANDBOXES = [
    sandbox("webshop", "Running", [worktree("main"), worktree("checkout"), worktree("docs", running=False, rc=False, window=False)], grants=1, pending=2, oc=True),
    sandbox("blog", "Running", [worktree("main")]),
    sandbox("data-pipeline", "Stopped", [worktree("main", running=False, rc=False, window=False)], token=False),
]


def req(i, sb, wt, kind, path, reason, **kw):
    r = {"id": i, "sandbox": sb, "worktree": wt, "kind": kind, "path": path, "reason": reason, "status": "pending",
         "created_unix": now - 40 * i, "bytes": 0, "files": 0, "large": False}
    r.update(kw)
    return r


REQUESTS = [
    req(3, "webshop", "main", "file", "Documents/Invoices/2024-Q3.pdf", "I need the invoice layout to build the PDF export.", bytes=182_000, files=1),
    req(2, "webshop", "checkout", "ssh", "staging.example.com", "Deploy the new checkout build to the staging server.",
        ssh={"host_key_fingerprints": [FP], "source": "known_hosts", "default_key": KEY, "default_user": "deploy",
             "requested_user": "deploy", "suggested_minutes": 30}),
    req(1, "webshop", "docs", "ghtoken", "acme/shop-docs", "git clone failed with 'Repository not found'.",
        github={"repo": "acme/shop-docs", "has_token": True, "link": "https://github.com/settings/personal-access-tokens/new", "hint": ""}),
]
GRANTS = [{"id": 1, "sandbox": "webshop", "worktree": "main", "host": "build.example.com", "key_fingerprint": KEY, "user": "deploy",
           "expires_unix": now + 22 * 60, "per_connection": False, "revoked": False, "active": True, "host_key_fingerprints": [FP]}]
AUDIT = [{"ts_unix": now - 60 * i, "actor": a, "event": e, "sandbox": "webshop", "worktree": g, "detail": d} for i, (a, e, g, d) in enumerate([
    ("worktree", "request.created", "main", "id=3 file Documents/Invoices/2024-Q3.pdf"),
    ("app", "ssh.grant_created", "main", "grant=1 host=build.example.com minutes=30"),
    ("broker", "ssh.sign", "main", "grant=1 host=build.example.com"),
    ("daemon", "worktree.rc_enable", "docs", "/remote-control")])]

ROUTES = {
    "/v1/status": {"version": "1.0.0", "base_ready": True, "base_name": "bergfried-base-demo", "template_version": 3,
                   "index_ready": True, "upstream_agent": True, "sandboxes": len(SANDBOXES)},
    "/v1/sandboxes": SANDBOXES, "/v1/requests": REQUESTS, "/v1/ssh/grants": GRANTS, "/v1/audit": AUDIT,
    "/v1/blocklist": {"paths": [".ssh", "Library", ".config", ".gnupg"], "names": [".env", ".env.*", "id_rsa"]},
    "/v1/update": {"current": "1.0.0", "latest": "1.0.0", "available": False, "notes": "", "url": "", "checked_unix": now, "updatable": True},
}


class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def _send(self, obj, code=200):
        b = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(b)

    def do_GET(self):
        self._send(ROUTES.get(self.path.split("?")[0], {}))

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        self.rfile.read(n)
        self._send({})

    do_PUT = do_DELETE = do_POST


class S(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True

    def get_request(self):
        c, _ = super().get_request()
        return c, ("local", 0)


if __name__ == "__main__":
    home = sys.argv[1] if len(sys.argv) > 1 else "/private/tmp/bft-demo"
    os.makedirs(home, mode=0o700, exist_ok=True)
    sock = os.path.join(home, "app.sock")
    if os.path.exists(sock):
        os.remove(sock)
    srv = S(sock, H)
    os.chmod(sock, 0o600)
    print("demo API on", sock, flush=True)
    srv.serve_forever()
