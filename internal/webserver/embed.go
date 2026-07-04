package webserver

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// assetsFS returns the built SPA rooted at its dist directory. The embed always
// contains at least dist/index.html, so fs.Sub cannot fail.
func assetsFS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
