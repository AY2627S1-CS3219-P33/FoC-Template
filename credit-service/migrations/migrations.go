// Package migrations embeds the ordered Goose SQL migrations. The service does
// not apply them (infra/migrate.sh does, as the migrator role); it embeds them
// only to know which schema version it expects.
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
