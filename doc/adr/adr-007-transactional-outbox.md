# ADR-007: Transactional Outbox with SKIP LOCKED

## Status
Accepted

## Context
When settling payments, we must publish notifications to Redis Streams for downstream processing. Doing this inside the database transaction causes slow connection holding, whereas doing it after transaction commit risks losing the notification on app crash (split-brain).

## Decision
We implemented the **Transactional Outbox Pattern** using PostgreSQL `SKIP LOCKED`.
- Every state transition inserts a notification task into `outbox_events` within the same DB transaction.
- A background worker pool selects pending tasks using:
  `SELECT ... FROM outbox_events WHERE status = 'PENDING' FOR UPDATE SKIP LOCKED LIMIT N`
- Workers publish payloads to Redis Streams outside the DB transaction.
- Failed tasks are retried with jittered backoff and eventually routed to a Dead Letter Queue (DLQ).

## Consequences
### Pros
- **Atomic Execution**: Guaranteed event publication if and only if the database transaction commits.
- **High Concurrency**: `SKIP LOCKED` prevents parallel worker threads from blocking or contesting the same outbox rows.
- **Resource Hygiene**: Doing Redis I/O outside database transactions avoids connection pool starvation.

### Cons
- **Polling Delay**: Event publishing is subject to the background worker loop interval (e.g. 200ms).
- **Database Bloat**: The outbox table requires periodic pruning of published rows to avoid index degradation.
