package storage

import (
	"context"
	"log"
	"time"

	"dropoutbox/internal/apiclient"
	"dropoutbox/internal/config"
)

const bootstrapRetryInterval = 5 * time.Second

type Runtime struct {
	client            *apiclient.Client
	heartbeatInterval time.Duration
}

func NewRuntime(cfg config.Config) (*Runtime, error) {
	client, err := apiclient.New(cfg)
	if err != nil {
		return nil, err
	}

	return &Runtime{
		client:            client,
		heartbeatInterval: cfg.App.HeartbeatInterval,
	}, nil
}

func (r *Runtime) Start(ctx context.Context) {
	go r.run(ctx)
}

func (r *Runtime) run(ctx context.Context) {
	pair, ok := r.bootstrap(ctx)
	if !ok {
		return
	}

	go r.refreshLoop(ctx, pair)
	go r.heartbeatLoop(ctx)
}

func (r *Runtime) bootstrap(ctx context.Context) (*apiclient.NodeTokenPair, bool) {
	for {
		pair, err := r.client.Authenticate(ctx)
		if err != nil {
			if !sleepContext(ctx, bootstrapRetryInterval) {
				return nil, false
			}
			log.Printf("storage runtime authenticate failed: %v", err)
			continue
		}

		if _, err := r.client.ReportAvailability(ctx); err != nil {
			if !sleepContext(ctx, bootstrapRetryInterval) {
				return nil, false
			}
			log.Printf("storage runtime initial heartbeat failed: %v", err)
			continue
		}

		log.Printf("storage runtime connected to coordinator as node_id=%s", r.client.NodeID())
		return pair, true
	}
}

func (r *Runtime) refreshLoop(ctx context.Context, pair *apiclient.NodeTokenPair) {
	current := pair
	for {
		delay := refreshDelay(time.Now().UTC(), current.AccessTokenExpiresAt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		nextPair, err := r.client.Refresh(ctx)
		if err != nil {
			log.Printf("storage runtime token refresh failed: %v", err)
			nextPair, err = r.client.Authenticate(ctx)
			if err != nil {
				log.Printf("storage runtime re-authenticate failed: %v", err)
				if !sleepContext(ctx, bootstrapRetryInterval) {
					return
				}
				continue
			}
		}

		current = nextPair
	}
}

func (r *Runtime) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(r.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := r.client.ReportAvailability(ctx); err != nil {
				log.Printf("storage runtime heartbeat failed: %v", err)
			}
		}
	}
}

func refreshDelay(now, expiresAt time.Time) time.Duration {
	until := expiresAt.Sub(now)
	if until <= time.Second {
		return time.Second
	}

	lead := time.Minute
	if until <= 2*time.Minute {
		lead = until / 2
	}

	delay := until - lead
	if delay < time.Second {
		return time.Second
	}
	return delay
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
