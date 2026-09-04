# ADR-011: Bounded Recovery Has No Financial Authority

## Status
Accepted.

## Context
OfflinePay already has an authoritative settlement path: cryptographic validation, replay protection, risk evaluation, Saga orchestration, a double-entry ledger, event sourcing, and the transactional outbox. Recovery must improve fault tolerance without creating a second payment path.

## Decision
Failures are classified rules-first. An optional LLM may classify only otherwise ambiguous text and must return a known category plus a confidence in `[0,1]`. Confidence below `0.70`, malformed LLM output, security failures, and consistency failures do not initiate a retry.

The deterministic policy enforces three attempts, a `₹50,000` (5,000,000 minor-unit) cap, exponential cooldown, and a 24-hour maximum recovery period. A unique `recovery:<txn_id>:<action>` key is written in `recovery_operations` before a recovery request enters the transactional outbox. Workers claim due operations using `FOR UPDATE SKIP LOCKED`.

Recovery emits only `RecoveryRetryRequested` or `RecoveryCompensationReviewRequested` outbox events. It never calls an external bank API and never writes balances, ledger entries, tokens, or nonce rows. Any retry must re-enter the normal relay/settlement path and therefore repeats all existing security, risk, idempotency, Saga, and ledger controls.

## Consequences
This adds bounded diagnosis and durable scheduling while preserving a single financial authority. A successful outbox delivery is not a settlement confirmation; reconciliation remains authoritative.
