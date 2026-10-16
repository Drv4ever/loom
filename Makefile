.PHONY: help demo test build

help:
	@printf '%s\n' \
	  'make demo  Start the local demo dashboard and Go mesh' \
	  'make test  Run the black-box demo API smoke test' \
	  'make build Build the Go backend and production frontend'

demo:
	@./scripts/demo.sh

test:
	@python3 scripts/smoke.py

build:
	@mkdir -p bin
	@go build -o bin/loom ./cmd/loom
	@cd web && bun run build
