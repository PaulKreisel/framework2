package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// TranscriptText is one assistant text segment extracted from a Claude Code
// transcript JSONL. Text is the segment's `text` field; ContextTokens is the
// total context-window size computed from the same message's `usage` block
// (sum of input + cache_creation + cache_read + output tokens).
type TranscriptText struct {
	Text          string
	ContextTokens int
}

// AssistantTextSegments scans a transcript JSONL and returns every assistant
// text content block in chronological order, paired with the context-window
// size at the time that message was produced.
//
// Claude Code's transcript layout puts one content block per `assistant` row
// (the standard agent loop alternates thinking → tool_use → text → tool_use
// → … with each block on its own row), so we yield one segment per row that
// has a non-empty `text` block. Rows of other types (`tool_use`, `thinking`,
// tool_result user rows, attachment rows, …) are skipped.
//
// Malformed lines and lines without an assistant text block are silently
// ignored — the function only fails when the file itself can't be opened or
// scanned.
func AssistantTextSegments(path string) ([]TranscriptText, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Mirror the buffer ceiling used by ContextTokensFromTranscript so a
	// single fat assistant line doesn't truncate the rest.
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var out []TranscriptText
	for scanner.Scan() {
		var rec struct {
			Type    string `json:"type"`
			Message struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				Usage *struct {
					InputTokens              int `json:"input_tokens"`
					CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
					CacheReadInputTokens     int `json:"cache_read_input_tokens"`
					OutputTokens             int `json:"output_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			continue
		}
		if rec.Type != "assistant" || rec.Message.Role != "assistant" {
			continue
		}
		var tokens int
		if rec.Message.Usage != nil {
			tokens = rec.Message.Usage.InputTokens +
				rec.Message.Usage.CacheCreationInputTokens +
				rec.Message.Usage.CacheReadInputTokens +
				rec.Message.Usage.OutputTokens
		}
		for _, c := range rec.Message.Content {
			if c.Type != "text" || c.Text == "" {
				continue
			}
			out = append(out, TranscriptText{Text: c.Text, ContextTokens: tokens})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan transcript: %w", err)
	}
	return out, nil
}

// ContextTokensFromTranscript returns the total context-window size after
// the most recent assistant turn in the session transcript at path: the
// sum of input_tokens, cache_creation_input_tokens, cache_read_input_tokens,
// and output_tokens from the last `message.usage` block in the JSONL. This
// matches the number Claude Code shows in its in-session context indicator.
//
// Lines that aren't JSON or that lack a usage block are skipped — the file
// may carry user-turn rows, tool-result rows, etc. interleaved with
// assistant rows. A transcript with no usage blocks at all returns (0, nil)
// so the caller can treat "unknown" as "leave the stored value untouched".
//
// We scan the file forward (not from the tail) because Claude Code's
// transcripts are append-only and not huge in practice — buffered I/O
// already keeps this O(file-size) in a single sequential read. A reverse
// scan would need a custom seek/read-backwards loop for marginal gain.
func ContextTokensFromTranscript(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Some assistant lines (large tool-result echoes, big outputs) can
	// exceed the default 64KiB Scanner buffer. Raise the ceiling so a
	// single fat line doesn't truncate the rest of the file.
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var latest int
	for scanner.Scan() {
		line := scanner.Bytes()
		var rec struct {
			Message struct {
				Usage *struct {
					InputTokens              int `json:"input_tokens"`
					CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
					CacheReadInputTokens     int `json:"cache_read_input_tokens"`
					OutputTokens             int `json:"output_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.Message.Usage == nil {
			continue
		}
		latest = rec.Message.Usage.InputTokens +
			rec.Message.Usage.CacheCreationInputTokens +
			rec.Message.Usage.CacheReadInputTokens +
			rec.Message.Usage.OutputTokens
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan transcript: %w", err)
	}
	return latest, nil
}
