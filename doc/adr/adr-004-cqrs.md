# ADR-004: Command Query Responsibility Segregation (CQRS)

## Status
Accepted

## Context
hydration of payment aggregates from versioned event streams can become slow as the volume of events grows. High-performance querying of payments (e.g., status dashboards, merchant reporting) should not compete with critical write operations or load payment event streams continuously.

## Decision
We implemented a **CQRS pattern**.
- **Write Path (Commands)**: Writes happen against the event store (`payment_events`).
- **Read Path (Queries)**: A dedicated projections read model (`payment_read_projections`) is maintained.
- An outbox subscriber receives events and updates the projection table asynchronously. All query routes read directly from this projection table.

## Consequences
### Pros
- **Optimized Reads**: Queries are extremely fast and do not require replaying event streams or complex SQL joins.
- **Separation of Concerns**: Write logic (Sagas, validations) is separate from read models.
- **Independent Scaling**: Read capacity can be scaled separately from write capacity.

### Cons
- **Eventual Consistency**: There is a minor lag between the transaction settling and the read projection updating.
- **Complexity**: Multiple schemas and tables must be kept in sync.
