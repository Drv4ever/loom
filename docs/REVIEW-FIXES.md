# Review fixes

This tracks the review of `main` at `3c14418`, with the faculty presentation scoped to two physical devices.

| Review finding | Resolution | Evidence |
| --- | --- | --- |
| Demo launcher uses removed `--demo` and simulated APIs | Both launchers start one real node; API and peer smoke tests use real daemon processes | `make demo` startup/shutdown checked; `make test`, `make test-peers` |
| PIN claim announces success without enrollment | Removed PIN dashboard workflow; generation/claim return 501 and CLI explains shared-key setup | Pairing API and CLI regression tests; API smoke |
| Conflict baseline advances and overwrites a version on the next scan | Freeze unresolved paths on both peers, retain the shared baseline, verify copies, persist records, and record the observed remote hash on resolution | Repeated scans, restart serialization, older advanced baselines, all three resolutions, and subsequent deletion tests |
| Receiver deletion resurrects files | Detect deletion against the receiver's receipt catalog, persist tombstones on both sides, and recognize recreation/restoration after acknowledged deletion | Multiple cycles, real node restart, archive restore; peer process smoke |
| Read-only checkbox does not protect the source | Explicit Two way / Send only selector and protocol mode; send-only never fetches or applies receiver deletions to the source | Receiver edit/delete tests; legacy flag maps to send-only |
| Peer paths and transfer requests escape filesystem boundaries | Validate IDs, descriptors, requests and chunks; authorize operations against the plan; root filesystem operations with `os.Root`; recheck destination hashes | Traversal, sender ownership, symlink, unrequested download, corrupt content, and malformed chunk tests |
| Raw TCP bypasses publishing and permissions | Require an explicitly published TCP service, check its allowlist, reject raw targets and HTTP tunneling, preflight before creating the listener | Successful echo tunnel plus publication/protocol/permission rejection tests |
| Folder ownership and direction unclear | Shared folders and Incoming shares show paths and direction; initiating side shows the accepted remote destination | Browser check and frontend checks |
| Receiver lacks a sharing workflow | Persist invitations; accept/decline, managed/custom destination, pause/resume/remove, and retain files on removal | Consent, prefilled destination, custom destination, destination change, pause/decline and restart tests; browser acceptance |
| Deletion recovery missing from dashboard | Recovery dialog lists local archives and restores absent paths, refusing overwrite | Restore regression, browser restore, subsequent two-way sync |
| TCP creation sends users to terminal | Publish TCP in the service dialog; choose forward action and local port in the dashboard | Frontend checks, backend tunnel tests |
| Permissions require raw peer IDs; existing services cannot change access | Named-device checkboxes and edit-access dialog; validate and persist allowlists; disconnect revoked sessions | Browser publishing/editor checks; ACL tests |
| Registered services always appear published and vanish offline | Local HTTP/TCP probes; advertise health; persist remembered device/service metadata; show offline/unavailable/checking/available | API health smoke, peer offline/restart smoke |
| First-run page implies a useful connected mesh | Setup checklist and Waiting for another device status | Empty-state browser check |
| Documentation describes conflicting architectures and simulated peers | Archive the original proposal and earlier validation; rewrite current architecture, setup and two-device presentation | README, implementation contract, and two-computer walkthrough |
| Frontend typecheck/lint fail | Fix optional callback and redundant fallback conditions, plus unused imports | `bun run typecheck`, `bun run lint`, `bun run build` |

The two-device presentation remains deliberately short: access one app, synchronize one folder, and optionally restart the other device. PIN enrollment stays disabled. Relay support is optional and requires rehearsal on the exact network.
