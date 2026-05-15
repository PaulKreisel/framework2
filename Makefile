BIN_DIR ?= $(HOME)/bin

.PHONY: build install test clean

build:
	go build -o bin/ticket ./cmd/ticket
	go build -o bin/this ./cmd/this
	go build -o bin/jira ./cmd/jira

install:
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/ticket ./cmd/ticket
	go build -o $(BIN_DIR)/this ./cmd/this
	go build -o $(BIN_DIR)/jira ./cmd/jira
	@echo "installed: $(BIN_DIR)/ticket"
	@echo "installed: $(BIN_DIR)/this"
	@echo "installed: $(BIN_DIR)/jira"

test:
	go test ./...

clean:
	rm -rf bin
