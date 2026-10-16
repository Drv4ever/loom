# Three-minute Loom demo

Start the demo from the repository root with `make demo`, then open [http://127.0.0.1:3000](http://127.0.0.1:3000). It creates three actual local libp2p nodes: Primary laptop, Studio workstation, and Linux server. The labels identify them as demo fixtures; they are not physical remote computers. The Linux peer publishes a real local HTTP sample service and is reached through Studio's circuit relay.

## Talk track

**0:00–0:25 — The mesh.** Show the Overview topology and device list. “Loom connects the apps and folders I choose across my devices. This is an application mesh, not a system VPN: it does not reroute browser or operating-system traffic. These demo peers are real libp2p nodes running locally, and the diagram reflects their actual direct and relayed links.”

**0:25–0:55 — Service access.** Open the Linux server's sample service from Services. “This is a real HTTP response from the Linux demo service. The browser reaches it through Loom's peer connection and Studio's circuit relay; the proxy only targets a service explicitly published by that peer.”

**0:55–1:40 — File transfer.** In Folders, show the sample folder and its sync status. Click **Change sample file**. The equivalent API action is:

```sh
curl -sS -X POST http://127.0.0.1:8787/api/demo/change \
  -H 'Content-Type: application/json' -d '{}'
```

“Loom creates a new sample file and reports success only after it has transferred to Linux through the relay. The folder status, transferred bytes, and Activity entry update from that transfer.” To show flow control, pause the folder, trigger **Sync now**, and show the paused-folder error with no transfer; resume it and wait for the transfer to complete.

**1:40–2:30 — Reconnect.** Turn off the **Networking** switch, then show the peer's offline state and unavailable service. Turn it back on and wait for the peer and service to return. If you need the API control directly, use:

```sh
curl -sS -X POST http://127.0.0.1:8787/api/network \
  -H 'Content-Type: application/json' -d '{"connected":false}'
curl -sS -X POST http://127.0.0.1:8787/api/network \
  -H 'Content-Type: application/json' -d '{"connected":true}'
```

“Disconnect pauses Loom's peer connections and sync. Reconnecting restores the application mesh; it does not change the device's ordinary network routing.”

**2:30–3:00 — Real devices.** “These names are demo fixtures. On physical devices, I run one Loom node per device with the same private network key. Peers on the same LAN find each other through mDNS; the device list and topology then show those real peers. I can publish a loopback HTTP service or choose a folder on the device that owns it.”

## Before presenting on physical devices

Create a 64-character hex network key once, store it in a private file, and share it with the other device through a trusted private channel. Keep the key out of source control, terminal transcripts, and logs. Diagnostics omit key material, but the network key itself is a secret and should not be pasted into support output.

Run the same backend command on both devices, changing the name and data directory on the second:

```sh
go run ./cmd/loom \
  --name "Primary laptop" \
  --listen 127.0.0.1:8787 \
  --data-dir "$HOME/.loom" \
  --network-key "$LOOM_NETWORK_KEY"
```

Start `cd web && bun run dev --host 127.0.0.1` on each device and open its local dashboard. Add a service using an HTTP target bound to loopback, such as `http://127.0.0.1:3001`; do not use a remote browser URL as a service target. Register a source folder on the device that owns it and select the receiving peer. Files copy one way into the recipient's Loom-managed data directory; the receiver does not choose an arbitrary path through the browser, and folder removal never deletes received data.
