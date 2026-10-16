#!/usr/bin/env python3
"""Black-box API smoke test for a real local Loom demo mesh."""

from __future__ import annotations

import json
import os
import re
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
STARTUP_TIMEOUT = 60.0


class SmokeFailure(RuntimeError):
	pass


def reserve_port() -> int:
	with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
		sock.bind(("127.0.0.1", 0))
		return int(sock.getsockname()[1])


def request(base_url: str, path: str, method: str = "GET", body: Any = None) -> tuple[int, Any, bytes]:
	data = None if body is None else json.dumps(body).encode("utf-8")
	headers = {"Accept": "application/json"}
	if data is not None:
		headers["Content-Type"] = "application/json"
	req = urllib.request.Request(base_url + path, data=data, headers=headers, method=method)
	try:
		with urllib.request.urlopen(req, timeout=8) as response:
			raw = response.read()
			status = response.status
	except urllib.error.HTTPError as error:
		raw = error.read()
		status = error.code
	try:
		payload = json.loads(raw.decode("utf-8")) if raw else None
	except (UnicodeDecodeError, json.JSONDecodeError):
		payload = None
	return status, payload, raw


def require(condition: bool, message: str) -> None:
	if not condition:
		raise SmokeFailure(message)


def api_json(base_url: str, path: str, method: str = "GET", body: Any = None) -> Any:
	status, payload, raw = request(base_url, path, method, body)
	require(200 <= status < 300, f"{method} {path} returned HTTP {status}: {raw[:300]!r}")
	require(payload is not None, f"{method} {path} did not return JSON")
	return payload


def require_api_error(base_url: str, path: str, method: str, body: Any = None) -> None:
	status, payload, _ = request(base_url, path, method, body)
	require(400 <= status < 500, f"{method} {path} should return a client error, got HTTP {status}")
	require(isinstance(payload, dict) and isinstance(payload.get("error"), str) and payload["error"].strip(),
		f"{method} {path} should return a human-readable JSON error")


def wait_until_ready(base_url: str, process: subprocess.Popen[bytes], deadline: float) -> None:
	while time.monotonic() < deadline:
		if process.poll() is not None:
			raise SmokeFailure(f"backend exited during startup with status {process.returncode}")
		try:
			status, payload, _ = request(base_url, "/api/health")
			if status == 200 and isinstance(payload, dict) and payload.get("ok") is True:
				return
		except (OSError, urllib.error.URLError):
			pass
		time.sleep(0.25)
	raise SmokeFailure(f"backend did not become ready within {STARTUP_TIMEOUT:.0f} seconds")


def find_folder(state: dict[str, Any], folder_id: str) -> dict[str, Any]:
	for folder in state.get("folders", []):
		if folder.get("id") == folder_id:
			return folder
	raise SmokeFailure(f"folder {folder_id!r} was not present in /api/state")


def snapshot_tree(root: Path) -> dict[str, bytes]:
	if not root.exists():
		return {}
	return {
		str(path.relative_to(root)): path.read_bytes()
		for path in root.rglob("*")
		if path.is_file() and not path.is_symlink()
	}


def snapshot_received(data_dir: Path) -> dict[str, bytes]:
	files: dict[str, bytes] = {}
	for root in (data_dir / "nodes" / "linux" / "received", data_dir / "received"):
		for name, contents in snapshot_tree(root).items():
			files[f"{root.relative_to(data_dir)}/{name}"] = contents
	return files


def sensitive_diagnostic_field_names(value: Any) -> list[str]:
	suspicious = ("networkkey", "privatekey", "secretkey", "keymaterial", "presharedkey", "privkey")
	found: list[str] = []
	if isinstance(value, dict):
		for key, child in value.items():
			normalized = re.sub(r"[^a-z0-9]", "", str(key).lower())
			if any(term in normalized for term in suspicious):
				found.append(str(key))
			found.extend(sensitive_diagnostic_field_names(child))
	elif isinstance(value, list):
		for child in value:
			found.extend(sensitive_diagnostic_field_names(child))
	return found


