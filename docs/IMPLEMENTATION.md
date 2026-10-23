# Implementation and API contract

## Architecture

One process runs one Go/libp2p node per device. The shared 32-byte private-network key gates the mesh; identities and network configuration persist locally. libp2p provides encrypted peer transport, mDNS discovery, manual multiaddress connections, and optional Circuit Relay v2. There is no kernel tunnel, Headscale service, or Syncthing dependency.

The React/TanStack dashboard runs on loopback port 3000 and proxies `/api` and `/proxy` to the Go API on loopback 8787. The real-device launcher uses peer TCP 4001. API host/origin checks restrict browser mutations. Errors use `{ "error": "human readable message" }`.

## Services

`POST /api/services` accepts `{name,target,protocol?,trusted?,allowedPeers?}`. Protocol defaults to `http`; targets must be loopback HTTP URLs or, for `tcp`, loopback host:port values. Empty allowlists grant all members of the private network. `POST /api/services/{id}/access` changes a local service's allowlist; revoked peer connections are closed. `DELETE /api/services/{id}` unpublishes it.

`/proxy/{peerId}/{serviceId}/*` opens HTTP over an authorized libp2p stream. TCP services use `POST /api/forwards` with `{peerId,serviceId,localPort?}`; zero selects an available loopback port. A successful remote handshake must confirm publication and permission before a listener is reported active. Direct target forwarding and tunneling HTTP publications as TCP are rejected. `DELETE /api/forwards/{id}` stops a listener. Every new inbound TCP stream checks the service and allowlist.

Local probes periodically check HTTP responses (5xx/transport failures mean unavailable) or TCP connectivity. Peer summaries advertise the result. Remembered peer identities, names and services persist; offline peers keep their service entries. Statuses are `checking`, `available`, `app_unavailable`, `device_offline`, or `access_denied`. Availability is a point-in-time probe, not a guarantee that every application route works.

## Folder sharing

`POST /api/folders` accepts `{path,peerId,name?,mode?}`; `mode` is `two_way` (default) or `send_only`. The legacy `readOnly:true` flag maps to send-only. Paths are explicit absolute source paths outside Loom's data directory. The source initiates sync every two seconds while connected and unpaused.

The `/loom/sync/3.0.0` handshake introduces a folder invitation bound to its sender. The receiver persists a `pending` entry without transferring shared files. `POST /api/received/{id}` accepts `{action,path?}`, with action `accept`, `decline`, `pause`, `resume`, or `remove`. Acceptance chooses the managed destination or an existing empty directory outside the data directory and other shares. Rejected/removed shares retain a denial record, so repeated source scans do not silently recreate consent. Removing a share preserves files.

`POST /api/folders/{id}/sync`, `POST /api/folders/{id}/pause` with `{paused}`, and `DELETE /api/folders/{id}` control the initiating side. Receiver controls serialize against inbound transfers and reset active streams before changing permissions or destinations. Older sync protocols are not accepted by application nodes; use the same version on both devices.

Two-way reconciliation compares local/remote SHA-256 manifests to the last agreed per-file baseline. Both replicas detect deletions and persist tombstones. A file recreated after an acknowledged deletion can propagate again. In send-only mode, the source never fetches receiver files or applies receiver deletions.

Concurrent edits save a checksum-verified conflict copy, preserve the original and shared baseline, and avoid duplicate copies for an unchanged remote version. Conflict records and baselines persist. `GET /api/folders/{id}/conflicts` lists conflicts; `POST /api/folders/{id}/resolve` accepts `{conflictId,resolution}` with `local`, `remote`, or `both`. Resolution records the observed remote hash so the chosen value reconciles explicitly; a further remote edit creates another conflict. A changed local version cannot be silently overwritten by a stale Keep Remote action.

All protocol descriptors, requested downloads and deletions are validated. Reads, temporary files and renames use `os.Root` plus symlink/regular-file checks. Incoming operations must match the responder's reconciliation plan, and destination hashes are checked again before installation. Whole-file and per-chunk hashes are verified. Delta chunks are bounded to 1 MiB and streamed through a temporary file rather than retained in an unbounded map. Limits are 100 MiB/file, 1 GiB/scan and 10,000 files. Empty directories and symlinks are skipped. Content hashes, rather than modification times, determine changes.

`GET /api/received/{id}/files/{path...}` downloads a catalogued file through its locally accepted root, including custom destinations. `GET /api/folders/{id}/trash` lists local deletion archives; `POST /api/folders/{id}/restore` with `{id: archiveRelativeId}` restores an absent path. Archive IDs are validated and absolute client paths are not accepted. Recovery never overwrites an existing file. Archives preserve deletions applied by Loom, not files users already removed directly from their local filesystem. Automatic archive expiry is not scheduled.

## State, setup and diagnostics

`GET /api/state` returns network, devices, services, forwards, outgoing folders, incoming shares, activity and counts. `GET /api/health` returns `{ok:true}`. `POST /api/peers` accepts `{address}`. `POST /api/network` accepts `{connected}` and persists pause state. Device block/unblock routes close and gate peer connections. Diagnostics omit private keys and network-key material.

PIN generation/claim endpoints return HTTP 501 with setup instructions; pairing status reports unavailable. No successful credential voucher is presented as completed enrollment. Both devices must start with the same network key file and keep distinct local identities.

The original broader architecture is archived in [archive/ORIGINAL-PLAN.md](archive/ORIGINAL-PLAN.md). The supported demo is [TWO-COMPUTER-DEMO.md](TWO-COMPUTER-DEMO.md).
