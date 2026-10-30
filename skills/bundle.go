// Package skills contains the version-matched agent guidance shipped with Docket.
package skills

import "embed"

// Docket embeds the portable skill folder so CLI discovery never depends on the working directory.
//
//go:embed docket-cli/SKILL.md docket-cli/references/*.md
var Docket embed.FS
