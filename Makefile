BIN_DIR ?= $(HOME)/bin

.PHONY: build install test clean

build:
	go build -o bin/ticket ./cmd/ticket
	go build -o bin/this ./cmd/this
	go build -o bin/jira ./cmd/jira
	go build -o bin/github ./cmd/github

install:
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/ticket ./cmd/ticket
	go build -o $(BIN_DIR)/this ./cmd/this
	go build -o $(BIN_DIR)/jira ./cmd/jira
	go build -o $(BIN_DIR)/github ./cmd/github
	@echo "installed: $(BIN_DIR)/ticket"
	@echo "installed: $(BIN_DIR)/this"
	@echo "installed: $(BIN_DIR)/jira"
	@echo "installed: $(BIN_DIR)/github"

test:
	go test ./...

clean:
	rm -rf bin
