#!/usr/bin/env python3
"""Exercise two independent normal-mode Loom nodes over the local network."""

from __future__ import annotations

import argparse
import hashlib
import http.server
import json
import os
import secrets
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any, Callable


ROOT = Path(__file__).resolve().parents[1]
STARTUP_TIMEOUT = 60.0
DISCOVERY_TIMEOUT = 25.0


class SmokeFailure(RuntimeError):
	pass


def reserve_port() -> int:
	with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
		sock.bind(("127.0.0.1", 0))
		return int(sock.getsockname()[1])


def request(base_url: str, path: str, method: str = "GET", body: Any = None,
		 timeout: float = 6.0) -> tuple[int, Any, bytes]:
	data = None if body is None else json.dumps(body).encode("utf-8")
	headers = {"Accept": "application/json"}
	if data is not None:
		headers["Content-Type"] = "application/json"
	req = urllib.request.Request(base_url + path, data=data, headers=headers, method=method)
	try:
		with urllib.request.urlopen(req, timeout=timeout) as response:
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
	require(200 <= status < 300, f"{method} {path} returned HTTP {status}: {raw[:250]!r}")
	require(payload is not None, f"{method} {path} did not return JSON")
	return payload


def wait_for(label: str, predicate: Callable[[], bool], timeout: float) -> None:
	deadline = time.monotonic() + timeout
	while time.monotonic() < deadline:
		try:
			if predicate():
				return
		except (OSError, urllib.error.URLError, TimeoutError, json.JSONDecodeError):
			pass
		time.sleep(0.25)
	raise SmokeFailure(f"timed out waiting for {label} after {timeout:.0f} seconds")


def get_state(base_url: str) -> dict[str, Any]:
	status, payload, _ = request(base_url, "/api/state")
	if status != 200 or not isinstance(payload, dict):
		return {}
	return payload


def is_peer_online(state: dict[str, Any], peer_id: str) -> bool:
	return any(device.get("id") == peer_id and device.get("online") is True
		for device in state.get("devices", []))


def wait_ready(base_url: str, process: subprocess.Popen[bytes], timeout: float) -> None:
	deadline = time.monotonic() + timeout
	while time.monotonic() < deadline:
		if process.poll() is not None:
			raise SmokeFailure(f"backend exited during startup with status {process.returncode}")
		try:
			status, payload, _ = request(base_url, "/api/health", timeout=1.5)
			if status == 200 and isinstance(payload, dict) and payload.get("ok") is True:
				return
		except (OSError, urllib.error.URLError, TimeoutError):
			pass
		time.sleep(0.25)
	raise SmokeFailure(f"backend did not become ready within {timeout:.0f} seconds")


def wait_peer(base_url: str, peer_id: str, online: bool, timeout: float = DISCOVERY_TIMEOUT) -> None:
	description = f"peer {peer_id} {'online' if online else 'offline'}"
	wait_for(description, lambda: is_peer_online(get_state(base_url), peer_id) == online, timeout)


def manual_join(base_url: str, peer_url: str, peer_id: str) -> None:
	state = api_json(peer_url, "/api/state")
	addresses = state.get("network", {}).get("addresses", [])
	addresses = [address for address in addresses if isinstance(address, str) and address.endswith("/p2p/" + peer_id)]
	addresses.sort(key=lambda address: ("/ip4/127.0.0.1/" not in address, address))
	last_error = "no usable peer multiaddress was advertised"
	for address in addresses:
		status, payload, raw = request(base_url, "/api/peers", "POST", {"address": address}, timeout=15.0)
		if 200 <= status < 300:
			try:
				wait_peer(base_url, peer_id, True, timeout=8.0)
				return
			except SmokeFailure:
				pass
		last_error = f"manual multiaddress join returned HTTP {status}: {raw[:200]!r}"
	raise SmokeFailure(f"mDNS discovery was unavailable and manual join failed: {last_error}")


def discover_peer(base_url: str, peer_url: str, peer_id: str, stage: str) -> str:
	try:
		wait_peer(base_url, peer_id, True, DISCOVERY_TIMEOUT)
		print(f"PASS {stage}: peer discovered through mDNS")
		return "mdns"
	except SmokeFailure:
		manual_join(base_url, peer_url, peer_id)
		print(f"INFO {stage}: mDNS was not observed; used the peer's advertised multiaddress fallback")
		return "manual"


def redact(text: str, network_key: str) -> str:
	return text.replace(network_key, "[redacted-network-key]")


