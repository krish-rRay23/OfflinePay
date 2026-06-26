package crypto

import (
	"bytes"
	"testing"
)

func TestECDSASigning(t *testing.T) {
	priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("failed to generate key pair: %v", err)
	}

	pubPEM, err := ExportPublicKeyToPEM(&priv.PublicKey)
	if err != nil {
		t.Fatalf("failed to export public key: %v", err)
	}

	pub, err := ParsePEMToPublicKey(pubPEM)
	if err != nil {
		t.Fatalf("failed to parse public key PEM: %v", err)
	}

	message := []byte("hello-world-offlinepay-signature-testing")
	signature, err := Sign(priv, message)
	if err != nil {
		t.Fatalf("failed to sign message: %v", err)
	}

	if signature == "" {
		t.Fatal("empty signature returned")
	}

	valid := Verify(pub, message, signature)
	if !valid {
		t.Fatal("failed to verify signature")
	}

	invalid := Verify(pub, []byte("tampered-message"), signature)
	if invalid {
		t.Fatal("signature verified for a tampered message")
	}
}

func TestECIESEncryption(t *testing.T) {
	bankPriv, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("failed to generate bank key pair: %v", err)
	}

	payload := []byte("secret-payment-payload-12345")
	ephemeralPubPEM, ciphertext, iv, authTag, err := EncryptECIES(&bankPriv.PublicKey, payload)
	if err != nil {
		t.Fatalf("failed to encrypt: %v", err)
	}

	decrypted, err := DecryptECIES(bankPriv, ephemeralPubPEM, ciphertext, iv, authTag)
	if err != nil {
		t.Fatalf("failed to decrypt: %v", err)
	}

	if !bytes.Equal(payload, decrypted) {
		t.Errorf("decrypted content does not match. Expected %s, got %s", payload, decrypted)
	}

	// Test tampering
	tamperedCiphertext := ciphertext + "A"
	_, err = DecryptECIES(bankPriv, ephemeralPubPEM, tamperedCiphertext, iv, authTag)
	if err == nil {
		t.Fatal("decryption succeeded with a tampered ciphertext")
	}
}
