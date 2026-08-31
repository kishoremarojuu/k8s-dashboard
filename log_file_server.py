#!/usr/bin/env python3
"""
Local helper for tools/k8s-dashboard.html — tails files under a pod's logs/
directory (jmet-signer.log, audit.log, enclave-signer.log) that `kubectl logs`
can't see because they're written to disk, not stdout.

Runs alongside `kubectl proxy`; the dashboard polls this server the same way
it polls the proxy for stdout logs. Shells out to `kubectl exec ... tail` per
request rather than holding persistent connections open — matches the
dashboard's simple poll-every-3s model.

Usage:
    python3 log_file_server.py [--port PORT]

Environment variables:
    LOG_FILE_SERVER_PORT   - port to listen on (default: 8002)
"""

import argparse
import json
import os
import subprocess
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse, parse_qs

ALLOWED_FILES = {"jmet-signer.log", "audit.log", "enclave-signer.log"}
KUBECTL_TIMEOUT_SECONDS = 15


class TailHandler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        sys.stderr.write(f"[log_file_server] {fmt % args}\n")

    def _send(self, status, body, content_type="text/plain; charset=utf-8"):
        encoded = body.encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(encoded)))
        # Local-only helper serving a file:// or localhost dashboard — CORS
        # wide open is fine, nothing here is reachable off this machine.
        self.send_header("Access-Control-Allow-Origin", "*")
        self.end_headers()
        self.wfile.write(encoded)

    def do_OPTIONS(self):
        self.send_response(204)
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Methods", "GET, OPTIONS")
        self.end_headers()

    def do_GET(self):
        parsed = urlparse(self.path)
        if parsed.path != "/tail":
            self._send(404, "not found")
            return

        params = parse_qs(parsed.query)

        def one(name):
            values = params.get(name)
            return values[0] if values else None

        namespace = one("namespace")
        pod = one("pod")
        container = one("container")
        filename = one("file")
        lines = one("lines") or "200"

        if not all([namespace, pod, container, filename]):
            self._send(400, "missing required query params: namespace, pod, container, file")
            return

        if filename not in ALLOWED_FILES:
            self._send(400, f"file must be one of: {', '.join(sorted(ALLOWED_FILES))}")
            return

        try:
            lines_int = max(1, min(int(lines), 5000))
        except ValueError:
            self._send(400, "lines must be an integer")
            return

        cmd = [
            "kubectl", "exec",
            "-n", namespace,
            pod,
            "-c", container,
            "--",
            "tail", "-n", str(lines_int), f"logs/{filename}",
        ]

        try:
            result = subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                timeout=KUBECTL_TIMEOUT_SECONDS,
            )
        except subprocess.TimeoutExpired:
            self._send(504, f"kubectl exec timed out after {KUBECTL_TIMEOUT_SECONDS}s")
            return
        except FileNotFoundError:
            self._send(500, "kubectl not found on PATH")
            return

        if result.returncode != 0:
            self._send(502, result.stderr or f"kubectl exec exited {result.returncode}")
            return

        self._send(200, result.stdout)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--port",
        type=int,
        default=int(os.environ.get("LOG_FILE_SERVER_PORT", "8002")),
        help="Port to listen on (default: 8002)",
    )
    args = parser.parse_args()

    server = ThreadingHTTPServer(("127.0.0.1", args.port), TailHandler)
    print(f"[log_file_server] Listening on http://127.0.0.1:{args.port} (allowed files: {', '.join(sorted(ALLOWED_FILES))})", file=sys.stderr)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\n[log_file_server] Shutting down.", file=sys.stderr)


if __name__ == "__main__":
    main()
