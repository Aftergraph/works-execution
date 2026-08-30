GO ?= go
BIN := bin

.PHONY: all build vet test e2e kanban kanban-validate standards standards-validate clean

all: vet test build

build:
	mkdir -p $(BIN)
	$(GO) build -o $(BIN)/works           ./cmd/works
	$(GO) build -o $(BIN)/works-api      ./cmd/works-api
	$(GO) build -o $(BIN)/works-worker   ./cmd/works-worker
	$(GO) build -o $(BIN)/works-kanban   ./cmd/works-kanban
	$(GO) build -o $(BIN)/works-standards ./cmd/works-standards

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

clean:
	rm -rf $(BIN)
