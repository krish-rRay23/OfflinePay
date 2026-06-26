# Runbook: Dead Letter Queue (DLQ) Event Processing

## Context
When the transactional outbox worker fails to publish a state change event to Redis Streams (e.g. due to persistent Redis timeouts), it retries up to 5 times. If all attempts fail, it routes the event to `dead_letter_events` to avoid blocking other transactions.

## Diagnostics
1. **List DLQ Items**:
   Use the API server endpoint to view pending DLQ events:
   ```bash
   curl -X GET http://localhost:8080/api/dlq
   ```
   Or query the database:
   ```sql
   SELECT event_id, failure_reason, retry_count, timestamp FROM dead_letter_events;
   ```
2. **Review Failures**:
   Analyze the `failure_reason` column to identify if the issue is network-related, serialization-related, or due to a Redis broker crash.

## Remediation Steps
1. **Replay the Event**:
   Once the downstream broker or Redis stream issue is resolved, trigger a manual replay of the DLQ event by ID:
   ```bash
   curl -X POST http://localhost:8080/api/dlq/replay -H "Content-Type: application/json" -d '{"event_id": <event_id>}'
   ```
   This returns the event back to the transactional outbox with a pending state, allowing outbox workers to process it.
2. **Purge Old/Invalid Events**:
   If an event is corrupted and cannot be replayed, delete it to prevent storage build-up:
   ```bash
   curl -X DELETE http://localhost:8080/api/dlq/<event_id>
   ```
