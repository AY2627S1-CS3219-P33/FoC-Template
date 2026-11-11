// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-09-29
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package supplier contains the shared supplier domain contracts.
//
// A SupplierID identifies a supplier for its whole lifetime. A VersionID
// identifies one immutable snapshot. Updating a supplier appends a snapshot;
// deleting one only removes it from current catalogue reads. Version reads are
// retained so orders can continue to display the pickup details they captured.
package supplier