class LoomProcess:
	def __init__(self, binary: Path, name: str, port: int, data_dir: Path, log_path: Path, network_key: str):
		self.binary = binary
		self.name = name
		self.port = port
		self.data_dir = data_dir
		self.log_path = log_path
		self.network_key = network_key
		self.peer_port = reserve_port()
		self.process: subprocess.Popen[bytes] | None = None

	@property
	def base_url(self) -> str:
		return f"http://127.0.0.1:{self.port}"

	def start(self) -> None:
		self.data_dir.mkdir(parents=True, exist_ok=True)
		key_file = self.data_dir / "network.key"
		if not key_file.exists():
			key_file.write_text(self.network_key + "\n", encoding="ascii")
			os.chmod(key_file, 0o600)
		log_file = self.log_path.open("ab")
		try:
			self.process = subprocess.Popen(
				[str(self.binary), "--name", self.name, "--listen", f"127.0.0.1:{self.port}",
				 "--peer-listen", f"/ip4/0.0.0.0/tcp/{self.peer_port}",
				 "--network-key-file", str(key_file), "--data-dir", str(self.data_dir)],
				cwd=ROOT,
				stdout=log_file,
				stderr=subprocess.STDOUT,
			)
		finally:
			log_file.close()

	def wait_ready(self, timeout: float = STARTUP_TIMEOUT) -> None:
		require(self.process is not None, f"{self.name} was not started")
		wait_ready(self.base_url, self.process, timeout)

	def stop(self) -> None:
		process = self.process
		if process is None or process.poll() is not None:
			return
		process.terminate()
		try:
			process.wait(timeout=8.0)
		except subprocess.TimeoutExpired:
			process.kill()
			process.wait(timeout=5.0)

	def diagnostics(self) -> str:
		try:
			text = self.log_path.read_text(encoding="utf-8", errors="replace")[-3000:]
		except OSError:
			return ""
		return redact(text, self.network_key)


class FixtureHandler(http.server.BaseHTTPRequestHandler):
	body = b"Loom normal-mode service fixture over the private mesh.\n"

	def do_GET(self) -> None:
		if self.path != "/fixture":
			self.send_error(404)
			return
		self.send_response(200)
		self.send_header("Content-Type", "text/plain; charset=utf-8")
		self.send_header("Content-Length", str(len(self.body)))
		self.end_headers()
		self.wfile.write(self.body)

	def log_message(self, _format: str, *_args: Any) -> None:
		return


def find_service(state: dict[str, Any], peer_id: str, service_id: str | None = None) -> dict[str, Any] | None:
	for service in state.get("services", []):
		if service.get("deviceId") == peer_id and (service_id is None or service.get("id") == service_id):
			return service
	return None


def get_proxy(base_url: str, proxy_url: str, expected: bytes) -> None:
	parsed = urllib.parse.urlsplit(proxy_url)
	path = parsed.path if parsed.scheme else proxy_url
	path = path.rstrip("/") + "/fixture"
	status, _, body = request(base_url, path)
	require(status == 200, f"mesh service proxy returned HTTP {status}: {body[:200]!r}")
	require(body == expected, "mesh service proxy body did not match the local HTTP fixture")


def wait_file(path: Path, expected: bytes, timeout: float = 20.0) -> None:
	def matches() -> bool:
		try:
			return path.is_file() and path.read_bytes() == expected
		except OSError:
			return False
	wait_for(f"exact bytes at receiver path {path.name}", matches, timeout)


def sync_folder(base_url: str, folder_id: str) -> None:
	path = f"/api/folders/{urllib.parse.quote(folder_id, safe='')}/sync"
	deadline = time.monotonic() + 25.0
	while time.monotonic() < deadline:
		status, payload, raw = request(base_url, path, "POST", {})
		if 200 <= status < 300:
			return
		if status == 409 and isinstance(payload, dict) and "already running" in payload.get("error", "").lower():
			time.sleep(0.2)
			continue
		raise SmokeFailure(f"folder sync returned HTTP {status}: {raw[:250]!r}")
	raise SmokeFailure("folder sync remained busy for 25 seconds")


