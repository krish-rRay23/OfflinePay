-- The prior operation key included the action. Collapse any historical duplicate
-- operations before enforcing the stronger one-operation-per-transaction invariant.
WITH ranked AS (
    SELECT recovery_id,
           row_number() OVER (PARTITION BY txn_id ORDER BY created_at ASC, recovery_id ASC) AS rank
    FROM recovery_operations
)
DELETE FROM recovery_operations r
USING ranked
WHERE r.recovery_id = ranked.recovery_id
  AND ranked.rank > 1;

CREATE UNIQUE INDEX IF NOT EXISTS uq_recovery_operations_txn_id
    ON recovery_operations (txn_id);
