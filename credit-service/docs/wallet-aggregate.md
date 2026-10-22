# Wallet aggregate

Status: implemented so far are the schema (`migrations/00002`, `00003`), the data-access layer (`internal/database/walletrepo`), wallet creation with the initial allocation, and balance reads (`internal/wallet`, `internal/walletapi`). The order commands (reserve, adjust, release, transfer) are implemented in `internal/wallet/commands.go` as plain functions; the RabbitMQ consumer and the result outbox are not built yet.

## Source flows

From the event storming board (`docs/CS3219.drawio.xml` at the repo root):

| Trigger | Command | Outcome |
|---|---|---|
| New user | Allocate initial credit | credits added to the wallet (NFR7.3 requires idempotency) |
| Order validated | Reserve credits | `CreditReserved` success, or failed with insufficient credits |
| Order edit changes the amount | Update credit reservation | same success and failure outcomes |
| Order cancelled or expired | Refund credits | `CreditRefunded` |
| Delivery completed | Transfer credit | reserved credit moves to the courier: `CreditTransferred` |
| Any time | Check balance | read only |

## Decisions

- **Credits are whole numbers** (`bigint`). APIs serialise them as strings.
- **One wallet per user**, identified by the user-service account UUID (`owner_id`).
- **Reservations live inside the Wallet aggregate**, one per order.
- **Wallets are created over HTTP** by user-service (`PUT /internal/wallets/{ownerId}`, machine token), idempotent per owner. **Order operations (reserve, adjust, release, transfer) arrive as RabbitMQ events**, authenticated by broker credentials and permissions; messages never carry tokens.
- **The initial allocation amount is configuration** (`INITIAL_CREDIT_AMOUNT`) and is recorded once as a ledger entry with the fixed key `initial-allocation`. Callers cannot choose it.
- **Each wallet stores the owner's Auth0 subject** (supplied at creation, unique, never changed), so a user reads their own wallet by token subject with no call to user-service.
- **A transfer is one database transaction** that locks both wallets, in wallet-ID order via `LockWalletsByOwner`, to avoid deadlocks. There is no eventual consistency between the two wallets.

## Order commands

`Reserve`, `Adjust`, `Release` and `Transfer` take the message ID of the command and the Auth0 subjects of the users (order-service does not know owner UUIDs). Each runs in one transaction and returns a `Result` whose `Type` is the routing key of the result message.

- **Idempotent by message ID.** The result of every command is stored in `processed_commands` in the same transaction as the balance change. A repeat returns the stored result, including a stored refusal.
- **Refusal vs error.** A business refusal (not enough credits, reservation not active) is a `Result`. An error (`ErrInvalid`, `ErrConflict`, database failure) means the command could not be judged; the consumer should reject it or retry.
- **Release tombstone.** A release for an order with no reservation writes `released_orders`. A later reserve for that order is refused with `RESERVATION_RELEASED`.
- **Adjust ordering.** `reservations.last_adjust_seq` holds the newest applied seq. An adjust with an older or equal seq returns the current amount and changes nothing. A refused adjust does not use its seq.
- **Transfer amount.** The command amount must equal the reservation amount, otherwise the result is `credit.operation_failed` with `AMOUNT_MISMATCH`.
- `RESERVATION_NOT_FOUND` and `AMOUNT_MISMATCH` appear only in `credit.operation_failed`; they are not in the `OperationFailedData` list of `docs/asyncapi.yaml`.

## Model

```
Wallet (root)  available, reserved, version
 ├── Reservation   one per order: amount, status ACTIVE | RELEASED | TRANSFERRED
 └── LedgerEntry   append-only: entry_type, available_delta, reserved_delta,
                   order_id, counterparty_wallet_id, idempotency_key
```

## Invariants

1. `available >= 0` and `reserved >= 0`, enforced by CHECK constraints as a last line of defence.
2. `reserved` equals the sum of `ACTIVE` reservation amounts.
3. At most one reservation per order (unique `order_id`); an edit changes the existing row.
4. Credits are conserved: nothing creates or destroys them except the initial allocation. A transfer writes `TRANSFER_OUT` and `TRANSFER_IN` entries that cancel out across wallets.
5. Each logical operation applies at most once: `(wallet_id, idempotency_key)` is unique.
6. For every wallet, `available = SUM(available_delta)` and `reserved = SUM(reserved_delta)` over its ledger entries.
7. `ledger_entries` rows are never updated or deleted (trigger-enforced).

## How an operation is written

One transaction (`pgx.BeginFunc`): lock the wallet or wallets, check the rule, insert the ledger entry (an `ErrDuplicateOperation` means the operation already happened, so replay its result with `GetLedgerEntryByKey`), apply the deltas, and change the reservation. The repository functions enforce no business rules; the rules above are checked by the domain code before it writes.

## Open questions

- What user-service does when wallet creation fails at signup (retry or reconcile).
- Existing users created before credit-service need their wallets created (a one-off backfill).
- What happens to an `ACTIVE` reservation when an order expires: refund is assumed.
- Whether `order_id` is a UUID in order-service.
- RabbitMQ topology, per-service permissions, and the transactional outbox for result events.
- Whether order-service accepts the two extra `credit.operation_failed` codes.
