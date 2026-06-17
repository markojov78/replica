package router

import "net/http"

func registerStorageShareRoutes(mux *http.ServeMux, svc services) {
	mux.HandleFunc("POST /api/share/auth/login", svc.storage.ServeUserLoginProxy)
	mux.HandleFunc("GET /api/share/shares", svc.storage.ServeAuthenticatedShares)
	mux.HandleFunc("GET /api/share/shares/{id}", svc.storage.ServeAuthenticatedShares)
	mux.HandleFunc("GET /api/share/shares/{id}/files", svc.storage.ServeAuthenticatedShares)
	mux.HandleFunc("GET /api/share/shares/{id}/files/{file_id}/content", svc.storage.ServeAuthenticatedShares)
	mux.HandleFunc("GET /s/{link_hash}", svc.storage.ServePublicShares)
	mux.HandleFunc("GET /s/{link_hash}/files", svc.storage.ServePublicShares)
	mux.HandleFunc("GET /s/{link_hash}/files/{file_id}/content", svc.storage.ServePublicShares)
}
