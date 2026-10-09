// Package db compiles every service's migrations into its binary, so an image
// always carries exactly the schema its code was written for.
package db

import (
	"embed"
	"io/fs"
	"path"
)

//go:embed */migrations/*.sql
var files embed.FS

// Migrations returns one service's migrations, from db/<service>/migrations.
func Migrations(service string) fs.FS {
	sub, err := fs.Sub(files, path.Join(service, "migrations"))
	if err != nil {
		panic(err) // only for a name that isn't a valid path
	}
	return sub
}
