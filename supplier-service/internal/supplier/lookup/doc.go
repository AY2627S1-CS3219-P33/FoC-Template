// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-10-13
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package lookup serves the managed building and supplier-type lists that
// supplier writes resolve against (F2.1.2, F2.2.1). Its route registrar owns
// GET, POST /buildings and /supplier-types and DELETE of a single row.
// Reads require suppliers:read; creating and retiring rows additionally
// require suppliers:manage. Names are immutable (F2.5.1, F2.5.3), so there is
// no rename: an administrator creates a new row, moves suppliers to it and
// retires the old one.
package lookup
