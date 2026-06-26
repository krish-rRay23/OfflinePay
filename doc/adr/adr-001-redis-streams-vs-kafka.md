# ADR-001: Redis Streams vs Apache Kafka

## Status
Accepted

## Context
OfflinePay requires a lightweight, fast, and durable message broker to publish transaction state changes, process outbox events, and trigger downstream projections. We evaluated both Redis Streams and Apache Kafka for this purpose.

## Decision
We chose **Redis Streams** as our primary message broker for the v2 MVP.

## Consequences
### Pros
- **Operational Simplicity**: Redis is already utilized for cache and risk limits; running Redis Streams does not require additional infrastructure or JVM overhead like Apache Kafka.
- **Sub-millisecond Latency**: Redis's in-memory model provides extremely low latency.
- **Built-in Deduplication and Consumer Groups**: Redis Streams supports consumer groups similar to Kafka.

### Cons
- **Storage Constraints**: As an in-memory database, Redis streams are bound by RAM limits unless trimmed (`XADD ... MAXLEN`).
- **Lighter Durability Guarantees**: While Redis supports AOF (Append Only File), Apache Kafka is fundamentally built for disk-backed distributed event logging with stronger durability. We mitigate this by using a Postgres Transactional Outbox as the single source of truth for events.
