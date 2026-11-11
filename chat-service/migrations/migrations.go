// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-10-17
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package migrations embeds the ordered goose SQL migrations. The service
// never applies them (the chat-migrate job does, before the service starts);
// it embeds them so tests apply exactly the schema deployment applies, and so
// /readyz can tell when the database is older than this build expects.
package migrations

import (
	"embed"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
)

//go:embed *.sql
var FS embed.FS

// LatestVersion returns the highest migration version shipped with this build.
func LatestVersion() (int64, error) {
	names, err := fs.Glob(FS, "*.sql")
	if err != nil {
		return 0, err
	}
	var latest int64
	for _, name := range names {
		prefix, _, found := strings.Cut(name, "_")
		if !found {
			return 0, fmt.Errorf("migration %q must be named <version>_<name>.sql", name)
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("migration %q has a non-numeric version", name)
		}
		latest = max(latest, version)
	}
	return latest, nil
}
