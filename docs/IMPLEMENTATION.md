# Demo implementation contract

Build a real, small application mesh in Go using go-libp2p, not a system VPN. Reuse libraries. No kernel tunnel, Headscale deployment, cloud authentication, or production claims in this demo. This supersedes the initial long-term PLAN.md for implementation scope.

## Runtime

- `go run ./cmd/loom --demo --listen 127.0.0.1:8787 --data-dir .loom-demo` starts the API plus three real local libp2p nodes (primary laptop, studio workstation, Linux server), actual temporary sample folders and a real sample HTTP service. Demo fixtures must be distinguishable from remote physical devices in UI/docs. Demo peer engines must really exchange encrypted traffic and files.
- Normal `go run ./cmd/loom --name my-device --listen 127.0.0.1:8787 --data-dir .loom --network-key <64-hex>` starts one node. Same key creates an isolated private libp2p mesh. Discover peers via mDNS on LAN; manual multiaddress join across reachable networks. Optional relay address support if achievable without destabilizing demo.
- Dashboard at Bun port 3000 proxies `/api` and `/proxy` to Go 8787. Production frontend build/run instructions should be tested. API localhost only by default, no permissive CORS.
- Persistent identity and network key; restrictive file modes; graceful shutdown; errors handled. Sync writes cannot escape selected destination root or follow untrusted symlinks. Rate/size limits and request timeouts.

## HTTP contract

JSON camelCase. Errors: `{ "error": "human readable message" }`. Each mutation returns changed object or `{ "ok": true }`; frontend refreshes state. No fake successful toasts.

`GET /api/state` returns:

```json
{
  "network": {"id":"...","name":"Personal workspace","connected":true,"demo":true,"peerId":"...","addresses":["/ip4/..."],"version":"0.1.0"},
  "devices":[{"id":"peer-id","name":"MacBook Pro","platform":"darwin","online":true,"self":true,"connection":"local","latencyMs":0,"address":"...","lastSeen":"ISO8601"}],
  "services":[{"id":"...","name":"Project preview","deviceId":"...","deviceName":"Studio","protocol":"http","target":"http://127.0.0.1:...","port":3000,"status":"healthy","url":"/proxy/<deviceId>/<serviceId>/"}],
  "folders":[{"id":"...","name":"Design assets","path":"/absolute/path","peerId":"...","peerName":"Studio","status":"synced","files":3,"bytes":1234,"progress":100,"lastSync":"ISO8601","paused":false,"error":""}],
  "activity":[{"id":"...","type":"device|service|sync|system","title":"...","detail":"...","time":"ISO8601"}],
  "stats":{"onlineDevices":3,"services":2,"syncedFiles":6,"transferredBytes":1234}
}
```

- `GET /api/health`: `{ "ok": true }`.
- `POST /api/peers` body `{ "address": "/ip4/.../p2p/..." }`: connects a peer.
- `POST /api/services` body `{ "name":"Preview", "target":"http://127.0.0.1:3001" }`: publishes a local HTTP service. Validate loopback-only HTTP target; proxy target never taken from remote HTTP query.
- `DELETE /api/services/{id}`: removes only locally-owned published service.
- `GET /proxy/{peerId}/{serviceId}/*`: real HTTP proxy over libp2p to approved published service; strip hop-by-hop headers; handle safe path joining.
- `POST /api/folders` body `{ "path":"/absolute/path", "peerId":"...", "name":"Assets" }`: register selected source; in demo choose isolated receiver root automatically. In normal mode destination root configured under receiver data directory, never remote-chosen absolute path.
- `POST /api/folders/{id}/sync`: transfer now; return real status.
- `POST /api/folders/{id}/pause` body `{ "paused":true }`.
- `DELETE /api/folders/{id}`: stop tracking, never delete user files.
- `POST /api/network` body `{ "connected":false }`: pause/resume application networking and sync (close existing connections when paused).
- `POST /api/demo/change`: demo-only writes a sample source file, sync it, produce real activity and transferred byte counts. Return `{ "ok":true }` only after successful transfer; normal mode 404.
- `GET /api/diagnostics`: safe JSON diagnostics without private keys, key material, or file contents.

Polling every 2 seconds is sufficient. Backend may add fields. Coordinate any contract changes. No pretend bandwidth charts; any graph depicts actual peers and transport. Offline API state must be visible. Service and folder dialogs support actual paths/targets. Browser cannot pick arbitrary server directories; use explicit path entry with useful explanation.

## Visual direction

shadcn preset `b1PzeK`, TanStack Start scaffold from user command. A calm professional network workspace: dark navy sidebar, very light cool neutral content, blue accent, subtle cyan direct links. Use the preset font. Tokens approximately ink #182235, sidebar #142239, surface #f5f7fb, panel #ffffff, blue #386be8, muted #748198. One memorable element: a polished node topology diagram showing actual devices and connection state. Quiet list/table surfaces around it. Responsive, keyboard-accessible dialogs, loading/empty/error states. Sections Overview, Devices, Services, Folders, Activity, Settings; implement functional core actions, not marketing pages.

## Ownership and commits

- Backend agent owns `cmd/`, `internal/`, `go.mod`, `go.sum` and Go tests; commit working milestones only for its own files.
- Frontend agent owns `web/`; run the requested shadcn initialization, implement, build/typecheck, commit working milestones only for its own files.
- Parent owns scripts, README, integration review and fixes. Agents may read shared files; do not overwrite another owner's files. No additional subagents unless coordinated with parent.
