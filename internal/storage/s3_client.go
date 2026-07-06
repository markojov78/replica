package storage

import (
	"context"
	"replica/internal/config"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsConfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type S3ClientProvider struct {
	mu      sync.Mutex
	clients map[string]*s3.Client
}

func (p *S3ClientProvider) Client(ctx context.Context, profile *config.StorageProfileConfig) (*s3.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := s3ClientCacheKey(profile)
	if p.clients == nil {
		p.clients = make(map[string]*s3.Client)
	}
	if client := p.clients[key]; client != nil {
		return client, nil
	}

	options := make([]func(*awsConfig.LoadOptions) error, 0, 2)
	if profile != nil {
		options = append(options, awsConfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			profile.AccessKeyID,
			profile.SecretAccessKey,
			"",
		)))
		if profile.Region != "" {
			options = append(options, awsConfig.WithRegion(profile.Region))
		}
	}

	cfg, err := awsConfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, err
	}

	clientOptions := make([]func(*s3.Options), 0, 1)
	if profile != nil && profile.Endpoint != "" {
		clientOptions = append(clientOptions, func(options *s3.Options) {
			options.BaseEndpoint = aws.String(profile.Endpoint)
		})
	}

	client := s3.NewFromConfig(cfg, clientOptions...)
	p.clients[key] = client
	return client, nil
}

func s3ClientCacheKey(profile *config.StorageProfileConfig) string {
	if profile == nil {
		return "default"
	}
	return "profile:" + profile.ProfileName
}