def run_checks(binary: Path, scratch: Path, network_key: str) -> None:
	api_a_port = reserve_port()
	api_b_port = reserve_port()
	peer_a = LoomProcess(binary, "Peer A", api_a_port, scratch / "peer-a", scratch / "peer-a.log", network_key)
	peer_b = LoomProcess(binary, "Peer B", api_b_port, scratch / "peer-b", scratch / "peer-b.log", network_key)
	server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), FixtureHandler)
	fixture_thread = threading.Thread(target=server.serve_forever, name="loom-fixture", daemon=True)
	fixture_thread.start()
	try:
		peer_a.start()
		peer_b.start()
		peer_a.wait_ready()
		peer_b.wait_ready()
		state_a = api_json(peer_a.base_url, "/api/state")
		state_b = api_json(peer_b.base_url, "/api/state")
		peer_a_id = state_a.get("network", {}).get("peerId", "")
		peer_b_id = state_b.get("network", {}).get("peerId", "")
		require(peer_a_id and peer_b_id and peer_a_id != peer_b_id, "normal nodes must have distinct persistent peer identities")
		require(state_a.get("network", {}).get("demo") is not True and state_b.get("network", {}).get("demo") is not True,
			"peer integration test must start normal-mode nodes, not demo nodes")
		require(any(f"/tcp/{peer_b.peer_port}/" in addr for addr in state_b["network"]["addresses"]),
			"configured fixed peer port was not advertised")
		discovery = discover_peer(peer_a.base_url, peer_b.base_url, peer_b_id, "initial connection")

		fixture_url = f"http://127.0.0.1:{server.server_port}"
		published = api_json(peer_b.base_url, "/api/services", "POST", {"name": "Peer B fixture", "target": fixture_url})
		service_id = published.get("id")
		require(isinstance(service_id, str) and service_id, "service publish did not return a service id")
		wait_for("published service metadata on Peer A",
			lambda: find_service(get_state(peer_a.base_url), peer_b_id, service_id) is not None, 15.0)
		remote_service = find_service(get_state(peer_a.base_url), peer_b_id, service_id)
		require(remote_service is not None, "Peer A did not discover Peer B's service")
		get_proxy(peer_a.base_url, remote_service.get("url", ""), FixtureHandler.body)
		print("PASS published local HTTP fixture served through Peer A's mesh proxy")

		source = scratch / "source-folder"
		source.mkdir()
		file_path = source / "peer-transfer.txt"
		first_bytes = b"Loom peer smoke: first exact-byte transfer.\n" * 32
		second_bytes = b"Loom peer smoke: transfer recovered after Peer B restart.\n" * 32
		file_path.write_bytes(first_bytes)
		folder = api_json(peer_a.base_url, "/api/folders", "POST", {
			"path": str(source), "peerId": peer_b_id, "name": "Peer smoke folder",
		})
		folder_id = folder.get("id")
		require(isinstance(folder_id, str) and folder_id, "folder registration did not return an id")
		received = peer_b.data_dir / "received" / folder_id / file_path.name
		wait_file(received, first_bytes)
		require(hashlib.sha256(received.read_bytes()).digest() == hashlib.sha256(first_bytes).digest(),
			"initial receiver file hash did not match the source")
		def receipt_matches(base_url: str, expected_id: str, expected_name: str, expected_sender: str, expected: bytes) -> bool:
			for item in get_state(base_url).get("receivedFolders", []):
				if item.get("id") == expected_id:
					return (item.get("name") == expected_name and item.get("peerId") == expected_sender
						and item.get("bytes") == len(expected) and len(item.get("files", [])) == 1
						and item["files"][0].get("sha256") == hashlib.sha256(expected).hexdigest())
			return False

		wait_for("received folder in Peer B dashboard state",
			lambda: receipt_matches(peer_b.base_url, folder_id, "Peer smoke folder", peer_a_id, first_bytes), 10.0)
		download_path = f"/api/received/{folder_id}/files/{file_path.name}"
		status, _, downloaded = request(peer_b.base_url, download_path)
		require(status == 200 and downloaded == first_bytes, "received-file download differs from transferred bytes")
		print("PASS automatic byte-for-byte folder transfer, receiver dashboard metadata, and file download")

		# Share a distinct source in the opposite direction, as in the two-computer demo.
		reverse_source = scratch / "reverse-source"
		reverse_source.mkdir()
		reverse_name = "reply #1.txt"
		reverse_bytes = b"Reply from Peer B to Peer A.\n"
		(reverse_source / reverse_name).write_bytes(reverse_bytes)
		reverse_folder = api_json(peer_b.base_url, "/api/folders", "POST", {
			"path": str(reverse_source), "peerId": peer_a_id, "name": "Peer B replies",
		})
		wait_file(peer_a.data_dir / "received" / reverse_folder["id"] / reverse_name, reverse_bytes)
		wait_for("reverse transfer receipt", lambda: receipt_matches(
			peer_a.base_url, reverse_folder["id"], "Peer B replies", peer_b_id, reverse_bytes), 10.0)
		status, _, downloaded = request(peer_a.base_url,
			f"/api/received/{reverse_folder['id']}/files/{urllib.parse.quote(reverse_name, safe='')}")
		require(status == 200 and downloaded == reverse_bytes, "encoded filename download failed")
		print("PASS the second computer can also share a separate folder back to the first")

		peer_b.stop()
		wait_peer(peer_a.base_url, peer_b_id, False, timeout=20.0)
		peer_b.start()
		peer_b.wait_ready()
		restarted_state = api_json(peer_b.base_url, "/api/state")
		require(restarted_state.get("network", {}).get("peerId") == peer_b_id,
			"Peer B identity did not persist across restart")
		require(received.is_file() and received.read_bytes() == first_bytes,
			"Peer B's managed received file did not persist across restart")
		require(receipt_matches(peer_b.base_url, folder_id, "Peer smoke folder", peer_a_id, first_bytes),
			"receiver dashboard metadata did not persist across restart")
		persisted_service = find_service(restarted_state, peer_b_id, service_id)
		require(persisted_service is not None, "Peer B's published service did not persist across restart")
		require(persisted_service.get("name") == "Peer B fixture" and persisted_service.get("target") == fixture_url,
			"Peer B's published service configuration changed across restart")
		discovery_after_restart = discover_peer(peer_a.base_url, peer_b.base_url, peer_b_id, "restart reconnection")
		remote_service = None
		wait_for("persisted service metadata on Peer A",
			lambda: find_service(get_state(peer_a.base_url), peer_b_id, service_id) is not None, 15.0)
		remote_service = find_service(get_state(peer_a.base_url), peer_b_id, service_id)
		require(remote_service is not None and remote_service.get("name") == "Peer B fixture"
			and remote_service.get("target") == fixture_url, "Peer A did not receive the persisted service configuration")
		get_proxy(peer_a.base_url, remote_service.get("url", ""), FixtureHandler.body)

		file_path.write_bytes(second_bytes)
		wait_file(received, second_bytes)
		wait_for("edited file receipt", lambda: receipt_matches(
			peer_b.base_url, folder_id, "Peer smoke folder", peer_a_id, second_bytes), 10.0)
		print("PASS Peer B identity, received folders, and service persisted; edits synced automatically after restart")

		# Restart the sender too: persisted manifests must still notice new nested files.
		peer_a.stop()
		peer_a.start()
		peer_a.wait_ready()
		discover_peer(peer_a.base_url, peer_b.base_url, peer_b_id, "sender restart")
		nested = source / "nested" / "new.bin"
		nested.parent.mkdir()
		nested_bytes = bytes(range(256)) * 64
		nested.write_bytes(nested_bytes)
		wait_file(peer_b.data_dir / "received" / folder_id / "nested" / "new.bin", nested_bytes)
		print("PASS new nested binary files sync automatically after the sender restarts")
		if discovery == "manual" or discovery_after_restart == "manual":
			print("NOTE This run used the advertised multiaddress fallback where mDNS discovery was not observed.")
		else:
			print("PASS both initial discovery and restart reconnection used mDNS")
	except Exception:
		for child in (peer_a, peer_b):
			output = child.diagnostics()
			if output.strip():
				print(f"{child.name} output (network key redacted):", file=sys.stderr)
				print(output, file=sys.stderr)
		raise
	finally:
		peer_b.stop()
		peer_a.stop()
		server.shutdown()
		server.server_close()
		fixture_thread.join(timeout=2.0)


