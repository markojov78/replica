package router

import (
	"context"
	"errors"
	"net/http"

	"replica/internal/model"
	"replica/internal/service"

	"github.com/danielgtaylor/huma/v2"
)

func registerConfigRoutes(api huma.API, svc services) {
	huma.Get(api, "/config", func(ctx context.Context, input *adminConfigInput) (*adminConfigResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceSettings, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		configs, err := svc.configs.List()
		if err != nil {
			return nil, mapConfigError(err)
		}
		return &adminConfigResponse{Body: *configs}, nil
	})

	huma.Patch(api, "/config", func(ctx context.Context, input *patchConfigInput) (*adminConfigResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceSettings, model.PermissionActionUpdate); err != nil {
			return nil, mapPermissionError(err)
		}

		configs, err := svc.configs.Update(input.Body.Items)
		if err != nil {
			return nil, mapConfigError(err)
		}
		return &adminConfigResponse{Body: *configs}, nil
	})

	huma.Delete(api, "/config", func(ctx context.Context, input *adminConfigInput) (*deleteConfigResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceSettings, model.PermissionActionUpdate); err != nil {
			return nil, mapPermissionError(err)
		}

		if err := svc.configs.DeleteAll(); err != nil {
			return nil, mapConfigError(err)
		}
		return &deleteConfigResponse{Status: http.StatusNoContent}, nil
	})

	huma.Delete(api, "/config/{key}", func(ctx context.Context, input *deleteConfigKeyInput) (*deleteConfigResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceSettings, model.PermissionActionUpdate); err != nil {
			return nil, mapPermissionError(err)
		}

		if err := svc.configs.DeleteKey(input.Key); err != nil {
			return nil, mapConfigError(err)
		}
		return &deleteConfigResponse{Status: http.StatusNoContent}, nil
	})
}

func registerInternalConfigRoutes(api huma.API, svc services) {
	huma.Get(api, "/config", func(ctx context.Context, input *nodeConfigInput) (*nodeConfigResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated node")
		}
		if _, err := svc.auth.Node(accessToken); err != nil {
			return nil, mapNodeMeError(err)
		}

		configs, err := svc.configs.List()
		if err != nil {
			return nil, mapConfigError(err)
		}
		return &nodeConfigResponse{Body: configs.Items}, nil
	})

	huma.Get(api, "/config/storage-profiles", func(ctx context.Context, input *nodeConfigInput) (*nodeStorageProfilesResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated node")
		}
		node, err := svc.auth.Node(accessToken)
		if err != nil {
			return nil, mapNodeMeError(err)
		}
		if svc.profiles == nil {
			return nil, huma.Error500InternalServerError("config request failed")
		}

		profiles, err := svc.profiles.ListForNode(node.ID, node.PublicKey)
		if err != nil {
			return nil, mapConfigError(err)
		}
		return &nodeStorageProfilesResponse{Body: profiles}, nil
	})
}

func mapConfigError(err error) error {
	switch {
	case errors.Is(err, service.ErrEmptyConfigUpdate):
		return huma.Error400BadRequest("empty config update")
	case errors.Is(err, service.ErrUnknownConfigKey):
		return huma.Error400BadRequest("unknown configuration key")
	case errors.Is(err, service.ErrInvalidConfigValue), errors.Is(err, service.ErrInvalidConfigSetting):
		return huma.Error400BadRequest("invalid configuration value")
	case errors.Is(err, service.ErrNodePublicKeyNotRegistered):
		return huma.Error409Conflict("node encryption public key not registered")
	case errors.Is(err, service.ErrStorageProfileEncryption):
		return huma.Error500InternalServerError("failed to encrypt storage profile credentials", err)
	default:
		return huma.Error500InternalServerError("config request failed", err)
	}
}

type adminConfigInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
}

type patchConfigInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	Body          struct {
		Items []service.ConfigUpdateItem `json:"items"`
	}
}

type deleteConfigKeyInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	Key           string `path:"key"`
}

type nodeConfigInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
}

type adminConfigResponse struct {
	Body service.ConfigList
}

type nodeConfigResponse struct {
	Body []service.ConfigItem
}

type nodeStorageProfilesResponse struct {
	Body []service.StorageProfileDetails
}

type deleteConfigResponse struct {
	Status int `status:"204"`
}
