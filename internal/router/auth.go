package router

import (
	"context"
	"net/http"
	"time"

	"dropoutbox/internal/service"

	"github.com/danielgtaylor/huma/v2"
)

func registerAuthRoutes(api huma.API, svc services) {
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

type logoutInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
}

type meInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
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

type meBody struct {
	ID       uint                  `json:"id"`
	Username string                `json:"username"`
	Status   string                `json:"status"`
	Roles    []service.RoleDetails `json:"roles"`
}

type meResponse struct {
	Body meBody
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