def build_binary(output_path: Path) -> int:
	build = subprocess.run(
		["go", "build", "-o", str(output_path), "./cmd/loom"],
		cwd=ROOT,
		check=False,
		text=True,
		capture_output=True,
	)
	if build.returncode != 0:
		sys.stderr.write(build.stderr)
		sys.stderr.write(build.stdout)
	return build.returncode


def main() -> int:
	parser = argparse.ArgumentParser(description=__doc__)
	parser.add_argument("--binary", help="use an existing Loom binary instead of building one")
	args = parser.parse_args()
	try:
		with tempfile.TemporaryDirectory(prefix="loom-peer-smoke-") as temp_dir:
			scratch = Path(temp_dir)
			if args.binary:
				binary = Path(args.binary).expanduser()
				if not binary.is_absolute():
					binary = ROOT / binary
				binary = binary.resolve()
				require(binary.is_file(), f"binary was not found: {binary}")
			else:
				binary = scratch / "loom"
				result = build_binary(binary)
				if result != 0:
					return result or 1
			require(os.access(binary, os.X_OK), f"binary is not executable: {binary}")
			network_key = secrets.token_hex(32)
			run_checks(binary, scratch, network_key)
	except FileNotFoundError as error:
		print(f"Missing required command: {error.filename}", file=sys.stderr)
		return 2
	except SmokeFailure as error:
		print(f"FAIL {error}", file=sys.stderr)
		return 1
	except Exception as error:
		print(f"FAIL {error}", file=sys.stderr)
		return 1
	print("Loom normal-mode peer smoke test passed.")
	return 0


if __name__ == "__main__":
	raise SystemExit(main())
