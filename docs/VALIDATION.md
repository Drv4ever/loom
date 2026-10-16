# Demo validation

Verified locally on macOS/arm64 on 16 October 2026 using Go 1.27 and Bun 1.4.2. Implementation was split between Luna agents at max reasoning, then reviewed and exercised independently by the parent agent. The current implementation is an application mesh; the larger original PLAN.md remains a future product direction.

## Automated checks

All passed on the final implementation:

```sh
make check
go test -race ./cmd/... ./internal/...
make build
git diff --check
```

`make check` runs Go tests/vet, frontend type checking/lint, a real three-node relay demo smoke test, and a separate-process normal-mode peer smoke test.

Coverage includes incompatible network keys, connection rejection while paused, checksums and version preservation, safe retry after failed transfer, path traversal and symlink rejection, private-key file permissions, same-origin API protection, proxy sandbox headers, empty collection responses, persisted pause state, relay reconnects, real HTTP proxying, and exact received file contents.

The normal-mode test verifies persisted device identity and published services across peer restart, followed by another successful folder transfer. LAN discovery was observed in independent runs and after restart. One final concurrent-check run used the documented multiaddress fallback for its initial connection; multicast discovery can depend on the local network environment.

## Browser and launcher checks

- Reviewed the live dashboard at narrow and desktop widths, with keyboard focus, loading, API-unavailable, dialog validation, and published-service ownership states.
- Opened a real HTTP service through the circuit relay and published another service through the UI; invalid non-loopback targets produced a useful error.
- Changed a sample file through the UI, then independently compared every source/received file byte-for-byte on disk.
- Paused networking through the UI, confirmed actual service access returned 503, resumed networking, and confirmed the Linux peer still used the circuit relay.
- Ran the exact `make demo` launcher. Stopped its shell, verified both owned child processes exited, and relaunched immediately on the same ports.
- Restarted with networking and folder sync paused and an unsent source change present. Startup retained both pauses and did not transfer the file. Resuming networking alone left it unsent; resuming the folder delivered the exact bytes.
- Kept the application running beyond the original two-minute relay failure and verified the relay still served real HTTP responses.

## Review fixes

Review identified and fixed a manifest retry bug that could mark a failed file as delivered; incorrect self-dialing in the demo service proxy; missing relay reconnects; pause/restart failures; unsorted device state that made topology nodes jump; incomplete checksum/header handling; and UI feedback/focus issues. Regression tests cover the material networking and file-handling fixes.

## Scope to present accurately

- This provides selected HTTP-service access and one-way folder replication over encrypted libp2p connections. It does not install a VPN adapter or route arbitrary operating-system traffic.
- The three named demo peers are actual local processes within the Go application, not physical remote machines. Normal mode runs one peer per application instance.
- Folder sync transfers changed regular files, retains replaced receiver versions, and does not propagate deletions. It is not collaborative editing or a full backup product. Limits are 100 MiB per file and 1 GiB per scanned folder.
- HTTP proxying is bounded and sandboxed. It does not promise arbitrary authenticated web apps, WebSockets, transparent absolute asset paths, or general TCP/UDP forwarding.
- Devices sharing the network key are trusted members. There is no hosted account service, per-user ACL system, public relay fleet, or general Internet NAT-traversal guarantee. The demo includes a working private circuit relay; normal-device connectivity depends on reachable peer addresses and LAN discovery.

Run `make demo` and use [the walkthrough](DEMO.md) for the short presentation. The default dashboard is http://127.0.0.1:3000.
