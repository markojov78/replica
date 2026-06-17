package router

import "net/http"

func registerStorageShareRoutes(mux *http.ServeMux, svc services) {
	mux.HandleFunc("POST /api/auth/login", svc.storage.ServeUserLoginProxy)
	mux.HandleFunc("GET /api/shares", svc.storage.ServeAuthenticatedShares)
	mux.HandleFunc("GET /api/shares/{id}", svc.storage.ServeAuthenticatedShares)
	mux.HandleFunc("GET /api/shares/{id}/files", svc.storage.ServeAuthenticatedShares)
	mux.HandleFunc("GET /api/shares/{id}/files/{file_id}/content", svc.storage.ServeAuthenticatedShares)
	mux.HandleFunc("GET /api/public/shares/{link_hash}", svc.storage.ServePublicShares)
	mux.HandleFunc("GET /api/public/shares/{link_hash}/files", svc.storage.ServePublicShares)
	mux.HandleFunc("GET /api/public/shares/{link_hash}/files/{file_id}/content", svc.storage.ServePublicShares)
}
