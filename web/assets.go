//go:build webui

// Package webassets provides the UI embedded in distribution builds.
package webassets

import (
	"embed"
	"io/fs"
)

//go:embed dist
var bundled embed.FS

func Files() fs.FS { f, _ := fs.Sub(bundled, "dist"); return f }
