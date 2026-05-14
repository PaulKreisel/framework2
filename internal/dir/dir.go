// Package dir implements the `ticket dir` subcommand group.
package dir

import (
	"encoding/json"
	"fmt"
	"io"
)

// Entry is one entry in the ticket's dirs metadata.
type Entry struct {
	Path      string `json:"path"`
	Repo      string `json:"repo,omitempty"`
	LocalRepo string `json:"localRepo,omitempty"`
	Branch    string `json:"branch,omitempty"`
}

// Link appends an entry for path to the ticket's dirs metadata.
func Link(db, ticketID, path string) error {
	entries, err := load(db, ticketID)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Path == path {
			return fmt.Errorf("path already linked to %s: %s", ticketID, path)
		}
	}
	entry, err := buildEntry(path)
	if err != nil {
		return err
	}
	entries = append(entries, *entry)
	return save(db, ticketID, entries)
}

// Remove drops the entry whose path matches the argument.
func Remove(db, ticketID, path string) error {
	entries, err := load(db, ticketID)
	if err != nil {
		return err
	}
	found := -1
	for i, e := range entries {
		if e.Path == path {
			found = i
			break
		}
	}
	if found < 0 {
		return fmt.Errorf("path not linked to %s: %s", ticketID, path)
	}
	entries = append(entries[:found], entries[found+1:]...)
	return save(db, ticketID, entries)
}

// List writes the ticket's dir entries as indented JSON.
func List(db, ticketID string, out io.Writer) error {
	entries, err := load(db, ticketID)
	if err != nil {
		return err
	}
	if entries == nil {
		entries = []Entry{}
	}
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(b))
	return err
}

func buildEntry(path string) (*Entry, error) {
	e := &Entry{Path: path}
	top, ok := gitToplevel(path)
	if !ok {
		return e, nil
	}
	branch, ok := gitBranch(path)
	if !ok || branch == "HEAD" {
		return nil, fmt.Errorf("path is on a detached HEAD (or branch unreadable): %s", path)
	}
	e.Branch = branch
	if remote, ok := gitRemote(path); ok {
		e.Repo = remote
	} else {
		e.LocalRepo = top
	}
	return e, nil
}
