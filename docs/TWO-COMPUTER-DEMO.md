# Demo Loom on two computers on the same Wi-Fi

This runs one real Loom device on each computer. Files travel over the LAN through an encrypted peer connection. Use normal mode for this demo; `make demo` creates three fixture nodes on one computer.

## 1. Prepare both computers

Use the same checkout/version of this repository on both computers. Install Go 1.27+, Bun, and Python 3, then run this once in each checkout:

```sh
cd web
bun install
cd ..
```

These commands use a macOS/Linux shell. Join both computers to the same Wi-Fi or wired LAN. Guest Wi-Fi with client isolation can block communication even when the Wi-Fi name matches.

## 2. Create one shared network key

On **computer A only**, create a private key file:

```sh
umask 077
openssl rand -hex 32 > "$HOME/.loom-demo-network.key"
```

Copy this file privately to computer B as `$HOME/.loom-demo-network.key`. Both files must contain the same key. On B, run `chmod 600 "$HOME/.loom-demo-network.key"`. Do not generate a second key on B or copy A's entire data directory: each computer needs its own identity.

## 3. Start Loom on each computer

From the repository root on **A**:

```sh
LOOM_DATA_DIR="$HOME/.loom-two-computer" ./scripts/run.sh \
  --name "Laptop A" \
  --network-key-file "$HOME/.loom-demo-network.key"
```

On **B**:

```sh
LOOM_DATA_DIR="$HOME/.loom-two-computer" ./scripts/run.sh \
  --name "Laptop B" \
  --network-key-file "$HOME/.loom-demo-network.key"
```

The launcher builds the backend, starts the local dashboard, waits for both to be ready, and stops both on Ctrl-C. Open [http://127.0.0.1:3000](http://127.0.0.1:3000) **on each computer**. The browser talks to its own computer's backend.

Allow Loom to access the local network when the operating system asks. The real-device launcher uses **TCP port 4001** for peer traffic; LAN discovery uses mDNS (UDP 5353). The dashboard (3000) and API (8787) stay on loopback and do not need to be exposed to the LAN. To change the peer port, append `--peer-listen /ip4/0.0.0.0/tcp/4002`.

In **Devices**, each dashboard should show the other computer online within a few seconds. In **Settings**, both should show the same network identity; their connection addresses end with different peer IDs.

If discovery does not work, open B's **Settings → Connection details**, copy the address containing B's LAN IP (for example `/ip4/192.168.1.24/tcp/4001/p2p/...`), and paste it into A's **Add device** dialog. Do not use an address containing `127.0.0.1`. A manual connection is enough; both computers can then send folders. If that also fails, check TCP 4001, local-network permissions, and Wi-Fi client isolation.

## 4. Show a real file appearing on B

On **A**, create a source folder:

```sh
mkdir -p "$HOME/Loom Demo A"
printf 'Hello from Laptop A!\n' > "$HOME/Loom Demo A/hello.txt"
```

In A's dashboard, open **Folders → Add folder**:

- Name: `Demo from A`
- Full path: the absolute path to `Loom Demo A` (for example `/Users/ayush/Loom Demo A`). Enter the expanded path, not `$HOME` or `~`.
- Device: `Laptop B`

Within a few seconds, A's **Sending folders** table should show **Synced**. B's **Received folders** table shows `Demo from A`, its local disk path, and the file list. Expand the file count and click `hello.txt` to download it, or use **Copy path** to open the folder in Finder / your file manager. The managed destination is `$HOME/.loom-two-computer/received/<folder-id>/hello.txt` on B.

While showing B's screen, edit the source on A:

```sh
printf 'Edited live on Laptop A!\n' >> "$HOME/Loom Demo A/hello.txt"
mkdir -p "$HOME/Loom Demo A/nested"
printf 'A second file.\n' > "$HOME/Loom Demo A/nested/another.txt"
```

Loom scans every two seconds and sends new or changed files automatically. Refresh or reopen the file on B to show the new contents. No **Sync now** click is needed, though that button can trigger a scan immediately.

## 5. Send a reply and show reconnection

On B, create a **different source folder** (`$HOME/Loom Demo B`) with `reply.txt`. Add it in B's **Folders**, selecting `Laptop A`. A now shows that folder under **Received folders**. Both computers can send and receive at the same time.

For a reconnection demo, stop B with Ctrl-C, edit a source file on A, then rerun B's startup command. Its peer identity and received-folder list persist. Once the connection returns, A automatically sends its pending changes. Keep the same data directory and network key across restarts.

## Current sync behavior

Each registered folder is **one-way**, from its selected source to the other computer's managed destination. Changes made inside a received folder do not travel back to the source. To demonstrate both directions, use the separate source folders described above. Shared-folder editing with conflict resolution is not implemented.

Source deletions are not propagated; copies stay on the receiver. Empty directories and symlinks are skipped. Files are limited to 100 MiB each, with a 1 GiB source-folder limit per scan. Older destination contents are backed up under the receiver's `node/history/<folder-id>/` when a changed file arrives. Stop tracking a source folder to stop sending it; this does not delete its received copies.

If startup says the provided key differs from the stored key, that data directory belongs to another network. Reuse its original key or choose a new `LOOM_DATA_DIR`. Do not delete a data directory containing files you need. If ports 3000 or 8787 are busy, stop the old Loom demo before starting this one.

For a repeatable rehearsal on one computer, run `make test-peers`. It starts two independent normal-mode processes with separate identities and checks discovery, automatic file transfer, receiver downloads, separate folders in both directions, and persistence across restarts. It does not replace testing the actual two computers' Wi-Fi and firewall setup before presenting.
