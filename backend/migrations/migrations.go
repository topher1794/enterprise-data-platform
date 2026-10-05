// Package migrations embeds the SQL migration files so the compiled binary is
// self-contained and does not depend on files being present on disk.
package migrations

import "embed"

// FS holds every {version}_{title}.{up,down}.sql file in this directory.
//
//go:embed *.sql
var FS embed.FS
