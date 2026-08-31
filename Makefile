GO ?= go
BIN := bin

.PHONY: all build vet test e2e kanban kanban-validate standards standards-validate sbom clean

all: vet test build

build:
	mkdir -p $(BIN)
	$(GO) build -o $(BIN)/works           ./cmd/works
	$(GO) build -o $(BIN)/works-api      ./cmd/works-api
	$(GO) build -o $(BIN)/works-worker   ./cmd/works-worker
	$(GO) build -o $(BIN)/works-kanban   ./cmd/works-kanban
	$(GO) build -o $(BIN)/works-standards ./cmd/works-standards
	$(GO) build -o $(BIN)/works-sbom     ./cmd/works-sbom
	$(GO) build -o $(BIN)/works-pilot    ./cmd/works-pilot
	$(GO) build -o $(BIN)/works-runner-id ./cmd/works-runner-id
	$(GO) build -o $(BIN)/works-publisher ./cmd/works-publisher
	$(GO) build -o $(BIN)/works-bench    ./cmd/works-bench

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

e2e: build
	$(GO) test -tags=e2e ./e2e/...

# Standards governance (slice 3)
standards: standards-validate
	./bin/works-standards summary
	./bin/works-standards gaps | head -40

standards-validate:
	$(GO) test ./internal/standards/...
	./bin/works-standards list > /dev/null
	./bin/works-kanban validate

kanban:
	./bin/works-kanban summary

kanban-validate:
	./bin/works-kanban validate

# SBOM emission (slice 3) — SPDX 3.0.1 + CycloneDX 1.6 from the Go
# module graph. Output goes to artifacts/sbom/.
#   SPDX:     artifacts/sbom/<sanitized-name>.spdx.json
#   CycloneDX: artifacts/sbom/<sanitized-name>.cdx.json
# Both files are self-verified (JSON parse + spec discriminator) before
# the target exits 0. See services/sbom/ and tests/supply_chain/.
sbom:
	$(GO) build -o $(BIN)/works-sbom ./cmd/works-sbom
	./bin/works-sbom --out artifacts/sbom

clean:
	rm -rf $(BIN)
