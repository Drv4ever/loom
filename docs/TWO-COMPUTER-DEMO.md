# Two-computer faculty demo

Use exactly two physical computers, one real Loom node on each. The presentation story is: **an app and files on B become accessible from A**. Prepare the shared key and rehearse discovery before presenting.

## Prepare both devices

Use the same checkout and install Go 1.27+, Bun, and Python 3. Run `cd web && bun install` once on each computer. Join the same Wi-Fi or wired LAN; guest networks may isolate clients.

Generate the shared key on **A only**:

```sh
umask 077
openssl rand -hex 32 > "$HOME/.loom-demo-network.key"
```

Copy that file privately to B at the same path. On B, run `chmod 600 "$HOME/.loom-demo-network.key"`. Do not generate a separate key or copy A's data directory: the devices need different identities.

From the repository root on A:

```sh
./scripts/demo.sh --name "Laptop A" \
  --network-key-file "$HOME/.loom-demo-network.key"
```

Run the same command on B with `--name "Laptop B"`. Open [the dashboard](http://127.0.0.1:3000) locally on both. State persists in `$HOME/.loom-two-computer`. To use another directory, prefix the command with `LOOM_DATA_DIR=/absolute/path`.

Allow local-network access when asked by the operating system. Peer traffic uses TCP 4001 and mDNS uses UDP 5353. The API (8787) and dashboard (3000) stay on loopback.

If mDNS does not discover B, copy B's address containing its LAN IP from **Devices** or **Settings → Connection details**, then paste it into A's **Connect device** dialog. An address containing `127.0.0.1` cannot reach the other computer. Both devices must show the same network ID and different peer IDs.

If the stored and supplied keys differ, use that directory's original key or choose a fresh data directory. Preserve any received files you need. If a dashboard port is busy, stop the previous launcher before starting another.

## Three-minute presentation

1. **Connect (20 seconds).** Show A and B appearing online. Explain that each dashboard controls its own computer.
2. **Access an app (60 seconds).** On B, publish a useful local HTTP app in Services, with access granted to A. On A, open that service. Keep B's app visible so the audience sees where it runs.
3. **Share files (60 seconds).** On B, add a small folder, select A, and choose Two way or Send only. On A, accept the invitation under Incoming shares and show its destination. Edit a file on B and show the update arriving on A. Keep Activity visible as evidence.
4. **Recovery, if time allows (40 seconds).** Stop B's launcher with Ctrl-C. A retains its service with Device offline. Restart B with the same command and show reconnection and availability.

For a simple local web app on B, create a tiny project status page:

```sh
mkdir -p "$HOME/Loom Demo App"
printf '<h1>Project preview on Laptop B</h1><p>Accessed privately through Loom.</p>\n' > "$HOME/Loom Demo App/index.html"
python3 -m http.server 8080 --bind 127.0.0.1 --directory "$HOME/Loom Demo App"
```

Publish `http://127.0.0.1:8080` on B. Stop the Python server with Ctrl-C after the presentation.

For the folder demonstration on B:

```sh
mkdir -p "$HOME/Loom Demo Files"
printf 'Hello from Laptop B!\n' > "$HOME/Loom Demo Files/hello.txt"
```

Enter the expanded absolute path in B's folder dialog, select Laptop A, and accept it on A. The suggested destination is `$HOME/.loom-two-computer/received/<folder-id>`. A may instead choose an existing empty directory. Then edit on B:

```sh
printf 'Edited live on Laptop B!\n' >> "$HOME/Loom Demo Files/hello.txt"
```

The update should arrive after the next scan, normally within a few seconds. Open or refresh the received file to show its content. In Two way mode, an edit at A's accepted destination also travels back; keep the short live presentation to the rehearsed B-to-A edit.

## Features to rehearse separately

Conflict resolution, propagated deletions and recovery, send-only behavior, TCP service forwarding, and relays have their own controls and regression coverage. The short faculty presentation does not need to exercise every feature. PIN enrollment is disabled; use the shared key and direct connection workflow above.

`make test-peers` rehearses two independent real processes, a local HTTP service, explicit folder acceptance with a custom destination, edits and deletions from the receiver, and persistence through restarts. Rehearse the actual laptops too, since a process test cannot validate their Wi-Fi and firewall settings.
