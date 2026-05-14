BIN_DIR ?= $(HOME)/bin

.PHONY: build install test clean

build:
	go build -o bin/ticket ./cmd/ticket

install:
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/ticket ./cmd/ticket
	@echo "installed: $(BIN_DIR)/ticket"

test:
	go test ./...

clean:
	rm -rf bin