def run_checks(base_url: str, scratch: Path, data_dir: Path) -> None:
	status, health, _ = request(base_url, "/api/health")
	require(status == 200 and health == {"ok": True}, "/api/health must return {\"ok\": true}")
	print("PASS health endpoint")

	state_before = api_json(base_url, "/api/state")
	require(state_before.get("network", {}).get("demo") is True, "demo state must identify itself as demo")
	devices = state_before.get("devices", [])
	require(len(devices) >= 3, f"expected the three demo peers, found {len(devices)}")
	direct_devices = [device for device in devices if device.get("connection") == "direct"]
	relay_devices = [device for device in devices if device.get("connection") == "relay"]
	require(direct_devices, "demo state must show at least one direct peer link")
	require(relay_devices, "demo state must show at least one circuit-relayed peer link")
	linux = next((device for device in devices if "linux" in device.get("name", "").lower()), relay_devices[0])
	require(linux.get("id"), "could not identify the relayed Linux demo peer")
	print("PASS state reports direct and circuit-relayed peers")

	services = state_before.get("services", [])
	linux_service = next((service for service in services if service.get("deviceId") == linux.get("id")), None)
	require(linux_service is not None, "the relayed Linux demo peer must publish a sample HTTP service")
	service_url = linux_service.get("url", "")
	parsed = urllib.parse.urlsplit(service_url)
	proxy_path = parsed.path if parsed.scheme else service_url
	require(proxy_path.startswith("/proxy/"), f"service URL is not a proxy route: {service_url!r}")
	proxy_status, _, proxy_body = request(base_url, proxy_path)
	require(proxy_status == 200, f"relayed service proxy returned HTTP {proxy_status}")
	require(len(proxy_body.strip()) > 0, "relayed service proxy returned an empty body")
	print("PASS real sample HTTP response over circuit relay")

	require_api_error(base_url, "/api/peers", "POST", {"address": "not-a-multiaddr"})
	require_api_error(base_url, f"/proxy/{linux['id']}/missing-service/", "GET")
	print("PASS invalid peer and service requests return JSON errors")

	source = scratch / "relay-folder-source"
	source.mkdir()
	initial_bytes = ("Loom real relay folder transfer smoke test, version one.\n" * 512).encode("utf-8")
	final_bytes = ("Loom real relay folder transfer smoke test, version two.\n" * 512).encode("utf-8")
	(source / "relay-check.txt").write_bytes(initial_bytes)
	created = api_json(base_url, "/api/folders", "POST", {
		"path": str(source),
		"peerId": linux["id"],
		"name": "Smoke relay folder",
	})
	folder_id = created.get("id") if isinstance(created, dict) else None
	if not folder_id:
		registered_state = api_json(base_url, "/api/state")
		registered = next((folder for folder in registered_state.get("folders", []) if folder.get("name") == "Smoke relay folder"), None)
		require(registered is not None, "registered folder was not returned by /api/state")
		folder_id = registered.get("id")
	require(isinstance(folder_id, str) and folder_id, "folder registration did not return an id")

	folder_root = data_dir / "received" / folder_id
	api_json(base_url, f"/api/folders/{urllib.parse.quote(folder_id, safe='')}/pause", "POST", {"paused": True})
	paused = find_folder(api_json(base_url, "/api/state"), folder_id)
	require(paused.get("paused") is True, "folder pause state did not update")
	paused_snapshot = snapshot_tree(folder_root)
	(source / "relay-check.txt").write_bytes(final_bytes)
	require_api_error(base_url, f"/api/folders/{urllib.parse.quote(folder_id, safe='')}/sync", "POST")
	require(snapshot_tree(folder_root) == paused_snapshot, "paused sync changed files at the receiver")
	api_json(base_url, f"/api/folders/{urllib.parse.quote(folder_id, safe='')}/pause", "POST", {"paused": False})
	before_sync = api_json(base_url, "/api/state")
	bytes_before = before_sync.get("stats", {}).get("transferredBytes", 0)
	api_json(base_url, f"/api/folders/{urllib.parse.quote(folder_id, safe='')}/sync", "POST")
	after_sync = api_json(base_url, "/api/state")
	synced_folder = find_folder(after_sync, folder_id)
	require(synced_folder.get("paused") is False, "folder should be resumed before sync")
	require(synced_folder.get("files", 0) >= 1, "synced folder should report at least one file")
	require(synced_folder.get("bytes", 0) >= len(final_bytes), "synced folder byte count should include transferred content")
	require(after_sync.get("stats", {}).get("transferredBytes", 0) - bytes_before >= len(final_bytes),
		"transfer stats did not include the relayed folder bytes")
	received_file = folder_root / "relay-check.txt"
	require(received_file.is_file(), f"relay transfer did not create {received_file}")
	require(received_file.read_bytes() == final_bytes, "received relay file bytes did not match the source")
	print("PASS folder pause/resume and byte-for-byte file transfer over circuit relay")

	(source / "relay-check.txt").write_bytes(b"content staged while the Loom network is paused\n")
	network_pause = api_json(base_url, "/api/network", "POST", {"connected": False})
	require(network_pause.get("connected") is False or network_pause.get("ok") is True,
		"network disconnect did not report success")
	before_disconnect_sync = snapshot_tree(folder_root)
	require_api_error(base_url, proxy_path, "GET")
	require_api_error(base_url, f"/api/folders/{urllib.parse.quote(folder_id, safe='')}/sync", "POST")
	require(snapshot_tree(folder_root) == before_disconnect_sync,
		"service or sync request changed receiver files while the network was paused")
	api_json(base_url, "/api/network", "POST", {"connected": True})
	status, _, proxy_body = request(base_url, proxy_path)
	require(status == 200 and proxy_body.strip(), "service did not recover after reconnect")
	api_json(base_url, f"/api/folders/{urllib.parse.quote(folder_id, safe='')}/sync", "POST")
	require((folder_root / "relay-check.txt").read_bytes() == b"content staged while the Loom network is paused\n",
		"sync did not recover after reconnect")
	print("PASS network disconnect blocks proxy and sync, then reconnect restores both")

	before_change = api_json(base_url, "/api/state")
	bytes_before_change = before_change.get("stats", {}).get("transferredBytes", 0)
	synced_before = before_change.get("stats", {}).get("syncedFiles", 0)
	activity_ids_before = {entry.get("id") for entry in before_change.get("activity", [])}
	received_before_change = snapshot_received(data_dir)
	change = api_json(base_url, "/api/demo/change", "POST", {})
	require(change.get("ok") is True, "/api/demo/change must report successful completion")
	after_change = api_json(base_url, "/api/state")
	require(after_change.get("stats", {}).get("transferredBytes", 0) > bytes_before_change,
		"demo change did not increase transferred byte count")
	require(after_change.get("stats", {}).get("syncedFiles", 0) > synced_before,
		"demo change did not increase the synced file count")
	received_after_change = snapshot_received(data_dir)
	require(any(received_after_change.get(name) != contents for name, contents in received_before_change.items())
		or any(name not in received_before_change for name in received_after_change),
		"demo change did not add or update a file under the Linux receiver root")
	new_sync_activity = [
		entry for entry in after_change.get("activity", [])
		if entry.get("id") not in activity_ids_before and entry.get("type") == "sync"
	]
	require(new_sync_activity, "demo change did not add a sync activity entry")
	print("PASS demo change records a real transfer and sync activity")

	diagnostics = api_json(base_url, "/api/diagnostics")
	leaked_fields = sensitive_diagnostic_field_names(diagnostics)
	require(not leaked_fields, f"diagnostics exposed sensitive key field names: {leaked_fields}")
	print("PASS diagnostics omit private and network key fields")


