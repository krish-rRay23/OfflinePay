# Runbook: Replay Attack Incident Response

## Context
A replay attack occurs when a malicious relay node or interception system attempts to submit an already-processed offline payment envelope to double-debit a sender. 
OfflinePay uses:
- Database unique constraints: `UNIQUE(nonce)` on `nonce_registry`
- Idempotency status checks on the payment intent table

If a replay attack is attempted, the settlement service rejects it with `DUPLICATE` status. However, a spike in duplicate statuses indicates a potential active attack or malfunctioning relay.

## Diagnostics
1. **Monitor Duplicate Spikes**:
   Check Prometheus metrics for duplicates:
   `rate(settlement_total{status="DUPLICATE"}[5m])`
2. **Find Attacking Relays**:
   Query the `relay_attempts` table to locate which relay IP or node ID is broadcasting duplicate transactions:
   ```sql
   SELECT relay_id, COUNT(*) as duplicate_attempts
   FROM relay_attempts
   WHERE txn_id IN (SELECT txn_id FROM payment_intents WHERE status = 'DUPLICATE')
   GROUP BY relay_id;
   ```

## Remediation Steps
1. **Block/Throttling of Relay Nodes**:
   If a specific relay node is flooding the server with duplicates, flag it in the system or database:
   - Call the crash/offline simulation API for the relay node or block it at the API Gateway level.
2. **Blacklist Compromised Devices**:
   If the duplicates are coming from a single device, revoke it:
   ```bash
   curl -X POST http://localhost:8080/api/identity/devices/<device_id>/revoke -d '{"compromised": true}'
   ```
3. **Notify Sender**:
   Alert the sender user that their device public key is generating duplicates, which may indicate a cloned credential.
