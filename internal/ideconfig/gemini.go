package ideconfig

import (
	"path/filepath"
)

// geminiWriter handles Gemini CLI's settings file at
// ~/.gemini/settings.json. Schema mirrors Claude Code / Cursor — same
// mcpServers block.
type geminiWriter struct{}

func (geminiWriter) Name() string { return "Gemini CLI" }

func (geminiWriter) Detect() string {
	h, err := homeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(h, ".gemini", "settings.json")
}

func (w geminiWriter) Write(spec Spec, dryRun bool) Result {
	return writeJSONConfig(w.Name(), w.Detect(), dryRun, func(root map[string]any) bool {
		return setMCPServerEntry(root, spec)
	})
}
