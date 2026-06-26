# ADR-008: Saga Pattern for Distributed Payments

## Status
Accepted

## Context
Payment settlement requires multiple steps: reserving funds, validating nonces, consuming tokens, committing ledger balances, and generating outbox notifications. If a step fails, the system must not be left in an inconsistent state.

## Decision
We implemented a **Saga Orchestrator** in `internal/saga/saga.go` using a choreography/orchestration hybrid:
- The orchestrator executes steps sequentially: `RESERVE` -> `CONSUME_TOKEN` -> `COMMIT_LEDGER` -> `PUBLISH_OUTBOX`.
- State transitions are committed to `saga_states`.
- If any step fails (e.g. double-spend check fails during token consumption), the orchestrator triggers compensating actions in reverse order: `RELEASE_RESERVATION` and `MARK_FAILED`.

## Consequences
### Pros
- **Atomic-like Consistency**: Guarantees that either all steps complete, or the system compensates back to the initial state.
- **Fail-safe Recovery**: The saga state table allows recovering in-flight sagas on server crashes.
- **Clear Auditing**: We can trace the exact execution path of a saga.

### Cons
- **Increased Latency**: Executing and recording saga step states adds latency.
- **Complexity of Compensations**: Designing correct, idempotent compensation handlers requires careful design (e.g. ensuring reservations are released exactly once).
