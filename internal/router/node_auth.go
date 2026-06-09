package router

import (
	"context"
	"errors"
	"net/http"

	"replica/internal/service"
)

type authenticatedNodeContextKey struct{}

func requireAuthenticatedNode(auth *service.AuthService, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accessToken, err := bearerToken(r.Header.Get("Authorization"))
		if err != nil {
			writeJSONError(w, http.StatusUnauthorized, "missing authenticated node")
			return
		}

		node, err := auth.Node(accessToken)
		if err != nil {
			status, message := mapNodeAuthHTTPError(err)
			writeJSONError(w, status, message)
			return
		}

		ctx := context.WithValue(r.Context(), authenticatedNodeContextKey{}, node)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func authenticatedNodeFromContext(ctx context.Context) (*service.AuthenticatedNode, bool) {
	node, ok := ctx.Value(authenticatedNodeContextKey{}).(*service.AuthenticatedNode)
	return node, ok
}

func authenticatedNodeIDFromContext(ctx context.Context) (string, bool) {
	node, ok := authenticatedNodeFromContext(ctx)
	if !ok || node == nil || node.ID == "" {
		return "", false
	}
	return node.ID, true
}

func mapNodeAuthHTTPError(err error) (int, string) {
	switch {
	case errors.Is(err, service.ErrInvalidToken), errors.Is(err, service.ErrExpiredToken):
		return http.StatusUnauthorized, "missing authenticated node"
	case errors.Is(err, service.ErrDisabledNode):
		return http.StatusForbidden, "disabled node"
	case errors.Is(err, service.ErrRevokedNode):
		return http.StatusForbidden, "revoked node"
	default:
		return http.StatusInternalServerError, "failed to resolve authenticated node"
	}
}
