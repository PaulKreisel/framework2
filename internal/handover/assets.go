package handover

import "embed"

// Assets bundles the static review aids shipped alongside the JSONL snapshot.
// They explain to a reviewer how to load the handover into a fresh beads DB
// and run the cross-reference scripts.
//
//go:embed assets/conversation.py
//go:embed assets/handover_instructions.md
//go:embed assets/overview.py
var Assets embed.FS

// AssetNames lists the embedded files in stable order. Used by the Jira
// uploader to derive attachment filenames.
var AssetNames = []string{
	"handover_instructions.md",
	"conversation.py",
	"overview.py",
}
