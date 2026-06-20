package router

import (
	"bufio"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"replica/internal/admin"
	"replica/internal/buildinfo"
	"replica/internal/config"
	"replica/internal/service"
	"replica/internal/storage"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

type services struct {
	auth        *service.AuthService
	users       *service.UserService
	roles       *service.RoleService
	nodes       *service.NodeService
	inventories *service.InventoryService
	replicas    *service.ReplicaService
	shares      *service.ShareService
	configs     *service.ConfigService
	storage     *storage.Runtime
}

func New(
	cfg config.Config,
	info buildinfo.Info,
	authService *service.AuthService,
	userService *service.UserService,
	roleService *service.RoleService,
	nodeService *service.NodeService,
	inventoryService *service.InventoryService,
	replicaService *service.ReplicaService,
	shareService *service.ShareService,
	storageRuntime *storage.Runtime,
	configServices ...*service.ConfigService,
) http.Handler {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig(ServiceName, info.Version))
	adminGroup := huma.NewGroup(api, "/api/admin")
	nodeGroup := huma.NewGroup(api, "/node")
	var configService *service.ConfigService
	if len(configServices) > 0 {
		configService = configServices[0]
	}

	svc := services{
		auth:        authService,
		users:       userService,
		roles:       roleService,
		nodes:       nodeService,
		inventories: inventoryService,
		replicas:    replicaService,
		shares:      shareService,
		configs:     configService,
		storage:     storageRuntime,
	}

	registerServiceInfoRoute(mux, cfg, info, svc)
	if cfg.App.Storage && storageRuntime != nil {
		registerInternalStorageTransferRoutes(mux, svc)
		registerStorageShareRoutes(mux, svc)
	}
	if cfg.App.Coordinator || authService != nil {
		registerPublicAuthRoutes(adminGroup, svc)
		registerInternalAuthRoutes(nodeGroup, svc)
		registerInternalNodeWebSocketRoute(mux, svc)
		registerInternalNodeRoutes(nodeGroup, svc)
		registerInternalCommandRoutes(nodeGroup, svc)
		registerInternalReplicaRoutes(nodeGroup, svc)
		registerInternalShareRoutes(nodeGroup, svc)
		registerInternalConfigRoutes(nodeGroup, svc)
		registerUserRoutes(adminGroup, svc)
		registerRoleRoutes(adminGroup, svc)
		registerNodeRoutes(adminGroup, svc)
		registerInventoryRoutes(adminGroup, svc)
		registerReplicaRoutes(adminGroup, svc)
		registerShareRoutes(adminGroup, svc)
		registerConfigRoutes(adminGroup, svc)
		if err := admin.Register(mux, mux); err != nil {
			panic(err)
		}
	}

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

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
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
	case errors.Is(err, service.ErrInvalidNodeCredentials):
		return huma.Error401Unauthorized("invalid node credentials")
	case errors.Is(err, service.ErrInactiveUser):
		return huma.Error403Forbidden("inactive user")
	case errors.Is(err, service.ErrDisabledNode):
		return huma.Error403Forbidden("disabled node")
	case errors.Is(err, service.ErrRevokedNode):
		return huma.Error403Forbidden("revoked node")
	case errors.Is(err, service.ErrInvalidToken):
		return huma.Error401Unauthorized("invalid token")
	case errors.Is(err, service.ErrExpiredToken):
		return huma.Error401Unauthorized("expired token")
	case errors.Is(err, service.ErrRevokedToken):
		return huma.Error401Unauthorized("revoked token")
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

func mapNodeMeError(err error) error {
	switch {
	case errors.Is(err, service.ErrInvalidToken), errors.Is(err, service.ErrExpiredToken):
		return huma.Error401Unauthorized("missing authenticated node")
	case errors.Is(err, service.ErrDisabledNode):
		return huma.Error403Forbidden("disabled node")
	case errors.Is(err, service.ErrRevokedNode):
		return huma.Error403Forbidden("revoked node")
	default:
		return huma.Error500InternalServerError("failed to resolve current node", err)
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

func mapNodeError(err error, nodeService *service.NodeService) error {
	lower := strings.ToLower(err.Error())

	switch {
	case nodeService.IsNotFound(err):
		return huma.Error404NotFound("node not found")
	case errors.Is(err, service.ErrNodeCommandNotFound):
		return huma.Error404NotFound("node command not found")
	case errors.Is(err, service.ErrNodeCommandOwnership):
		return huma.Error403Forbidden("node command belongs to another node")
	case errors.Is(err, service.ErrInvalidNodeStatus):
		return huma.Error400BadRequest("invalid node status")
	case errors.Is(err, service.ErrInvalidNodeCommandStatus):
		return huma.Error400BadRequest("invalid node command status")
	case strings.Contains(lower, "unique"):
		return huma.Error409Conflict("node already exists")
	default:
		return huma.Error500InternalServerError("node request failed", err)
	}
}

func mapInventoryError(err error, inventoryService *service.InventoryService) error {
	var activeReplicaLocationError *service.ActiveReplicaLocationError

	switch {
	case inventoryService.IsNotFound(err):
		return huma.Error404NotFound("inventory not found")
	case errors.Is(err, service.ErrInvalidInventoryStatus):
		return huma.Error400BadRequest("invalid inventory status")
	case errors.Is(err, service.ErrInvalidInventoryFileStatus):
		return huma.Error400BadRequest("invalid inventory file status")
	case errors.Is(err, service.ErrInvalidInventoryType):
		return huma.Error400BadRequest("invalid inventory type")
	case errors.Is(err, service.ErrInvalidPermissions):
		return huma.Error400BadRequest("invalid permissions")
	case errors.Is(err, service.ErrInvalidInventoryURI):
		return huma.Error400BadRequest("invalid inventory uri")
	case errors.Is(err, service.ErrInventoryDeleted):
		return huma.Error409Conflict("inventory is deleted")
	case errors.Is(err, service.ErrInventoryHasActiveReplica):
		return huma.Error409Conflict("inventory has active replicas")
	case errors.Is(err, service.ErrReplicaHasActiveShare):
		return huma.Error409Conflict("replica has active shares")
	case errors.As(err, &activeReplicaLocationError):
		return huma.Error409Conflict(activeReplicaLocationError.Error())
	case errors.Is(err, service.ErrInventoryFileNotFound):
		return huma.Error404NotFound("inventory file not found")
	case errors.Is(err, service.ErrReplicaFileNotFound):
		return huma.Error404NotFound("replica file not found")
	case errors.Is(err, service.ErrInvalidReplicaFileStatus):
		return huma.Error400BadRequest("invalid replica file status")
	case errors.Is(err, service.ErrInvalidReplicaFileUpdate):
		return huma.Error400BadRequest("invalid replica file update")
	case errors.Is(err, service.ErrInvalidReplicaFileAction):
		return huma.Error400BadRequest("invalid file action")
	case errors.Is(err, service.ErrInvalidReplicaStatus):
		return huma.Error400BadRequest("invalid replica status")
	case errors.Is(err, service.ErrInvalidReplicaType):
		return huma.Error400BadRequest("invalid replica type")
	case errors.Is(err, service.ErrInvalidReplicaURI):
		return huma.Error400BadRequest("invalid replica uri")
	case errors.Is(err, service.ErrInvalidReplicaUpstream):
		return huma.Error400BadRequest("invalid replica upstream")
	case errors.Is(err, service.ErrReplicaNotFound):
		return huma.Error404NotFound("replica not found")
	default:
		return huma.Error500InternalServerError("inventory request failed", err)
	}
}

func mapShareError(err error, shareService *service.ShareService) error {
	switch {
	case shareService.IsNotFound(err):
		return huma.Error404NotFound("share not found")
	case errors.Is(err, service.ErrInvalidShareStatus):
		return huma.Error400BadRequest("invalid share status")
	case errors.Is(err, service.ErrInvalidShareName):
		return huma.Error400BadRequest("invalid share name")
	case errors.Is(err, service.ErrInvalidShareExpiration):
		return huma.Error400BadRequest("invalid share expiration")
	case errors.Is(err, service.ErrInvalidPermissions):
		return huma.Error400BadRequest("invalid permissions")
	case errors.Is(err, service.ErrReplicaNotFound):
		return huma.Error404NotFound("replica not found")
	case errors.Is(err, service.ErrInvalidReplicaStatus):
		return huma.Error409Conflict("replica is deleted")
	case errors.Is(err, service.ErrShareAlreadyExists):
		return huma.Error409Conflict("share already exists")
	default:
		return huma.Error500InternalServerError("share request failed", err)
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
