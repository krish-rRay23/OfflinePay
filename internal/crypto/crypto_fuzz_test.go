package crypto

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"
)

func FuzzParsePEMToPublicKey(f *testing.F) {
	// Add seed corpus
	f.Add("invalid pem data")
	f.Add("-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE9ZxPuGoekx6mkq8bg9so8CfQT6ac\nBF6iyy2GtXi//X0UEid4pWgs0Pvsi0KUHgKnEcKbd1rI1Lg8/UOKZDPrIg==\n-----END PUBLIC KEY-----\n")
	
	f.Fuzz(func(t *testing.T, data string) {
		// ParsePEMToPublicKey should return an error or a valid key, but must never panic
		_, _ = ParsePEMToPublicKey(data)
	})
}

func FuzzParsePEMToPrivateKey(f *testing.F) {
	f.Add("invalid pem private key")
	f.Add("-----BEGIN EC PRIVATE KEY-----\nMHcCAQEEIEO9xf9kYFvIjupGyQQXi3/RH7bfVtjc3b/0jG9jrnpJoAoGCCqGSM49\nAwEHoUQDQgAE9ZxPuGoekx6mkq8bg9so8CfQT6ac\n-----END EC PRIVATE KEY-----\n")

	f.Fuzz(func(t *testing.T, data string) {
		_, _ = ParsePEMToPrivateKey(data)
	})
}

func FuzzVerifySignature(f *testing.F) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		f.Fatalf("failed to generate key: %v", err)
	}
	pub := &priv.PublicKey

	f.Add([]byte("hello world"), "MEQCIE/dO3908k4Z3zG22...")
	f.Add([]byte(""), "")

	f.Fuzz(func(t *testing.T, msg []byte, sigBase64 string) {
		// Verify must fail safely (returning false) for invalid signatures, but never panic
		_ = Verify(pub, msg, sigBase64)
	})
}

func FuzzDecryptECIES(f *testing.F) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		f.Fatalf("failed to generate key: %v", err)
	}

	f.Add("invalid ephemeral key", "invalid cipher", "invalid iv", "invalid tag")

	f.Fuzz(func(t *testing.T, ephemPEM, cipherB64, ivB64, tagB64 string) {
		// DecryptECIES must fail safely and return an error for malformed parameters, never panic
		_, _ = DecryptECIES(priv, ephemPEM, cipherB64, ivB64, tagB64)
	})
}
