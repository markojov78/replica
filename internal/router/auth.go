package router

import (
	"context"
	"net/http"
	"time"

	"replica/internal/service"

	"github.com/danielgtaylor/huma/v2"
)

func registerPublicAuthRoutes(api huma.API, svc services) {
	huma.Post(api, "/auth/login", func(ctx context.Context, input *loginInput) (*tokenPairResponse, error) {
		pair, err := svc.auth.Login(input.Body.Username, input.Body.Password)
		if err != nil {
			return nil, mapAuthError(err)
		}
		return tokenPairFromService(pair), nil
	})

	huma.Post(api, "/auth/refresh", func(ctx context.Context, input *refreshInput) (*tokenPairResponse, error) {
		pair, err := svc.auth.Refresh(input.Body.RefreshToken)
		if err != nil {
			return nil, mapAuthError(err)
		}
		return tokenPairFromService(pair), nil
	})

	huma.Post(api, "/auth/logout", func(ctx context.Context, input *logoutInput) (*logoutResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("invalid token")
		}

		if err := svc.auth.Logout(accessToken); err != nil {
			return nil, mapAuthError(err)
		}

		return &logoutResponse{Status: http.StatusNoContent}, nil
	})

	huma.Get(api, "/auth/me", func(ctx context.Context, input *meInput) (*meResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}

		user, err := svc.auth.Me(accessToken)
		if err != nil {
			return nil, mapMeError(err)
		}

		return &meResponse{
			Body: meBody{
				ID:       user.ID,
				Username: user.Username,
				Status:   user.Status,
				Roles:    user.Roles,
			},
		}, nil
	})
}

func registerInternalAuthRoutes(api huma.API, svc services) {
	huma.Post(api, "/auth/login", func(ctx context.Context, input *nodeLoginInput) (*nodeTokenPairResponse, error) {
		pair, err := svc.auth.NodeLogin(input.Body.NodeID, input.Body.Secret, input.Body.PublicKey)
		if err != nil {
			return nil, mapAuthError(err)
		}
		return nodeTokenPairFromService(pair), nil
	})

	huma.Post(api, "/auth/refresh", func(ctx context.Context, input *refreshInput) (*nodeTokenPairResponse, error) {
		pair, err := svc.auth.NodeRefresh(input.Body.RefreshToken)
		if err != nil {
			return nil, mapAuthError(err)
		}
		return nodeTokenPairFromService(pair), nil
	})

	huma.Get(api, "/auth/me", func(ctx context.Context, input *nodeMeInput) (*nodeMeResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated node")
		}

		node, err := svc.auth.Node(accessToken)
		if err != nil {
			return nil, mapNodeMeError(err)
		}

		return &nodeMeResponse{
			Body: nodeMeBody{
				ID:     node.ID,
				Status: node.Status,
			},
		}, nil
	})

	huma.Post(api, "/auth/validate-user-token", func(ctx context.Context, input *validateUserTokenInput) (*validateUserTokenResponse, error) {
		nodeAccessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated node")
		}

		if _, err := svc.auth.Node(nodeAccessToken); err != nil {
			return nil, mapNodeMeError(err)
		}

		token, err := svc.auth.ValidateUserAccessToken(input.Body.AccessToken)
		if err != nil {
			return nil, mapAuthError(err)
		}

		return &validateUserTokenResponse{
			Body: validateUserTokenBody{
				UserID:               token.UserID,
				Username:             token.Username,
				Status:               token.Status,
				AccessTokenExpiresAt: token.AccessExpires,
			},
		}, nil
	})
}

type loginInput struct {
	versionHeader
	Body struct {
		Username string `json:"username" minLength:"1"`
		Password string `json:"password" minLength:"1"`
	}
}

type refreshInput struct {
	versionHeader
	Body struct {
		RefreshToken string `json:"refresh_token" minLength:"1"`
	}
}

type nodeLoginInput struct {
	versionHeader
	Body struct {
		NodeID    string `json:"node_id" minLength:"1"`
		Secret    string `json:"secret" minLength:"1"`
		PublicKey string `json:"public_key" minLength:"1"`
	}
}

type logoutInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
}

type meInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
}

type nodeMeInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
}

type validateUserTokenInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	Body          struct {
		AccessToken string `json:"access_token" minLength:"1"`
	}
}

type tokenPairBody struct {
	UserID                uint      `json:"user_id"`
	AccessToken           string    `json:"access_token"`
	RefreshToken          string    `json:"refresh_token"`
	AccessTokenExpiresAt  time.Time `json:"access_token_expires_at"`
	RefreshTokenExpiresAt time.Time `json:"refresh_token_expires_at"`
}

type tokenPairResponse struct {
	Body tokenPairBody
}

type nodeTokenPairBody struct {
	NodeID                 string    `json:"node_id"`
	AccessToken            string    `json:"access_token"`
	RefreshToken           string    `json:"refresh_token"`
	AccessTokenExpiresAt   time.Time `json:"access_token_expires_at"`
	RefreshTokenExpiresAt  time.Time `json:"refresh_token_expires_at"`
	TransferTokenPublicKey string    `json:"transfer_token_public_key"`
}

type nodeTokenPairResponse struct {
	Body nodeTokenPairBody
}

type meBody struct {
	ID       uint                  `json:"id"`
	Username string                `json:"username"`
	Status   string                `json:"status"`
	Roles    []service.RoleDetails `json:"roles"`
}

type meResponse struct {
	Body meBody
}

type nodeMeBody struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type nodeMeResponse struct {
	Body nodeMeBody
}

type validateUserTokenBody struct {
	UserID               uint      `json:"user_id"`
	Username             string    `json:"username"`
	Status               string    `json:"status"`
	AccessTokenExpiresAt time.Time `json:"access_token_expires_at"`
}

type validateUserTokenResponse struct {
	Body validateUserTokenBody
}

type logoutResponse struct {
	Status int `status:"204"`
}

func tokenPairFromService(pair *service.TokenPair) *tokenPairResponse {
	return &tokenPairResponse{
		Body: tokenPairBody{
			UserID:                pair.UserID,
			AccessToken:           pair.AccessToken,
			RefreshToken:          pair.RefreshToken,
			AccessTokenExpiresAt:  pair.AccessTokenExpiresAt,
			RefreshTokenExpiresAt: pair.RefreshTokenExpiresAt,
		},
	}
}

func nodeTokenPairFromService(pair *service.NodeTokenPair) *nodeTokenPairResponse {
	return &nodeTokenPairResponse{
		Body: nodeTokenPairBody{
			NodeID:                 pair.NodeID,
			AccessToken:            pair.AccessToken,
			RefreshToken:           pair.RefreshToken,
			AccessTokenExpiresAt:   pair.AccessTokenExpiresAt,
			RefreshTokenExpiresAt:  pair.RefreshTokenExpiresAt,
			TransferTokenPublicKey: pair.TransferTokenPublicKey,
		},
	}
}
