package router

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"dropoutbox/internal/buildinfo"
	"dropoutbox/internal/config"
	"dropoutbox/internal/service"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

type services struct {
	auth  *service.AuthService
	users *service.UserService
	roles *service.RoleService
}

func New(
	cfg config.Config,
	info buildinfo.Info,
	authService *service.AuthService,
	userService *service.UserService,
	roleService *service.RoleService,
) http.Handler {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig(cfg.App.Name, info.Version))
	apiGroup := huma.NewGroup(api, "/api")

	svc := services{
		auth:  authService,
		users: userService,
		roles: roleService,
	}

	registerServiceInfoRoute(mux, info, svc)
	registerAuthRoutes(apiGroup, svc)
	registerUserRoutes(apiGroup, svc)
	registerRoleRoutes(apiGroup, svc)

	return withMiddleware(mux)
}

func withMiddleware(next http.Handler) http.Handler {
	return recoverMiddleware(loggingMiddleware(next))
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		log.Printf("%s %s status=%d duration=%s", r.Method, r.URL.RequestURI(), recorder.status, time.Since(start).Round(time.Millisecond))
	})
}

func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("panic serving %s %s: %v", r.Method, r.URL.RequestURI(), recovered)
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
		}()

		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

type versionHeader struct {
	APIVersion string `header:"X-API-Version" doc:"API version" enum:"1,v1"`
}

func bearerToken(header string) (string, error) {
	if header == "" {
		return "", errors.New("missing authorization header")
	}

	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return "", errors.New("invalid authorization header")
	}

	return strings.TrimSpace(parts[1]), nil
}

func mapAuthError(err error) error {
	switch {
	case errors.Is(err, service.ErrInvalidCredentials):
		return huma.Error401Unauthorized("invalid username or password")
	case errors.Is(err, service.ErrInactiveUser):
		return huma.Error403Forbidden("inactive user")
	case errors.Is(err, service.ErrInvalidToken):
		return huma.Error401Unauthorized("invalid token")
	case errors.Is(err, service.ErrExpiredToken):
		return huma.Error401Unauthorized("expired token")
	default:
		return huma.Error500InternalServerError("auth request failed", err)
	}
}

func mapMeError(err error) error {
	switch {
	case errors.Is(err, service.ErrInvalidToken), errors.Is(err, service.ErrExpiredToken):
		return huma.Error401Unauthorized("missing authenticated user")
	default:
		return huma.Error500InternalServerError("failed to resolve current user", err)
	}
}

func mapMeHTTPError(err error) (int, string) {
	switch {
	case errors.Is(err, service.ErrInvalidToken), errors.Is(err, service.ErrExpiredToken):
		return http.StatusUnauthorized, "missing authenticated user"
	default:
		return http.StatusInternalServerError, "failed to resolve current user"
	}
}

func mapUserError(err error, userService *service.UserService) error {
	lower := strings.ToLower(err.Error())

	switch {
	case userService.IsNotFound(err):
		return huma.Error404NotFound("user not found")
	case errors.Is(err, service.ErrInvalidUserStatus):
		return huma.Error400BadRequest("invalid user status")
	case errors.Is(err, service.ErrInvalidRoles):
		return huma.Error400BadRequest("invalid roles")
	case strings.Contains(lower, "unique"):
		return huma.Error409Conflict("user already exists")
	default:
		return huma.Error500InternalServerError("user request failed", err)
	}
}

func mapRoleError(err error, roleService *service.RoleService) error {
	lower := strings.ToLower(err.Error())

	switch {
	case roleService.IsNotFound(err):
		return huma.Error404NotFound("role not found")
	case errors.Is(err, service.ErrInvalidRoleStatus):
		return huma.Error400BadRequest("invalid role status")
	case errors.Is(err, service.ErrInvalidPermissions):
		return huma.Error400BadRequest("invalid permissions")
	case strings.Contains(lower, "unique"):
		return huma.Error409Conflict("role already exists")
	default:
		return huma.Error500InternalServerError("role request failed", err)
	}
}

func mapPermissionError(err error) error {
	switch {
	case errors.Is(err, service.ErrInvalidToken), errors.Is(err, service.ErrExpiredToken):
		return huma.Error401Unauthorized("missing authenticated user")
	case errors.Is(err, service.ErrForbidden):
		return huma.Error403Forbidden("missing required permission")
	default:
		return huma.Error500InternalServerError("permission check failed", err)
	}
}

func resolvePagination(page, perPage int) (int, int) {
	if page == 0 {
		page = 1
	}
	if perPage == 0 {
		perPage = 20
	}

	return page, perPage
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
