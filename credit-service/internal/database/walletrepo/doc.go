// Package walletrepo holds the SQL data access for wallets, reservations and
// the ledger. Every function takes a DBTX, so domain operations can compose
// several of them inside one transaction (pgx.BeginFunc) and lock the wallets
// they touch with LockWalletsByAuth0Subject first. The package enforces no business
// rules; it only maps database constraint violations to typed errors.
package walletrepo
