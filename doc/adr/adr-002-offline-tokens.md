# ADR-002: Offline Spending Tokens

## Status
Accepted

## Context
OfflinePay needs to support payments in environments without internet connectivity. To prevent double-spending and control financial risk, we need a mechanism that allows users to pre-authorize transactions while online, which can then be securely signed and spent offline.

## Decision
We implemented a **Signed Offline Token** design.
- The bank issues a cryptographically signed token containing a token ID, owner ID, limit value, and expiry timestamp.
- The sender's app stores the token and generates signed offline intent envelopes using the token ID.
- The ledger service reserves the funds corresponding to the token value in the user's account at the time of token issuance.

## Consequences
### Pros
- **Strong Spending Caps**: Senders cannot spend more than the token value offline.
- **Double-Spend Prevention**: Since a token has a single token ID, the bank-authoritative ledger service can track token consumption status and reject any duplicate attempts to spend with the same token.
- **Zero Network Required at Point of Sale**: The token itself is proof of reservation and validity, enabling zero-internet payments.

### Cons
- **Pre-allocation of Capital**: Sender funds are reserved (locked) and cannot be used for other transactions until the token is settled or expires.
- **Expiration Management**: Tokens must have short expiries, requiring a background reconciliation process to release reserved funds if the token is never spent.
