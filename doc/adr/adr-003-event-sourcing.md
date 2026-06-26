# ADR-003: Event Sourcing for Payment Aggregates

## Status
Accepted

## Context
Traditional state-oriented database designs only keep the current state of a payment. However, for auditing, reconciliation, and resolving race conditions or byzantine behaviors, OfflinePay needs a detailed, immutable log of every status transition.

## Decision
We implemented **Event Sourcing** for payment aggregates.
- Every payment lifecycle transition (e.g., CREATED, RESERVED, SETTLED, REJECTED, DUPLICATE) is recorded as a versioned event row in `payment_events`.
- Aggregates are hydrated by replaying their event history.
- To optimize read performance, we cache aggregate snapshots in `payment_snapshots` every 5 events. Hydration loads the latest snapshot and replays only newer events.

## Consequences
### Pros
- **Auditability**: Complete, immutable history of all payment states.
- **Time Travel**: Ability to reconstruct the system state at any point in time.
- **Strong Version Check**: Enforcing sequential `event_version` increments protects against concurrent update conflicts.

### Cons
- **Write Amplification**: Every update requires inserting a new event row.
- **Snapshot Complexity**: Managing snapshot versioning and storage overhead adds system complexity.
