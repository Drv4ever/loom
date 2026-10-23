# Loom

Loom is a private application mesh for accessing selected services and synchronizing folders across devices. Each computer runs one Go/libp2p node and a local dashboard. It does not create a system VPN or change operating-system routes.

## Start a two-device demo

Requirements on both computers: Go 1.27+, Bun, and Python 3. Install the same checkout on both, then install frontend dependencies with `cd web && bun install`.

Generate one key on computer A:

```sh
umask 077
openssl rand -hex 32 > "$HOME/.loom-demo-network.key"
```

Copy that file privately to computer B at the same path and run `chmod 600 "$HOME/.loom-demo-network.key"` there. Each computer keeps its own identity and data directory.

From the repository root, run this on A, and use `Laptop B` as the name on B:

```sh
./scripts/demo.sh --name "Laptop A" \
  --network-key-file "$HOME/.loom-demo-network.key"
```

Open [the dashboard](http://127.0.0.1:3000) on each computer. The API stays on `127.0.0.1:8787`, peer traffic uses TCP 4001, and mDNS discovers devices on the same LAN. If discovery is unavailable, copy B's LAN multiaddress from Devices or Settings into A's **Connect device** dialog.

`make demo` starts one real device with the dashboard. It creates no simulated peers or sample services. Supply the same stored network key on both devices before presenting. The demo launcher stores state in `$HOME/.loom-two-computer`; override it with `LOOM_DATA_DIR`. Ctrl-C stops its backend and frontend. `make run` uses `$HOME/.loom` by default.

Follow the [two-computer walkthrough](docs/TWO-COMPUTER-DEMO.md) for the short presentation: connect two devices, open an app on B from A, sync one folder, and optionally restart B. Rehearse on the actual presentation network.

## Services and folders

Publish a loopback HTTP app in **Services**. Its availability is checked locally and advertised to peers; remembered services show **Device offline**, **App unavailable**, **Checking**, or **Available**. Select named devices when publishing, and use **Edit access** to change an existing service's permissions. An empty allowlist means all devices holding this private network key. Trusted HTTP mode preserves authentication and origin headers for apps that need them.

TCP services must be explicitly published with protocol **TCP**. On another device, use the service's forward action to choose a local loopback port. Every tunnel checks the publication and allowlist; arbitrary remote port targets are rejected. HTTP publications cannot be used as raw TCP tunnels.

Add a folder from the computer that owns its source path. The other device sees an invitation under **Incoming shares** and must accept it, using the managed destination or an existing empty directory. No shared content transfers before acceptance. Both devices can pause or remove their side without deleting files.

- **Two way** synchronizes edits and deletions in both replicas. The source device initiates scans every two seconds. Concurrent edits preserve the original and a checked conflict copy for Keep Local, Keep Remote, or Keep Both. Unresolved versions remain protected across repeated syncs and restarts.
- **Send only** keeps the source authoritative: it never fetches or deletes source files in response to receiver changes. Receiver edits can be replaced by the source on the next sync.
- Propagated deletions are archived on the device applying the deletion. Use **Recover files** there to restore an absent path. Recovery never overwrites an existing file. In send-only shares, pause the share and copy a recovered file elsewhere if it should be kept independently of the source.

Transfers use SHA-256 verification, temporary files, directory-root containment, and bounded delta chunks. Symlinks and empty directories are skipped. Limits are 100 MiB per file, 1 GiB per folder scan, and 10,000 files. `.gitignore` and `.loomignore` supply exclusion rules. Archives remain on disk; the retention helper is not an automatic cleanup service.

PIN enrollment is unavailable: its old voucher exchange did not actually join a device to the network. Use shared-key setup and multiaddresses. Relays remain optional and need a separately configured reachable Loom relay using the same network key; the two-device LAN demo does not need one.

## Build and verify

```sh
make build       # Backend and production frontend
make test        # One real daemon: API, proxy, health, boundaries, restart
make test-peers  # Two real processes: consent, custom destination, sync, restart
make check       # Go tests/vet, TypeScript, lint, and both smoke tests
```

The smoke tests create isolated identities and a random shared key in temporary directories. The peer test reports whether discovery used mDNS or an advertised-address fallback. They do not substitute for rehearsing the physical devices.

For separate processes, run `./bin/loom --name "Laptop A" --data-dir "$HOME/.loom" --network-key-file "$HOME/.loom-demo-network.key" --peer-listen /ip4/0.0.0.0/tcp/4001`, and run `cd web && bun run dev` for the dashboard. After `make build`, `bun run start` serves the built frontend with the same local API/proxy routes.

See the [architecture and API contract](docs/IMPLEMENTATION.md), [review fixes](docs/REVIEW-FIXES.md), and [validation](docs/VALIDATION.md). The [original long-term plan](docs/archive/ORIGINAL-PLAN.md) is historical and does not describe the current libp2p implementation.
