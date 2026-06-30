package service

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"replica/internal/model"
	"replica/internal/repository"

	"github.com/golang-jwt/jwt/v5"
)

const (
	SettingTransferKeyPublic     = "transfer_key_public"
	SettingTransferKeyPrivate    = "transfer_key_private"
	SettingTransferKeyCreateTime = "transfer_key_create_time"
)

var (
	ErrIncompleteTransferKeys    = errors.New("incomplete transfer key settings")
	ErrTransferPublicKeyUnset    = errors.New("transfer public key is not configured")
	ErrTransferPrivateKeyUnset   = errors.New("transfer private key is not configured")
	ErrInvalidTransferPrivateKey = errors.New("invalid transfer private key")
)

type SettingService struct {
	settings *repository.SettingRepository
	now      func() time.Time
}

type TransferTokenInput struct {
	SourceReplicaID      uint
	DestinationReplicaID uint
	SourceNodeID         string
	DestinationNodeID    string
	ExpiresIn            time.Duration
}

type TransferTokenClaims struct {
	Purpose              string `json:"purpose"`
	SourceReplicaID      uint   `json:"source_replica_id"`
	DestinationReplicaID uint   `json:"destination_replica_id"`
	SourceNodeID         string `json:"source_node_id"`
	DestinationNodeID    string `json:"destination_node_id"`
	jwt.RegisteredClaims
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

func (s *SettingService) TransferPrivateKey() (string, error) {
	setting, err := s.settings.FindByKey(SettingTransferKeyPrivate)
	if err != nil {
		if s.settings.IsNotFound(err) {
			return "", ErrTransferPrivateKeyUnset
		}
		return "", err
	}
	return setting.Value, nil
}

func (s *SettingService) NewReplicaTransferToken(input TransferTokenInput) (string, error) {
	privateKeyPEM, err := s.TransferPrivateKey()
	if err != nil {
		return "", err
	}

	privateKey, err := parseRSAPrivateKeyPEM(privateKeyPEM)
	if err != nil {
		return "", err
	}

	expiresIn := input.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 15 * time.Minute
	}
	now := s.now()
	claims := TransferTokenClaims{
		Purpose:              "replica_file_transfer",
		SourceReplicaID:      input.SourceReplicaID,
		DestinationReplicaID: input.DestinationReplicaID,
		SourceNodeID:         input.SourceNodeID,
		DestinationNodeID:    input.DestinationNodeID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "coordinator",
			Subject:   input.DestinationNodeID,
			Audience:  jwt.ClaimStrings{input.SourceNodeID},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(expiresIn)),
		},
	}

	return jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(privateKey)
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

	publicKey, privateKey, err := GenerateTransferKeyPairPEM()
	if err != nil {
		return err
	}

	return s.settings.CreateMany([]model.Setting{
		{Key: SettingTransferKeyPublic, Value: publicKey},
		{Key: SettingTransferKeyPrivate, Value: privateKey},
		{Key: SettingTransferKeyCreateTime, Value: s.now().Format(time.RFC3339)},
	})
}

func parseRSAPrivateKeyPEM(value string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		return nil, ErrInvalidTransferPrivateKey
	}

	privateKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, ErrInvalidTransferPrivateKey
	}
	return privateKey, nil
}

func GenerateTransferKeyPairPEM() (string, string, error) {
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
