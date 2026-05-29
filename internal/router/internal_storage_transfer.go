package router

import "net/http"

func registerInternalStorageTransferRoutes(mux *http.ServeMux, svc services) {
	mux.HandleFunc("GET /internal/replicas/{replica_id}/files/{file_id}/content", svc.storage.ServeReplicaFileContent)
}
