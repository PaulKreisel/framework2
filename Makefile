BIN_DIR ?= $(HOME)/bin

.PHONY: build install test test-for-real clean

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

# Tests are DISABLED ON PURPOSE. Every *_test.go carries a "//go:build runtests"
# constraint, so the default build excludes the suite; `make test` refuses to run
# rather than pretending there is nothing to run. This is a deliberate switch —
# do not re-point this target at `go test ./...`. See TESTING.md.
test:
	@echo "make test: tests in this repo are DISABLED ON PURPOSE." >&2
	@echo "" >&2
	@echo "The suite is slow (real bd, real git repos in tmpdirs), so running it is" >&2
	@echo "a deliberate act. Nothing here is broken." >&2
	@echo "" >&2
	@echo "  make test-for-real      run the whole suite anyway" >&2
	@echo "  go test -tags runtests ./internal/agent/    run one package" >&2
	@echo "" >&2
	@echo "Do not 'fix' this by dropping the build tags. See TESTING.md." >&2
	@exit 1

# The deliberate escape hatch: enables the runtests tag so the suite compiles in.
test-for-real:
	go test -tags runtests ./...

clean:
	rm -rf bin
