# ADR-005: Double-Entry Accounting

## Status
Accepted

## Context
In financial systems, tracking available account balances using simple `UPDATE balances SET amount = amount - X` statements is prone to integrity issues. It fails to show why a balance changed, lacks auditing, and can lead to money-creation bugs.

## Decision
We implemented a strict **Double-Entry Accounting Model**.
- Every financial movement is recorded as a set of immutable `ledger_entries`.
- Every transaction contains at least one DEBIT and one CREDIT entry.
- The sum of debits and credits for any transaction must net out to zero.
- Balances are authoritative based on ledger sum, though cached in `account_balances` for fast checks.
- A continuous background validator audits the ledger table to ensure the parity invariant is maintained.

## Consequences
### Pros
- **Auditability and Correctness**: Full history of where money came from and went.
- **Error Detection**: Any imbalance in debits and credits is immediately flagged by the validator.
- **Traceability**: All adjustments, reserves, and releases are visible.

### Cons
- **Storage Overhead**: Every payment requires multiple rows in `ledger_entries` rather than a simple database update.
- **Code Complexity**: Writing correct debit/credit entries requires careful bookkeeping.
