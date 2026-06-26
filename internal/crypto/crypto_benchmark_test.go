package crypto

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"
)

func BenchmarkECIESEncryption(b *testing.B) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		b.Fatalf("failed to generate key: %v", err)
	}
	pub := &priv.PublicKey
	payload := []byte("transaction-intent-payload-data-to-encrypt-in-ecies-envelope")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _, err := EncryptECIES(pub, payload)
		if err != nil {
			b.Fatalf("encryption failed: %v", err)
		}
	}
}

func BenchmarkECIESDecryption(b *testing.B) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		b.Fatalf("failed to generate key: %v", err)
	}
	pub := &priv.PublicKey
	payload := []byte("transaction-intent-payload-data-to-encrypt-in-ecies-envelope")

	ephemPEM, cipherB64, ivB64, tagB64, err := EncryptECIES(pub, payload)
	if err != nil {
		b.Fatalf("failed to prepare ciphertext: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := DecryptECIES(priv, ephemPEM, cipherB64, ivB64, tagB64)
		if err != nil {
			b.Fatalf("decryption failed: %v", err)
		}
	}
}

func BenchmarkECDSASign(b *testing.B) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		b.Fatalf("failed to generate key: %v", err)
	}
	payload := []byte("transaction-intent-payload-data-to-sign")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := Sign(priv, payload)
		if err != nil {
			b.Fatalf("signing failed: %v", err)
		}
	}
}

func BenchmarkECDSAVerify(b *testing.B) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		b.Fatalf("failed to generate key: %v", err)
	}
	pub := &priv.PublicKey
	payload := []byte("transaction-intent-payload-data-to-sign")

	sig, err := Sign(priv, payload)
	if err != nil {
		b.Fatalf("failed to prepare signature: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ok := Verify(pub, payload, sig)
		if !ok {
			b.Fatalf("verification failed")
		}
	}
}
