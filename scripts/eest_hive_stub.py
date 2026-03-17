#!/usr/bin/env python3
"""Minimal Hive simulator stub for consume-engine collect-only runs."""

from __future__ import annotations

import argparse
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse


CLIENTS_PAYLOAD = [
    {
        "name": "n42-stub",
        "version": "dev",
        "meta": {
            "roles": ["eth1"],
        },
    }
]

HIVE_PAYLOAD = {
    "name": "n42-stub",
    "command": ["n42-hive-stub", "--collect-only"],
    "commit": "local",
    "date": "1970-01-01T00:00:00Z",
    "client_file": {"root": []},
}


class StubHandler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:  # noqa: N802
        path = urlparse(self.path).path
        if path == "/clients":
            self._write_json(CLIENTS_PAYLOAD)
            return
        if path == "/hive":
            self._write_json(HIVE_PAYLOAD)
            return
        if path in {"/", "/healthz", "/livez"}:
            self._write_json({"status": "ok"})
            return
        self.send_error(404, "not found")

    def log_message(self, format: str, *args: object) -> None:
        return

    def _write_json(self, payload: object) -> None:
        body = json.dumps(payload).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Serve a minimal Hive API for consume-engine --collect-only.",
    )
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=3000)
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    server = ThreadingHTTPServer((args.host, args.port), StubHandler)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
