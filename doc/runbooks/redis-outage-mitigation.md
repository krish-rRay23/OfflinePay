# Runbook: Redis Broker Outage Mitigation

## Context
Redis is used as the event stream broker. If Redis crashes or goes offline, downstream write-projections (CQRS read models) will stop updating. However, due to the Transactional Outbox pattern, write transactions (settlement, reservations) will continue to commit successfully to PostgreSQL.

## Diagnostics
1. **Detect Outages**:
   Look for the following log errors:
   `redis connection lost (simulated chaos)` or `failed to publish to stream`
2. **Check Outbox Backlog**:
   Query the database to check the volume of pending outbox events accumulating:
   ```sql
   SELECT COUNT(*) FROM outbox_events WHERE status = 'PENDING';
   ```

## Remediation Steps
1. **Restart/Recover Redis**:
   Verify Redis service status and start it if stopped:
   ```bash
   docker-compose start redis
   ```
2. **Re-enable Outbox Worker Pool**:
   Once Redis is healthy, the outbox workers will automatically resume processing the backlog.
3. **Verify Projection Convergence**:
   Check if the read projections have caught up to the authoritative payment intent state:
   ```sql
   SELECT COUNT(*) FROM payment_intents WHERE status != (SELECT status FROM payment_read_projections WHERE payment_read_projections.txn_id = payment_intents.txn_id);
   ```
   If any discrepancy exists, rerun the outbox events or execute a manual synchronization query.
