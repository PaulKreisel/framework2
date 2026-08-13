// Package testsdisabled holds the guard that makes `go test ./...` fail loudly
// instead of silently reporting "no test files" everywhere.
//
// Tests in this repo are disabled on purpose: every `*_test.go` file carries a
// `//go:build runtests` constraint, so the default build excludes them. See
// TESTING.md ("Tests are disabled on purpose") for the why and for how to run
// them deliberately.
package testsdisabled
