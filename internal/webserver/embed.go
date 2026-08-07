package webserver

import (
	"embed"
	"io/fs"
	"strings"
	"sync"
)

//go:embed all:dist
var distFS embed.FS

// webBuild is the `g<sha>` stamp Vite bakes into the bundle, emitted beside the
// assets so the hub can answer it on /api/v1/health without parsing the bundle.
// A dist built before the stamp existed reads empty, which the client treats as
// unknown rather than as a mismatch.
var webBuild = sync.OnceValue(func() string {
	data, err := distFS.ReadFile("dist/web-build")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
})

// assetsFS returns the built SPA rooted at its dist directory. The embed always
// contains at least dist/index.html, so fs.Sub cannot fail.
func assetsFS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
