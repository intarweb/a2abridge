package ideconfig

import (
	"path/filepath"
)

// cursorWriter handles Cursor's MCP config at ~/.cursor/mcp.json.
//
// Schema is identical to Claude Code's user-level mcpServers block.
type cursorWriter struct{}

func (cursorWriter) Name() string { return "Cursor" }

func (cursorWriter) Detect() string {
	h, err := homeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(h, ".cursor", "mcp.json")
}

func (w cursorWriter) Write(spec Spec, dryRun bool) Result {
	return writeJSONConfig(w.Name(), w.Detect(), dryRun, func(root map[string]any) bool {
		return setMCPServerEntry(root, spec)
	})
}
