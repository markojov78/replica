package service

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"dropoutbox/internal/model"
	"dropoutbox/internal/repository"
)

const (
	SettingTransferKeyPublic     = "transfer_key_public"
	SettingTransferKeyPrivate    = "transfer_key_private"
	SettingTransferKeyCreateTime = "transfer_key_create_time"
)

var (
	ErrIncompleteTransferKeys = errors.New("incomplete transfer key settings")
	ErrTransferPublicKeyUnset = errors.New("transfer public key is not configured")
)

type SettingService struct {
	settings *repository.SettingRepository
	now      func() time.Time
}

func NewSettingService(settings *repository.SettingRepository) *SettingService {
	return &SettingService{
		settings: settings,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (s *SettingService) TransferPublicKey() (string, error) {
	setting, err := s.settings.FindByKey(SettingTransferKeyPublic)
	if err != nil {
		if s.settings.IsNotFound(err) {
			return "", ErrTransferPublicKeyUnset
		}
		return "", err
	}
	return setting.Value, nil
}

func (s *SettingService) EnsureTransferKeys() error {
	existing, err := s.settings.FindExisting(SettingTransferKeyPublic, SettingTransferKeyPrivate)
	if err != nil {
		return err
	}

	_, hasPublic := existing[SettingTransferKeyPublic]
	_, hasPrivate := existing[SettingTransferKeyPrivate]
	switch {
	case hasPublic && hasPrivate:
		return nil
	case hasPublic || hasPrivate:
		return fmt.Errorf("%w: both %q and %q must exist or both must be absent", ErrIncompleteTransferKeys, SettingTransferKeyPublic, SettingTransferKeyPrivate)
	}

	publicKey, privateKey, err := generateTransferKeyPairPEM()
	if err != nil {
		return err
	}

	return s.settings.CreateMany([]model.Setting{
		{Key: SettingTransferKeyPublic, Value: publicKey},
		{Key: SettingTransferKeyPrivate, Value: privateKey},
		{Key: SettingTransferKeyCreateTime, Value: s.now().Format(time.RFC3339)},
	})
}

func generateTransferKeyPairPEM() (string, string, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return "", "", err
	}

	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return "", "", err
	}

	publicPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: publicDER,
	})
	privatePEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})
	return string(publicPEM), string(privatePEM), nil
}
