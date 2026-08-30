GO ?= go
BIN := bin

.PHONY: all build vet test e2e clean

all: vet test build

build:
	mkdir -p $(BIN)
	$(GO) build -o $(BIN)/works        ./cmd/works
	$(GO) build -o $(BIN)/works-worker ./cmd/works-worker

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

e2e: build
	$(GO) test -tags=e2e ./e2e/...

clean:
	rm -rf $(BIN)
