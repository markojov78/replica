package router

import (
	"compress/gzip"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"replica/internal/service"
)

// registerInternalNodeLogRoute registers the storage-node log upload. The body is NDJSON,
// optionally gzip-compressed, so it is served by a plain handler instead of huma.
func registerInternalNodeLogRoute(mux *http.ServeMux, svc services) {
	mux.Handle("POST /node/nodes/logs", requireAuthenticatedNode(svc.auth, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nodeID, ok := authenticatedNodeIDFromContext(r.Context())
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "missing authenticated node")
			return
		}
		logs := svc.nodes.Logs()
		if logs == nil {
			writeJSONError(w, http.StatusNotFound, "node logs are not enabled")
			return
		}

		var body io.Reader = http.MaxBytesReader(w, r.Body, service.NodeLogMaxBytes)
		switch encoding := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); encoding {
		case "", "identity":
		case "gzip":
			gz, err := gzip.NewReader(body)
			if err != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid gzip body")
				return
			}
			defer gz.Close()
			body = gz
		default:
			writeJSONError(w, http.StatusUnsupportedMediaType, "unsupported content encoding")
			return
		}

		result, err := logs.Ingest(nodeID, body)
		if err != nil {
			var maxBytesErr *http.MaxBytesError
			switch {
			case errors.Is(err, service.ErrNodeLogBatchTooLarge), errors.As(err, &maxBytesErr):
				writeJSONError(w, http.StatusRequestEntityTooLarge, service.ErrNodeLogBatchTooLarge.Error())
			case errors.Is(err, service.ErrInvalidNodeLogBody):
				writeJSONError(w, http.StatusBadRequest, "invalid log body")
			default:
				log.Printf("store node logs node_id=%s error=%v", nodeID, err)
				writeJSONError(w, http.StatusInternalServerError, "failed to store node logs")
			}
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
}
