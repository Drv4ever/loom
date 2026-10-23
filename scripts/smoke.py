#!/usr/bin/env python3
"""Black-box API checks against one real Loom daemon, with isolated test state."""
from __future__ import annotations

import argparse
import http.server
import json
import os
import secrets
import sys
import tempfile
import threading
import urllib.error
import urllib.request
from pathlib import Path

from peer_smoke import (
    ROOT, FixtureHandler, LoomProcess, SmokeFailure, api_json,
    build_binary, request, require, reserve_port, wait_for,
)


def run_checks(binary: Path, scratch: Path) -> None:
    key = secrets.token_hex(32)
    daemon = LoomProcess(binary, "API smoke device", reserve_port(), scratch / "device", scratch / "device.log", key)
    fixture = http.server.ThreadingHTTPServer(("127.0.0.1", 0), FixtureHandler)
    worker = threading.Thread(target=fixture.serve_forever, daemon=True)
    worker.start()
    try:
        daemon.start()
        daemon.wait_ready()
        state = api_json(daemon.base_url, "/api/state")
        require(len(state["devices"]) == 1 and state["devices"][0]["self"], "daemon must start exactly one real device")
        require(not state["services"] and not state["folders"], "new device must not contain demo fixtures")
        peer_id = state["network"]["peerId"]
        for path in ("/api/pairing/generate", "/api/pairing/claim"):
            status, payload, _ = request(daemon.base_url, path, "POST", {})
            require(status == 501 and "unavailable" in payload["error"], "incomplete pairing must not report success")
        for body in ({"name": "bad", "target": "http://example.com"},
                     {"name": "bad TCP", "protocol": "tcp", "target": "8.8.8.8:53"}):
            status, _, _ = request(daemon.base_url, "/api/services", "POST", body)
            require(status == 400, "external service target was accepted")
        service = api_json(daemon.base_url, "/api/services", "POST", {
            "name": "Local fixture", "target": f"http://127.0.0.1:{fixture.server_port}"})
        require(service["status"] == "available", "healthy fixture was not available")
        status, _, body = request(daemon.base_url, service["url"] + "fixture")
        require(status == 200 and body == FixtureHandler.body, "local approved service proxy failed")
        status, _, _ = request(daemon.base_url, "/api/forwards", "POST", {
            "peerId": peer_id, "target": f"127.0.0.1:{fixture.server_port}"})
        require(status == 400, "raw TCP target was accepted")
        diagnostic = api_json(daemon.base_url, "/api/diagnostics")
        require(key not in json.dumps(diagnostic), "diagnostics leaked network key")
        require(key not in json.dumps(state), "state leaked network key")
        for header, value, expected in (("Origin", "https://example.com", 403), ("Host", "example.com", 421)):
            req = urllib.request.Request(daemon.base_url + "/api/network", data=b'{"connected": false}',
                                         headers={"Content-Type": "application/json", header: value}, method="POST")
            try:
                with urllib.request.urlopen(req, timeout=5) as response:
                    status = response.status
            except urllib.error.HTTPError as error:
                status = error.code
            require(status == expected, f"{header} restriction failed")
        api_json(daemon.base_url, "/api/network", "POST", {"connected": False})
        daemon.stop()
        daemon.start()
        daemon.wait_ready()
        state = api_json(daemon.base_url, "/api/state")
        require(state["network"]["peerId"] == peer_id, "identity changed after restart")
        require(not state["network"]["connected"], "paused network did not persist")
        require(len(state["services"]) == 1, "publication did not persist")
        api_json(daemon.base_url, "/api/network", "POST", {"connected": True})
        fixture.shutdown()
        fixture.server_close()
        wait_for("unavailable app status", lambda: api_json(daemon.base_url, "/api/state")["services"][0]["status"] == "app_unavailable", 10)
        api_json(daemon.base_url, f"/api/services/{service['id']}", "DELETE")
        print("PASS real device startup, service proxy/health, input boundaries, disabled PIN/raw TCP, and persistence")
    except Exception:
        print(daemon.diagnostics(), file=sys.stderr)
        raise
    finally:
        daemon.stop()
        fixture.shutdown()
        fixture.server_close()
        worker.join(timeout=2)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary")
    args = parser.parse_args()
    try:
        with tempfile.TemporaryDirectory(prefix="loom-api-smoke-") as temp:
            scratch = Path(temp)
            binary = (ROOT / args.binary).resolve() if args.binary else scratch / "loom"
            if not args.binary and build_binary(binary):
                return 1
            require(binary.is_file() and os.access(binary, os.X_OK), "Loom executable was not found")
            run_checks(binary, scratch)
    except (SmokeFailure, OSError) as error:
        print(f"FAIL {error}", file=sys.stderr)
        return 1
    print("Loom single-device API smoke test passed.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
