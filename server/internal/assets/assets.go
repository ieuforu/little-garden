package assets

import (
	"embed"
	"io/fs"
)

//go:embed dist
var files embed.FS

func Files() fs.FS {
	sub, err := fs.Sub(files, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
