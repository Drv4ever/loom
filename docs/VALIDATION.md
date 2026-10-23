# Validation

Updated 2026-10-23 for the review fixes and two-device demo. The [earlier validation](archive/PRE-FIX-VALIDATION.md) is archived as historical context.

## Automated checks

- `make check`: Go tests and vet; frontend TypeScript and lint; one-device API smoke; two-process peer smoke.
- `go test -race ./internal/loom ./internal/cli`: race-enabled backend and CLI suite.
- `make build`: backend executable and production frontend.
- `git diff --check`: whitespace/error check.

The Go regressions cover repeated conflicts without overwrite or duplicate copies, serialized restart state, older incorrectly advanced baselines, Keep Local/Remote/Both propagation, unresolved-path deletion, receiver deletion over multiple cycles and a real node restart, send-only protection, restore/recreation, receiver consent, managed and custom destinations, changing a destination without deleting the source, pause/decline, and sender ownership.

Protocol tests exercise traversal, destination symlinks, unrequested downloads, corrupt file bytes, malformed chunk lengths, published TCP permission/protocol checks, working TCP echo forwarding, and termination of existing sessions when a forward stops. Existing HTTP proxy, ACL, private-network, relay, chunk, ignore, persistence, and CLI tests remain in the suite.

The API smoke uses one real daemon and a real local HTTP fixture. It checks empty first-run state, approved service access, service availability changes, invalid external targets, disabled PIN/raw TCP paths, host/origin checks, key-free diagnostics, and identity/network/publication persistence.

The peer smoke uses two independent real daemon processes with separate identities and a random shared key. It verifies HTTP proxying, explicit invitation acceptance, a custom receive directory, file hashes/downloads, sharing from either device, receiver edits and deletions in the same share, remembered offline services, restart persistence, and new nested binary files after restarting the sender. The final full check used mDNS for initial discovery and restart reconnections. Earlier runs exercised the advertised-address fallback when initial mDNS was not detected. The test reports fallback separately rather than treating it as mDNS success.

## Launcher and browser checks

Started `scripts/demo.sh` with isolated data. It built and started one backend plus the dashboard, advertised a real device, and used no removed demo flags or endpoints.

In the browser, checked the lone-device setup checklist and waiting status; accepted an incoming folder into an existing custom directory; published a service with a named-device grant; opened the existing service's access editor and verified its saved selection; and restored an archived file from the incoming share's recovery dialog. The restored file rejoined synchronization. Local service availability and incoming folder paths were visible in the dashboard.

## Practical boundaries

The automated two-process test does not validate two physical laptops' Wi-Fi, firewalls, or operating-system local-network permissions. Rehearse the [two-computer walkthrough](TWO-COMPUTER-DEMO.md) on the presentation network. A direct advertised LAN address is the discovery fallback.

Use the same build on both devices: the sync protocol is now `/loom/sync/3.0.0` and application nodes reject the legacy transfer protocol. Existing incoming folders from versions without consent need acceptance again. Keep identities and key files distinct from source folders.

PIN enrollment is disabled. HTTP availability is a bounded HEAD request and TCP availability is a connection probe; these cannot guarantee all app routes or future requests. Symlinks and empty directories are skipped; limits are 100 MiB/file, 1 GiB/scan, and 10,000 files. Deletion archives cover deletions applied by Loom, not files already removed directly by the user, and expiry is not automatically scheduled. In send-only mode the source remains authoritative, including after a receiver restores a file.
