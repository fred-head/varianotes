package web

import "embed"

// Files contains the production frontend. Docker replaces the placeholder with
// the Vite build before compiling the application binary.
//
//go:embed dist
var Files embed.FS
