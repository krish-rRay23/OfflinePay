# ADR-009: Reservation-Based Financial Settlement

## Status
Accepted

## Context
Direct settlements like `DEBIT -> CREDIT` directly run the risk of overdrafts or balance locking if network or downstream operations fail during the debit phase. We need a way to secure funds before committing the actual transfer.

## Decision
We implemented a **Reservation-Based Financial Flow**.
- When an offline token is issued, its value is immediately moved from the user's `available_balance` to their `reserved_balance`.
- When the payment intent settles, the settled amount is debited from the sender's `reserved_balance`, and credited to the receiver's `available_balance`.
- Any remaining value (change return) is moved back from the sender's `reserved_balance` to their `available_balance`.
- If a token expires without being settled, a background reconciliation loop automatically releases all reserved funds.

## Consequences
### Pros
- **Overdraft Protection**: Once a token is issued, its funds are locked, ensuring they cannot be spent elsewhere while offline.
- **Atomic Commit**: Since funds are already reserved, settlement commits are guaranteed not to fail due to insufficient available balances.
- **Accurate Auditing**: Clear distinction between locked capital and spendable funds.

### Cons
- **Reduced Capital Efficiency**: Users cannot access reserved funds until tokens expire or settle.
