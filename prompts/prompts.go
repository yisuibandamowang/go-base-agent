package prompts

import "embed"

// FS contains embedded prompt template files.
//
//go:embed *.txt
var FS embed.FS
