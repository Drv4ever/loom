#!/usr/bin/env python3
"""Fail early when one of the local demo ports is already occupied."""

from __future__ import annotations

import socket
import sys


def main() -> int:
	ports = [int(value) for value in sys.argv[1:]]
	if not ports:
		print("usage: check_ports.py PORT...", file=sys.stderr)
		return 2

	for port in ports:
		with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
			sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
			try:
				sock.bind(("127.0.0.1", port))
			except OSError as error:
				print(f"Port {port} on 127.0.0.1 is unavailable: {error}", file=sys.stderr)
				return 1
	return 0


if __name__ == "__main__":
	raise SystemExit(main())
