package storage

import (
	"context"
	"sync"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type S3ClientProvider struct {
	mu     sync.Mutex
	client *s3.Client
}

func (p *S3ClientProvider) Client(ctx context.Context) (*s3.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.client != nil {
		return p.client, nil
	}

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}

	p.client = s3.NewFromConfig(cfg)
	return p.client, nil
}
