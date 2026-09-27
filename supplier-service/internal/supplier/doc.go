// Package supplier contains the shared supplier domain contracts.
//
// A SupplierID identifies a supplier for its whole lifetime. A VersionID
// identifies one immutable snapshot. Updating a supplier appends a snapshot;
// deleting one only removes it from current catalogue reads. Version reads are
// retained so orders can continue to display the pickup details they captured.
package supplier
