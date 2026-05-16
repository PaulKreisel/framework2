package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

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
