package router

import (
	"encoding/json"
	"errors"
	"net/http"

	"replica/internal/service"
)

func registerStorageShareRoutes(mux *http.ServeMux, svc services) {
	gate := sharingAPIGate(svc)

	mux.HandleFunc("POST /api/share/auth/login", gate(serveShareAuthLogin(svc)))
	mux.HandleFunc("POST /api/share/auth/refresh", gate(serveShareAuthRefresh(svc)))
	mux.HandleFunc("GET /api/share/auth/me", gate(serveShareAuthMe(svc)))
	mux.HandleFunc("GET /api/share/shares", gate(svc.storage.ServeAuthenticatedShares))
	mux.HandleFunc("GET /api/share/shares/{id}", gate(svc.storage.ServeAuthenticatedShares))
	mux.HandleFunc("GET /api/share/shares/{id}/files", gate(svc.storage.ServeAuthenticatedShares))
	mux.HandleFunc("POST /api/share/shares/{id}/files", gate(svc.storage.ServeAuthenticatedShares))
	mux.HandleFunc("DELETE /api/share/shares/{id}/files/{file_id}", gate(svc.storage.ServeAuthenticatedShares))
	mux.HandleFunc("GET /api/share/shares/{id}/files/{file_id}/content", gate(svc.storage.ServeAuthenticatedShares))
	mux.HandleFunc("GET /api/share/shares/{id}/files/{file_id}/thumbnail", gate(svc.storage.ServeAuthenticatedShares))
	mux.HandleFunc("PUT /api/share/shares/{id}/files/{file_id}/content", gate(svc.storage.ServeAuthenticatedShares))
	mux.HandleFunc("GET /s/{link_hash}", gate(svc.storage.ServePublicShares))
	mux.HandleFunc("GET /s/{link_hash}/files", gate(svc.storage.ServePublicShares))
	mux.HandleFunc("POST /s/{link_hash}/files", gate(svc.storage.ServePublicShares))
	mux.HandleFunc("DELETE /s/{link_hash}/files/{file_id}", gate(svc.storage.ServePublicShares))
	mux.HandleFunc("GET /s/{link_hash}/files/{file_id}/content", gate(svc.storage.ServePublicShares))
	mux.HandleFunc("GET /s/{link_hash}/files/{file_id}/thumbnail", gate(svc.storage.ServePublicShares))
	mux.HandleFunc("PUT /s/{link_hash}/files/{file_id}/content", gate(svc.storage.ServePublicShares))
}

func sharingAPIGate(svc services) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if svc.storage != nil && !svc.storage.SharingEnabled() {
				writeJSONError(w, http.StatusNotFound, "sharing is disabled")
				return
			}
			next(w, r)
		}
	}
}

func serveShareAuthLogin(svc services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc.auth == nil {
			svc.storage.ServeUserLoginProxy(w, r)
			return
		}
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON payload")
			return
		}
		pair, err := svc.auth.Login(body.Username, body.Password)
		if err != nil {
			writeShareAuthError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, tokenPairFromService(pair).Body)
	}
}

func serveShareAuthRefresh(svc services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc.auth == nil {
			svc.storage.ServeUserRefreshProxy(w, r)
			return
		}
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON payload")
			return
		}
		pair, err := svc.auth.Refresh(body.RefreshToken)
		if err != nil {
			writeShareAuthError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, tokenPairFromService(pair).Body)
	}
}

func serveShareAuthMe(svc services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc.auth == nil {
			svc.storage.ServeUserMe(w, r)
			return
		}
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		accessToken, err := bearerToken(r.Header.Get("Authorization"))
		if err != nil {
			writeJSONError(w, http.StatusUnauthorized, "missing authenticated user")
			return
		}
		user, err := svc.auth.ValidateUserAccessToken(accessToken)
		if err != nil {
			writeShareAuthError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, shareAuthMeBody{
			UserID:   user.UserID,
			Username: user.Username,
			Status:   user.Status,
		})
	}
}

type shareAuthMeBody struct {
	UserID   uint   `json:"user_id"`
	Username string `json:"username"`
	Status   string `json:"status"`
}

func writeShareAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidCredentials):
		writeJSONError(w, http.StatusUnauthorized, "invalid username or password")
	case errors.Is(err, service.ErrInactiveUser):
		writeJSONError(w, http.StatusForbidden, "inactive user")
	case errors.Is(err, service.ErrInvalidToken):
		writeJSONError(w, http.StatusUnauthorized, "invalid token")
	case errors.Is(err, service.ErrExpiredToken):
		writeJSONError(w, http.StatusUnauthorized, "expired token")
	case errors.Is(err, service.ErrRevokedToken):
		writeJSONError(w, http.StatusUnauthorized, "revoked token")
	default:
		writeJSONError(w, http.StatusInternalServerError, "auth request failed")
	}
}
