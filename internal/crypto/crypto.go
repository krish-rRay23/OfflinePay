package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
)

// Generate P-256 Private Key
func GenerateKeyPair() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

// Export Public Key to PEM string
func ExportPublicKeyToPEM(pub *ecdsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	block := &pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: der,
	}
	return string(pem.EncodeToMemory(block)), nil
}

// Parse PEM string to Public Key
func ParsePEMToPublicKey(pemStr string) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("failed to parse PEM block containing public key")
	}
	pubInterface, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := pubInterface.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("not an ECDSA public key")
	}
	return pub, nil
}

// Export Private Key to PEM string
func ExportPrivateKeyToPEM(priv *ecdsa.PrivateKey) (string, error) {
	der, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return "", err
	}
	block := &pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: der,
	}
	return string(pem.EncodeToMemory(block)), nil
}

// Parse PEM string to Private Key
func ParsePEMToPrivateKey(pemStr string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("failed to parse PEM block containing private key")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

type ecdsaSignature struct {
	R, S *big.Int
}

// Sign data using ECDSA P-256 and return base64 signature
func Sign(priv *ecdsa.PrivateKey, data []byte) (string, error) {
	hash := sha256.Sum256(data)
	r, s, err := ecdsa.Sign(rand.Reader, priv, hash[:])
	if err != nil {
		return "", err
	}
	sigStruct := ecdsaSignature{R: r, S: s}
	sigBytes, err := asn1.Marshal(sigStruct)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sigBytes), nil
}

// Verify signature of data using ECDSA P-256 public key
func Verify(pub *ecdsa.PublicKey, data []byte, sigBase64 string) bool {
	sigBytes, err := base64.StdEncoding.DecodeString(sigBase64)
	if err != nil {
		return false
	}
	var sigStruct ecdsaSignature
	_, err = asn1.Unmarshal(sigBytes, &sigStruct)
	if err != nil {
		return false
	}
	hash := sha256.Sum256(data)
	return ecdsa.Verify(pub, hash[:], sigStruct.R, sigStruct.S)
}

// HKDF-SHA256 Implementation (Extract and Expand)
func hkdfSHA256(secret, salt, info []byte, length int) ([]byte, error) {
	if salt == nil {
		salt = make([]byte, sha256.Size)
	}
	// Extract
	mac := hmac.New(sha256.New, salt)
	mac.Write(secret)
	prk := mac.Sum(nil)

	// Expand
	okm := make([]byte, length)
	t := []byte{}
	var err error

	mac = hmac.New(sha256.New, prk)
	for i := 0; i < (length+sha256.Size-1)/sha256.Size; i++ {
		mac.Reset()
		mac.Write(t)
		mac.Write(info)
		mac.Write([]byte{byte(i + 1)})
		t = mac.Sum(nil)
		copy(okm[i*sha256.Size:], t)
	}
	if err != nil {
		return nil, err
	}
	return okm[:length], nil
}

// Encrypt payload using ECIES (ECDH P-256 + HKDF + AES-256-GCM)
func EncryptECIES(bankPub *ecdsa.PublicKey, payload []byte) (ephemeralPubPEM string, ciphertextB64 string, ivB64 string, authTagB64 string, err error) {
	// Generate Ephemeral Key Pair
	ephemeralPriv, err := GenerateKeyPair()
	if err != nil {
		return "", "", "", "", err
	}
	ephemeralPubPEM, err = ExportPublicKeyToPEM(&ephemeralPriv.PublicKey)
	if err != nil {
		return "", "", "", "", err
	}

	// Perform ECDH: Multiply ephemeral private key by bank's public key point
	x, _ := bankPub.Curve.ScalarMult(bankPub.X, bankPub.Y, ephemeralPriv.D.Bytes())
	sharedSecret := x.Bytes()

	// Derive AES Key via HKDF-SHA256
	salt := []byte("offlinepay-ecies-salt")
	info := []byte("offlinepay-aes-key")
	aesKey, err := hkdfSHA256(sharedSecret, salt, info, 32)
	if err != nil {
		return "", "", "", "", err
	}

	// Encrypt using AES-GCM
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return "", "", "", "", err
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", "", "", err
	}

	// Generate random 12-byte IV
	iv := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return "", "", "", "", err
	}

	// Ciphertext contains both the cipher text and auth tag (appended by Seal)
	sealed := aesGCM.Seal(nil, iv, payload, nil)
	tagSize := aesGCM.Overhead()
	ciphertext := sealed[:len(sealed)-tagSize]
	authTag := sealed[len(sealed)-tagSize:]

	ciphertextB64 = base64.StdEncoding.EncodeToString(ciphertext)
	ivB64 = base64.StdEncoding.EncodeToString(iv)
	authTagB64 = base64.StdEncoding.EncodeToString(authTag)

	return ephemeralPubPEM, ciphertextB64, ivB64, authTagB64, nil
}

// Decrypt payload using ECIES (ECDH P-256 + HKDF + AES-256-GCM)
func DecryptECIES(bankPriv *ecdsa.PrivateKey, ephemeralPubPEM string, ciphertextB64 string, ivB64 string, authTagB64 string) ([]byte, error) {
	ephemeralPub, err := ParsePEMToPublicKey(ephemeralPubPEM)
	if err != nil {
		return nil, fmt.Errorf("invalid ephemeral public key: %w", err)
	}

	// Perform ECDH: Multiply bank private key by ephemeral public key point
	x, _ := bankPriv.Curve.ScalarMult(ephemeralPub.X, ephemeralPub.Y, bankPriv.D.Bytes())
	sharedSecret := x.Bytes()

	// Derive AES Key via HKDF-SHA256
	salt := []byte("offlinepay-ecies-salt")
	info := []byte("offlinepay-aes-key")
	aesKey, err := hkdfSHA256(sharedSecret, salt, info, 32)
	if err != nil {
		return nil, err
	}

	// Decode elements
	ciphertext, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return nil, fmt.Errorf("invalid ciphertext encoding: %w", err)
	}
	iv, err := base64.StdEncoding.DecodeString(ivB64)
	if err != nil {
		return nil, fmt.Errorf("invalid iv encoding: %w", err)
	}
	authTag, err := base64.StdEncoding.DecodeString(authTagB64)
	if err != nil {
		return nil, fmt.Errorf("invalid auth tag encoding: %w", err)
	}

	// Reconstruct the full sealed ciphertext required by AES-GCM Open
	sealed := append(ciphertext, authTag...)

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, err
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	plaintext, err := aesGCM.Open(nil, iv, sealed, nil)
	if err != nil {
		return nil, fmt.Errorf("decryption failed: %w", err)
	}

	return plaintext, nil
}
