#!/usr/bin/env python3
"""Wait for an HTTP endpoint to answer successfully."""

from __future__ import annotations

import sys
import time
import urllib.error
import urllib.request


def main() -> int:
	if len(sys.argv) not in (2, 3):
		print("usage: wait_http.py URL [TIMEOUT_SECONDS]", file=sys.stderr)
		return 2

	url = sys.argv[1]
	timeout = float(sys.argv[2]) if len(sys.argv) == 3 else 40.0
	deadline = time.monotonic() + timeout
	while time.monotonic() < deadline:
		try:
			with urllib.request.urlopen(url, timeout=1.5) as response:
				if 200 <= response.status < 300:
					return 0
		except (OSError, urllib.error.URLError):
			pass
		time.sleep(0.25)

	print(f"Timed out waiting for {url}", file=sys.stderr)
	return 1


if __name__ == "__main__":
	raise SystemExit(main())
