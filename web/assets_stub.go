//go:build !webui

package webassets

import "io/fs"

// Files is nil in source-only builds; use --ui or build with -tags webui.
func Files() fs.FS { return nil }
