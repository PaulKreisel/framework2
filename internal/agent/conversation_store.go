package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"framework2/internal/project"
)

// Conversation history is stored outside the bead, in append-only JSONL logs
// under <BEADS_DIR>/conversations/<beadId>/<sessionId>.jsonl — one line per
// [ConvEntry]. It deliberately lives here rather than in metadata.agents:
// bd snapshots the entire bead into its event store on every metadata write,
// and an unbounded conversation eventually overflows that store (the
// "Error 1105 … string is too long" failure that motivated ai-vrc.80). Keeping
// the transcript out of the bead caps metadata.agents at a handful of scalar
// fields per session, so writes stay small regardless of how long a session
// runs.
//
// The logs are never trimmed or deleted by the framework — `agent delete`
// removes the metadata record but leaves the JSONL behind. They are removed
// only by hand.
const conversationsDirName = "conversations"

// conversationDir returns <beadsDir>/conversations/<ticketID>. An empty
// beadsDir falls back to the process-wide BEADS_DIR (mirroring how the bd CLI
// callers treat an empty beadsDir as "inherit from the environment").
func conversationDir(beadsDir, ticketID string) string {
	if beadsDir == "" {
		beadsDir = project.Dir()
	}
	return project.Path(beadsDir, filepath.Join(conversationsDirName, ticketID))
}

// conversationFile returns the JSONL log path for one session.
func conversationFile(beadsDir, ticketID, sessionID string) string {
	return filepath.Join(conversationDir(beadsDir, ticketID), sessionID+".jsonl")
}

// loadConversation reads the append-only JSONL log for one session and decodes
// it into the in-memory [ConvEntry] slice. A missing file yields (nil, nil) —
// a session that has not produced any conversation yet. Malformed lines are
// skipped rather than failing the read, matching the transcript scanners'
// tolerance: a single corrupt line must not blank out the whole history.
func loadConversation(beadsDir, ticketID, sessionID string) ([]ConvEntry, error) {
	f, err := os.Open(conversationFile(beadsDir, ticketID, sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open conversation log for %s: %w", sessionID, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// A single entry can be large (a long assistant reply or a plan markdown
	// blob), so raise the line ceiling well past the 64KiB default — same
	// rationale as the transcript scanners.
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var entries []ConvEntry
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e ConvEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan conversation log for %s: %w", sessionID, err)
	}
	return entries, nil
}

// appendConversation appends entries to the session's JSONL log, creating the
// per-bead directory and the file on first write. It is a no-op for an empty
// slice. Each entry is written as one compact JSON line.
func appendConversation(beadsDir, ticketID, sessionID string, entries []ConvEntry) error {
	if len(entries) == 0 {
		return nil
	}
	dir := conversationDir(beadsDir, ticketID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create conversation dir for %s: %w", ticketID, err)
	}
	f, err := os.OpenFile(conversationFile(beadsDir, ticketID, sessionID),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open conversation log for %s: %w", sessionID, err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("encode conversation entry for %s: %w", sessionID, err)
		}
		if _, err := w.Write(b); err != nil {
			return fmt.Errorf("write conversation entry for %s: %w", sessionID, err)
		}
		if err := w.WriteByte('\n'); err != nil {
			return fmt.Errorf("write conversation entry for %s: %w", sessionID, err)
		}
	}
	return w.Flush()
}
