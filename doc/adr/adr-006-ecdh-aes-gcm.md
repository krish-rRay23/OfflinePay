# ADR-006: ECIES Hybrid Encryption (ECDH + AES-GCM)

## Status
Accepted

## Context
Offline pay requests (envelopes) must be securely transmitted through untrusted, proxy, or peer-to-peer relay nodes without leaking transaction details (sender ID, receiver ID, amount) or allowing tampering.

## Decision
We implemented **ECIES Hybrid Encryption** using P-256 Elliptic Curve Diffie-Hellman (ECDH) and AES-256-GCM.
- Senders generate an ephemeral EC key pair.
- They perform ECDH exchange with the bank's public key to derive a shared symmetric key.
- The intent payload is encrypted using AES-256-GCM with a unique IV and authentication tag.
- Senders package the ephemeral public key, ciphertext, IV, and auth tag into an `EncryptedEnvelope` before transmitting.

## Consequences
### Pros
- **End-to-End Confidentiality**: Only the bank (possessing the private key) can decrypt and read the payment intent.
- **Integrity**: AES-GCM's authentication tag prevents tampering by relayers.
- **No Symmetric Key Storage**: Ephemeral keys prevent the need to share symmetric secrets globally.

### Cons
- **Computation Overhead**: Performing ECDH on mobile devices is more CPU-intensive than symmetric crypto.
- **Key Distribution**: The bank's public key must be securely installed on the sender's device.
