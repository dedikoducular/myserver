// Package migrations embeds the SQL schema migrations. Files are applied in
// lexical order; each module adds its own numbered file.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
