// Package migrations embeds the ordered Goose SQL migrations so database
// tests apply exactly the schema that deployment applies. Goose itself reads
// only the numbered .sql files in this directory.
package migrations

import (
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

//go:embed *.sql
var files embed.FS

const (
	upMarker   = "-- +goose Up"
	downMarker = "-- +goose Down"
)

// Names returns the migration file names in version order.
func Names() ([]string, error) {
	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	return names, nil
}

// Section returns the Up or Down section of one migration file.
func Section(name, section string) (string, error) {
	contents, err := files.ReadFile(name)
	if err != nil {
		return "", err
	}
	migration := string(contents)
	upAt := strings.Index(migration, upMarker)
	downAt := strings.Index(migration, downMarker)
	if upAt < 0 || downAt <= upAt {
		return "", fmt.Errorf("%s is missing ordered Goose Up and Down markers", name)
	}
	switch section {
	case "Up":
		return migration[upAt+len(upMarker) : downAt], nil
	case "Down":
		return migration[downAt+len(downMarker):], nil
	}
	return "", fmt.Errorf("unknown migration section %q", section)
}

// Up returns every Up section in version order as one SQL script.
func Up() (string, error) {
	names, err := Names()
	if err != nil {
		return "", err
	}
	return join(names, "Up")
}

// Down returns every Down section in reverse version order as one SQL script.
func Down() (string, error) {
	names, err := Names()
	if err != nil {
		return "", err
	}
	slices.Reverse(names)
	return join(names, "Down")
}

func join(names []string, section string) (string, error) {
	var script strings.Builder
	for _, name := range names {
		sql, err := Section(name, section)
		if err != nil {
			return "", err
		}
		script.WriteString(sql)
		script.WriteString("\n")
	}
	return script.String(), nil
}
