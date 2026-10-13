// Package lookup serves the managed building and supplier-type lists that
// supplier writes resolve against (F2.1.2, F2.2.1). Its route registrar owns
// GET, POST /buildings and /supplier-types and DELETE of a single row.
// Reads require suppliers:read; creating and retiring rows additionally
// require suppliers:manage. Names are immutable (F2.5.1, F2.5.3), so there is
// no rename: an administrator creates a new row, moves suppliers to it and
// retires the old one.
package lookup
