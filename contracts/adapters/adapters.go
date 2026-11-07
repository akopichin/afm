// Package adapters holds the public command-line provider protocol adapters.
package adapters

import "embed"

//go:embed *.sh
var Files embed.FS
