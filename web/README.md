# Loom web dashboard

The dashboard runs on Bun and talks to the local Loom API at `127.0.0.1:8787`. Vite proxies both `/api` and `/proxy` for development and preview, so the browser only needs to reach the dashboard origin.

From the repository root, start the demo API:

```sh
go run ./cmd/loom --demo --listen 127.0.0.1:8787 --data-dir .loom-demo
```

In another terminal, start the dashboard:

```sh
cd web
bun install
bun run dev
```

Open `http://127.0.0.1:3000`. The dashboard polls `/api/state` every two seconds. Its “Local demo” marker and sample file action are shown only when the API reports demo mode.

## Production preview

```sh
cd web
bun install
bun run build
bun run start
```

`bun run start` serves the production build on `127.0.0.1:3000` and uses the same API and service proxy as development. Keep the Loom API running on `127.0.0.1:8787`.

## Checks

```sh
bun run typecheck
bun run lint
bun run build
```
