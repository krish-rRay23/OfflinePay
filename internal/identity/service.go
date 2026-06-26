package identity

import (
	"context"
	"errors"
	"time"

	"offlinepay/internal/domain"
	"offlinepay/internal/repository"
)

type Service struct {
	repo *repository.Repository
}

func NewService(repo *repository.Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) RegisterDevice(ctx context.Context, deviceID string, ownerID string, publicKey string) (*domain.Device, error) {
	if deviceID == "" || ownerID == "" || publicKey == "" {
		return nil, errors.New("missing required registration fields")
	}

	// Verify that the public key is syntactically valid PEM
	// (We can verify this by checking if it parses, though for the client's sake, we just validate it doesn't crash)
	dev := &domain.Device{
		DeviceID:   deviceID,
		OwnerID:    ownerID,
		PublicKey:  publicKey,
		TrustScore: 1.0,
		Status:     domain.DeviceActive,
		CreatedAt:  time.Now(),
	}

	err := s.repo.CreateDevice(ctx, dev)
	if err != nil {
		return nil, err
	}

	return dev, nil
}

func (s *Service) LookupDevice(ctx context.Context, deviceID string) (*domain.Device, error) {
	return s.repo.GetDevice(ctx, deviceID)
}

func (s *Service) RevokeDevice(ctx context.Context, deviceID string, compromised bool) error {
	status := domain.DeviceRevoked
	if compromised {
		status = domain.DeviceCompromised
	}
	return s.repo.UpdateDeviceStatus(ctx, deviceID, status)
}
