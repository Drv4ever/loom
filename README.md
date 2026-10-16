# Loom

Loom is a small encrypted application mesh for connecting services and copying selected folders between your devices. It uses libp2p for peer communication; it does not create a system VPN, change operating-system routes, or send all device traffic through the mesh.

## Try the local demo

Requirements: Go 1.27 or newer, Bun, and Python 3. OpenSSL is needed to generate a shared key for real devices. Install the frontend packages once, then start both apps from the repository root:

```sh
cd web && bun install
cd ..
make demo
```

Open [http://127.0.0.1:3000](http://127.0.0.1:3000). The Go API listens on `127.0.0.1:8787`; its demo data is stored in `.loom-demo/`. The demo creates three real local libp2p nodes named Primary laptop, Studio workstation, and Linux server, including a direct link and a circuit-relayed link. Their names and demo labels identify them as fixtures, not physical remote devices. The demo includes a real local HTTP service and sample folders so service access, file transfer, activity, and reconnect behavior can be exercised without another computer.

`Ctrl-C` stops the two processes started by `make demo`. It does not remove `.loom-demo/`.

## Run on real devices

For a live demo with two physical computers on the same Wi-Fi, follow [the two-computer walkthrough](docs/TWO-COMPUTER-DEMO.md). `./scripts/run.sh --name "Laptop A" --network-key-file "$HOME/.loom-demo-network.key"` starts a real backend and dashboard together, with a fixed peer port of 4001. `make run` also starts a real device, generating its own network key if none has been stored yet; the other computer must use that same key.

Install the same Loom version and use one private network key on each device. Generate the key once and keep it in a private file:

```sh
umask 077
openssl rand -hex 32 > "$HOME/.loom-network-key"
export LOOM_NETWORK_KEY="$(cat "$HOME/.loom-network-key")"
```

Share that key with the other device through a trusted private channel. Do not commit the key, paste it into tickets, or enable shell tracing while using it. Diagnostics omit network-key and private-key material.

Start the backend on each device, giving each its own name and data directory:

```sh
go run ./cmd/loom \
  --name "Primary laptop" \
  --listen 127.0.0.1:8787 \
  --data-dir "$HOME/.loom" \
  --network-key-file "$HOME/.loom-network-key" \
  --peer-listen /ip4/0.0.0.0/tcp/4001
```

On the other device, use a different `--name` and `--data-dir`, but the same key. Devices on the same LAN discover each other through mDNS. Start the dashboard for each local backend with `cd web && bun run dev --host 127.0.0.1`; the dashboard uses port 3000 and the backend uses port 8787. If a peer is on a reachable network without mDNS, add its libp2p multiaddress from the Devices view. A node can be started with `--relay` to provide a circuit relay; Loom does not include a hosted relay service.

Services are published from a device to its loopback HTTP target, such as `http://127.0.0.1:3001`. Loom proxies requests to that approved service over the peer connection. Folder sync automatically copies new and changed files from the selected local source path to the receiving peer's Loom-managed folder under its data directory. The receiver's **Received folders** table shows local paths, file lists, and download links, including after a restart. Each source folder sends one way; share a separate source folder from the other computer to send files back. Source deletions are not propagated, and deleting the Loom folder entry stops tracking without deleting received files. The browser cannot browse another machine's disk, so enter the source path on the device that owns it.

## Build and checks

```sh
make build  # Go backend and production frontend build
make test   # Black-box API smoke test with an isolated demo mesh
make test-peers # Two independent normal-mode peers, local service, and folder transfer
make check  # Go test/vet, frontend typecheck/lint, and both integration tests
```

`make test-peers` gives both temporary nodes the same random private-network key, checks mDNS discovery, then exercises a loopback HTTP service and byte-for-byte folder sync across a restart. It uses an advertised multiaddress only if mDNS discovery is not observed and reports that fallback. Test data and keys live in temporary directories and are removed on exit.

To run the built frontend locally, start `./bin/loom --demo --listen 127.0.0.1:8787 --data-dir .loom-demo` in one terminal and `cd web && bun run start` in another. The preview server uses the same local API and service-proxy routes as the development dashboard.

See [docs/DEMO.md](docs/DEMO.md) for a short walkthrough of the demo and real-device setup.

The independent review, verified behavior, and demo boundaries are recorded in [docs/VALIDATION.md](docs/VALIDATION.md).
