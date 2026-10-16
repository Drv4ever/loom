.PHONY: help demo test test-peers check build

help:
	@printf '%s\n' \
	  'make demo  Start the local demo dashboard and Go mesh' \
	  'make test  Run the black-box demo API smoke test' \
	  'make test-peers Run the normal-mode two-peer integration test' \
	  'make check Run Go, frontend, demo, and normal-mode checks' \
	  'make build Build the Go backend and production frontend'

demo:
	@./scripts/demo.sh

test:
	@python3 scripts/smoke.py

test-peers:
	@python3 scripts/peer_smoke.py

check:
	@go test ./cmd/... ./internal/...
	@go vet ./cmd/... ./internal/...
	@cd web && bun run typecheck
	@cd web && bun run lint
	@mkdir -p bin
	@go build -o bin/loom ./cmd/loom
	@python3 scripts/smoke.py --binary bin/loom
	@python3 scripts/peer_smoke.py --binary bin/loom

build:
	@mkdir -p bin
	@go build -o bin/loom ./cmd/loom
	@cd web && bun run build
