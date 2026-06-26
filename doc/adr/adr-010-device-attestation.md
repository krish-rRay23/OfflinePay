# ADR-010: Hardware-Backed Device Attestation

## Status
Accepted

## Context
In offline environments, clone attacks (copying key pairs to other devices) and compromised software on client devices present severe risks. We need a way to verify that offline payment intents are generated exclusively by authorized, secure hardware (TPM or Secure Enclave).

## Decision
We implemented **Device Attestation Checks** during payment settlement.
- When registering a device, the client must submit a hardware-generated attestation statement (attestation type, signature, and cryptographic hash of the public key bounded by the hardware root key).
- The bank validates this attestation and stores it in `device_attestations` with a trust level (e.g. TRUSTED, UNTRUSTED).
- During settlement, the service looks up the device's attestation status and rejects the transaction if the device is not registered or is flagged as `UNTRUSTED`.

## Consequences
### Pros
- **Clone Resistance**: Restricts key pairs to the physical hardware module where they were generated.
- **Hardware Root of Trust**: Prevents rooted or emulator devices from participating in token spending.
- **Dynamic Risk Adjustment**: Compromised hardware can be immediately marked `UNTRUSTED` in the database, blocking all pending settlement attempts.

### Cons
- **Platform Fragmentation**: Validating attestations requires supporting multiple vendor frameworks (Apple DeviceCheck/AppAttest, Google Play Integrity, Android Key Attestation).
- **Setup Complexity**: Initial device registration requires processing complex payload binaries.
