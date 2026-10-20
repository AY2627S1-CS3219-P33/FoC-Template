//go:build tools

// Package deps pins the libraries frozen by the S0 shared baseline that no
// S0 package imports yet, so `go mod tidy` keeps them in go.mod. The tools
// build tag keeps this file out of every build. Remove an import once a
// feature package imports that library for real.
package deps

import (
	_ "github.com/jackc/pgx/v5"
	_ "github.com/redis/go-redis/v9"
	_ "golang.org/x/sync/singleflight"
)
