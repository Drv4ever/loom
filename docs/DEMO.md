# Demo entry point

The supported presentation uses **two physical devices**, each running one real Loom node. `make demo` starts the local device and dashboard; it does not create peers, services, or file fixtures.

Follow [TWO-COMPUTER-DEMO.md](TWO-COMPUTER-DEMO.md) to prepare one shared network key, connect A and B, publish an app on B and open it from A, accept a folder share, and show a live file edit. Restart B only if time permits. Keep Activity visible.

For automated rehearsal, `make test` checks a single real daemon and `make test-peers` checks two separate real processes with isolated temporary state. No removed demo endpoints are used.
