// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package exportapi serves the read-only export routes admin-service uses to
// build and refresh its read model (docs/contracts.md, "Export routes").
//
// Routes (each needs a service token with orders:admin-read, through
// serviceauth.RequireService):
//
//   - GET /internal/export/transitions?after=&limit=
//   - GET /internal/export/errands?after=&limit=
//   - GET /internal/export/errands?ids=a,b,c
//
// The responses hold IDs, statuses, times, rewards and names only: never
// contact details, items, notes or free-text reasons.
package exportapi
