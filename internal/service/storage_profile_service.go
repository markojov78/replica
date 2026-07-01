package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"

	"replica/internal/config"
)

var ErrNodePublicKeyNotRegistered = errors.New("node encryption public key not registered")
var ErrStorageProfileEncryption = errors.New("failed to encrypt storage profile credentials")

type StorageProfileDetails struct {
	Name         string `json:"name"`
	EncryptedKey string `json:"encrypted_key"`
	Nonce        string `json:"nonce"`
	Payload      string `json:"payload"`
}

type encryptedStorageProfile struct {
	EncryptedKey string
	Nonce        string
	Payload      string
}

type StorageProfileService struct {
	replicas *ReplicaService
	config   config.StorageConfig
}

func NewStorageProfileService(replicas *ReplicaService, cfg config.StorageConfig) *StorageProfileService {
	return &StorageProfileService{replicas: replicas, config: cfg}
}

func (s *StorageProfileService) ListForNode(nodeID string, nodePublicKey string) ([]StorageProfileDetails, error) {
	if strings.TrimSpace(nodePublicKey) == "" {
		return nil, ErrNodePublicKeyNotRegistered
	}
	publicKey, err := parseStorageProfileRSAPublicKey(nodePublicKey)
	if err != nil {
		return nil, ErrNodePublicKeyNotRegistered
	}

	replicas, err := s.replicas.ListByNodeID(nodeID)
	if err != nil {
		return nil, err
	}

	seen := map[string]struct{}{}
	profiles := make([]StorageProfileDetails, 0)
	for _, replica := range replicas {
		name := strings.ToLower(strings.TrimSpace(replica.StorageProfile))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}

		profile, ok := s.config.Profiles[name]
		if !ok {
			continue
		}
		encrypted, err := encryptStorageProfileCredentials(publicKey, profile)
		if err != nil {
			return nil, ErrStorageProfileEncryption
		}
		profiles = append(profiles, StorageProfileDetails{
			Name:         name,
			EncryptedKey: encrypted.EncryptedKey,
			Nonce:        encrypted.Nonce,
			Payload:      encrypted.Payload,
		})
	}

	return profiles, nil
}

func parseStorageProfileRSAPublicKey(value string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(value)))
	if block == nil {
		return nil, errors.New("missing pem block")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	publicKey, ok := key.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("public key is not rsa")
	}
	return publicKey, nil
}

func encryptStorageProfileCredentials(publicKey *rsa.PublicKey, profile config.StorageProfileConfig) (encryptedStorageProfile, error) {
	plaintext, err := json.Marshal(struct {
		Endpoint        string `json:"endpoint"`
		Region          string `json:"region"`
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
	}{
		Endpoint:        strings.TrimSpace(profile.Endpoint),
		Region:          strings.TrimSpace(profile.Region),
		AccessKeyID:     profile.AccessKeyID,
		SecretAccessKey: profile.SecretAccessKey,
	})
	if err != nil {
		return encryptedStorageProfile{}, err
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return encryptedStorageProfile{}, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return encryptedStorageProfile{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return encryptedStorageProfile{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return encryptedStorageProfile{}, err
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	encryptedKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, publicKey, key, nil)
	if err != nil {
		return encryptedStorageProfile{}, err
	}

	return encryptedStorageProfile{
		EncryptedKey: base64.StdEncoding.EncodeToString(encryptedKey),
		Nonce:        base64.StdEncoding.EncodeToString(nonce),
		Payload:      base64.StdEncoding.EncodeToString(ciphertext),
	}, nil
}
