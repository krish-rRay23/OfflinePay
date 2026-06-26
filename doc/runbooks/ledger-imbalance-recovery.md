# Runbook: Ledger Imbalance Recovery

## Context
The continuous financial validator monitors the database to check if the fundamental double-entry invariant holds:
\[\sum \text{Debits} == \sum \text{Credits}\]
If this invariant is broken, an alert is triggered in the logs:
`ALERT: Ledger imbalance detected! Financial invariant broken!`

## Critical Rules
> [!IMPORTANT]
> - **Never Auto-Heal**: Do not write scripts that automatically balance or alter historical ledger records.
> - **Preserve Audit Trail**: Every adjustment must be written as a new transaction with its own unique `txn_id`.

## Diagnostics
1. **Identify the Imbalance**:
   Run the audit query to identify the transaction ID that broke parity:
   ```sql
   SELECT txn_id, SUM(CASE WHEN direction = 'DEBIT' THEN amount ELSE -amount END) AS imbalance
   FROM ledger_entries
   GROUP BY txn_id
   HAVING SUM(CASE WHEN direction = 'DEBIT' THEN amount ELSE -amount END) != 0;
   ```
2. **Examine the Saga States**:
   Retrieve the saga trace for the affected transaction:
   ```sql
   SELECT * FROM saga_states WHERE txn_id = '<txn_id>';
   ```
   Check which steps completed and if compensation failed.

## Remediation Steps
1. **Quarantine the Accounts**:
   If the imbalance is large or suggests ongoing exploit, temporarily freeze the affected accounts:
   ```sql
   UPDATE account_balances SET available_balance = 0 WHERE account_id IN (sender_id, receiver_id);
   ```
2. **Perform Manual Balancing Adjustment**:
   Write a correcting transaction entry in the ledger (e.g. an auditing correcting entry) to offset the imbalance.
   - For example, if a debit of $10 was recorded but the credit was missed, insert the compensating credit.
   - The adjustment must have the transaction ID matching `adjustment_<original_txn_id>` to ensure clear audit linking.
3. **Verify Ledger Parity**:
   Rerun the audit query to verify the imbalance is resolved.
