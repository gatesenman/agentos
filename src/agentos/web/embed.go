// Package web embeds the static web UI (web/static). Plain ES modules, no
// build step. Owned by module M6 except files listed for M4/M5 in docs/modules.md.
package web

import (
	"embed"
	"io/fs"
	"os"
)

//go:embed all:static
var static embed.FS

// FS returns the UI root (index.html at top level). For UI development set
// AGENTOS_WEB_DIR=/path/to/web/static to serve files from disk without rebuilding.
func FS() fs.FS {
	if dir := os.Getenv("AGENTOS_WEB_DIR"); dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return os.DirFS(dir)
		}
	}
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
