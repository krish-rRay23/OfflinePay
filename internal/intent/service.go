package intent

import (
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"time"

	"offlinepay/internal/crypto"
	"offlinepay/internal/domain"

	"github.com/google/uuid"
)

type Service struct{}

func NewService() *Service {
	return &Service{}
}

// Struct to combine payload and its signature for encryption
type SignedPaymentIntent struct {
	Payload         domain.PaymentIntentPayload `json:"payload"`
	DeviceSignature string                      `json:"device_signature"`
}

// CreateSignedAndEncryptedEnvelope simulates an offline device creating, signing, and encrypting a payment
func (s *Service) CreateSignedAndEncryptedEnvelope(
	senderID string,
	receiverID string,
	amount int64,
	currency string,
	deviceID string,
	tokenID string,
	devicePriv *ecdsa.PrivateKey,
	bankPub *ecdsa.PublicKey,
	duration time.Duration,
) (*domain.EncryptedEnvelope, *domain.PaymentIntentPayload, error) {

	txnID := uuid.New().String()
	nonce := uuid.New().String()
	expiry := time.Now().Add(duration)

	payload := domain.PaymentIntentPayload{
		TxnID:      txnID,
		SenderID:   senderID,
		ReceiverID: receiverID,
		Amount:     amount,
		Currency:   currency,
		Nonce:      nonce,
		Expiry:     expiry,
		DeviceID:   deviceID,
		TokenID:    tokenID,
	}

	// 1. Serialize payload fields to canonical string for signing
	canonicalData := GetCanonicalPayloadString(&payload)

	// 2. Sign canonical data using device private key
	signature, err := crypto.Sign(devicePriv, []byte(canonicalData))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to sign payment intent payload: %w", err)
	}

	// 3. Assemble the envelope payload containing the raw transaction and signature
	signedIntent := SignedPaymentIntent{
		Payload:         payload,
		DeviceSignature: signature,
	}

	signedIntentJSON, err := json.Marshal(signedIntent)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal signed intent: %w", err)
	}

	// 4. Encrypt using ECIES
	ephemeralPubPEM, ciphertext, iv, authTag, err := crypto.EncryptECIES(bankPub, signedIntentJSON)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to encrypt envelope payload: %w", err)
	}

	envelope := &domain.EncryptedEnvelope{
		EphemeralPublicKey: ephemeralPubPEM,
		Ciphertext:         ciphertext,
		Nonce:              nonce,
		AuthTag:            authTag,
		TxnID:              txnID,
		IV:                 iv,
	}

	return envelope, &payload, nil
}

// GetCanonicalPayloadString creates a deterministic string from the payment intent fields
func GetCanonicalPayloadString(p *domain.PaymentIntentPayload) string {
	return fmt.Sprintf("txn:%s:sender:%s:rec:%s:amt:%d:cur:%s:nonce:%s:exp:%d:dev:%s:tok:%s",
		p.TxnID, p.SenderID, p.ReceiverID, p.Amount, p.Currency, p.Nonce, p.Expiry.Unix(), p.DeviceID, p.TokenID)
}
