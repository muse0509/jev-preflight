// Package policies embeds the versioned default policy for release binaries.
package policies

import _ "embed"

// Default is the source-controlled policy document.
//
//go:embed default.json
var Default string
