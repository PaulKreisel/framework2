BIN_DIR ?= $(HOME)/bin

.PHONY: build install test clean

build:
	go build -o bin/bdir ./cmd/bdir

install:
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/bdir ./cmd/bdir
	@echo "installed: $(BIN_DIR)/bdir"

test:
	go test ./...

clean:
	rm -rf bin
