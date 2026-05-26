package storage

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"dropoutbox/internal/apiclient"
	"dropoutbox/internal/config"

	"github.com/gorilla/websocket"
)

const bootstrapRetryInterval = 5 * time.Second
const commandWebSocketPath = "/internal/nodes/ws"

type Runtime struct {
	client            *apiclient.Client
	heartbeatInterval time.Duration
	wsConnected       atomic.Bool
	wsDialer          *websocket.Dialer
}

func NewRuntime(cfg config.Config) (*Runtime, error) {
	client, err := apiclient.New(cfg)
	if err != nil {
		return nil, err
	}

	return &Runtime{
		client:            client,
		heartbeatInterval: cfg.App.HeartbeatInterval,
		wsDialer:          websocket.DefaultDialer,
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
	go r.commandLoop(ctx)
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

		report, err := r.client.ReportAvailability(ctx)
		if err != nil {
			if !sleepContext(ctx, bootstrapRetryInterval) {
				return nil, false
			}
			log.Printf("storage runtime initial heartbeat failed: %v", err)
			continue
		}

		r.processFallbackCommands(report.Commands)
		log.Printf("storage runtime connected to coordinator as node_id=%s", r.client.NodeID())
		return pair, true
	}
}

// Token refresh loop
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
			report, err := r.client.ReportAvailability(ctx)
			if err != nil {
				log.Printf("storage runtime heartbeat failed: %v", err)
				continue
			}
			r.processFallbackCommands(report.Commands)
		}
	}
}

func (r *Runtime) commandLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}

		if err := r.listenForCommands(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("storage runtime websocket listener failed: %v", err)
		}

		if !sleepContext(ctx, bootstrapRetryInterval) {
			return
		}
	}
}

func (r *Runtime) listenForCommands(ctx context.Context) error {
	accessToken, err := r.client.AccessToken(ctx)
	if err != nil {
		return err
	}

	wsURL, err := r.client.WebSocketURL(commandWebSocketPath)
	if err != nil {
		return err
	}

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+accessToken)

	conn, _, err := r.wsDialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return err
	}
	defer conn.Close()

	r.wsConnected.Store(true)
	defer r.wsConnected.Store(false)

	log.Printf("storage runtime websocket connected to %s", wsURL)

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	for {
		var command apiclient.Command
		if err := conn.ReadJSON(&command); err != nil {
			return err
		}
		r.processCommand(command)
	}
}

func (r *Runtime) processFallbackCommands(commands []apiclient.Command) {
	if r.wsConnected.Load() {
		return
	}

	for _, command := range commands {
		r.processCommand(command)
	}
}

func (r *Runtime) processCommand(command apiclient.Command) {
	log.Printf("storage runtime got command id=%d type=%s status=%s payload=%s", command.ID, command.Type, command.Status, formatPayload(command.Payload))
}

func formatPayload(payload json.RawMessage) string {
	if len(payload) == 0 {
		return "{}"
	}
	return string(payload)
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