def main() -> int:
	try:
		with tempfile.TemporaryDirectory(prefix="loom-smoke-") as tmp:
			scratch = Path(tmp)
			binary = scratch / "loom"
			build = subprocess.run(
				["go", "build", "-o", str(binary), "./cmd/loom"],
				cwd=ROOT,
				check=False,
				text=True,
				capture_output=True,
			)
			if build.returncode != 0:
				sys.stderr.write(build.stderr)
				sys.stderr.write(build.stdout)
				return build.returncode or 1

			port = reserve_port()
			base_url = f"http://127.0.0.1:{port}"
			data_dir = scratch / "demo-data"
			data_dir.mkdir()
			log_path = scratch / "backend.log"
			with log_path.open("w+b") as log_file:
				process = subprocess.Popen(
					[str(binary), "--demo", "--listen", f"127.0.0.1:{port}", "--data-dir", str(data_dir)],
					cwd=ROOT,
					stdout=log_file,
					stderr=subprocess.STDOUT,
				)
				try:
					wait_until_ready(base_url, process, time.monotonic() + STARTUP_TIMEOUT)
					run_checks(base_url, scratch, data_dir)
				except Exception:
					# Keep failure output useful while masking any accidental 64-hex secret in logs.
					if process.poll() is not None:
						log_file.flush()
						log_file.seek(0)
						log_text = log_file.read().decode("utf-8", errors="replace")
						log_text = re.sub(r"(?i)\b[0-9a-f]{64}\b", "[redacted-64-hex]", log_text)
						if log_text.strip():
							print("Backend output (64-hex strings redacted):", file=sys.stderr)
							print(log_text[-4000:], file=sys.stderr)
					raise
				finally:
					if process.poll() is None:
						process.terminate()
						try:
							process.wait(timeout=5)
						except subprocess.TimeoutExpired:
							process.kill()
							process.wait(timeout=5)
	except FileNotFoundError as error:
		print(f"Missing required command: {error.filename}", file=sys.stderr)
		return 2
	except SmokeFailure as error:
		print(f"FAIL {error}", file=sys.stderr)
		return 1
		print("Loom black-box smoke test passed.")
		return 0
	except Exception as error:
		print(f"FAIL {error}", file=sys.stderr)
		return 1


if __name__ == "__main__":
	raise SystemExit(main())
