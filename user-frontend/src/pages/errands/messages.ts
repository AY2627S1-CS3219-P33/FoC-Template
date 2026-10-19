// Turns the Order Service's stable error codes into something a student can act
// on. The codes come from order-service/openapi/common.yaml Error; anything
// unmapped falls through to the service's own message.

import { OrderError } from "../../api/order";

export function errandError(err: unknown): string {
  if (!(err instanceof OrderError)) {
    // A network or parse failure usually means the order backend is not running.
    return "Could not reach the order service.";
  }
  switch (err.code) {
    case "UNAUTHENTICATED":
      return "Please sign in again to continue.";
    case "FORBIDDEN":
      return "You are not allowed to do that.";
    case "ERRAND_NOT_FOUND":
      return "This errand no longer exists, or you can no longer see it.";
    case "ERRAND_CHANGED":
      return "This errand changed while you were looking at it. It has been reloaded — please try again.";
    case "ERRAND_UNAVAILABLE":
      return "Someone got there first: this errand has already moved on.";
    case "INVALID_TRANSITION":
      return "That action is not possible from the errand's current state.";
    case "NOT_ELIGIBLE":
      return "You cannot accept this errand — you requested it, or you withdrew from it before.";
    case "COURIER_RESTRICTED":
      return "You are temporarily restricted from accepting errands after repeated withdrawals.";
    case "INSUFFICIENT_CREDITS":
      return "You do not have enough available credits for this reward.";
    case "EDIT_PENDING":
      return "A reward change is still being confirmed. Wait for it to finish, then edit again.";
    case "SUPPLIER_UNAVAILABLE":
      return "That pickup location is closed right now, or no longer exists.";
    case "SUPPLIER_DELETION_IN_PROGRESS":
      return "That pickup location is being removed and cannot take new errands.";
    case "DEPENDENCY_UNAVAILABLE":
      return "The order service is temporarily unavailable. Please try again shortly.";
    case "VALIDATION_FAILED":
      // Field messages are shown on the fields themselves; this is the fallback.
      return err.fields.length > 0 ? "Please correct the highlighted fields." : err.message;
    default:
      return err.message;
  }
}

// A stale expectedVersion is recoverable by reloading, so callers refresh the
// errand instead of only showing the message.
export function isStale(err: unknown): boolean {
  return err instanceof OrderError && err.code === "ERRAND_CHANGED";
}
