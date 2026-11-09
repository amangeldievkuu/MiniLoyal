// Package migrations embeds the goose SQL migrations so the migrate binary
// and tests need no files on disk.
package migrations

import "embed"

// FS holds every *.sql migration in this directory.
//
//go:embed *.sql
var FS embed.FS
