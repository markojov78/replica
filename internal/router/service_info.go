package router

import (
	"net/http"

	"replica/internal/buildinfo"
	"replica/internal/config"
)

const ServiceName = "Replica"

func registerServiceInfoRoute(mux *http.ServeMux, cfg config.Config, info buildinfo.Info, svc services) {
	mux.HandleFunc("GET /api/admin/{$}", func(w http.ResponseWriter, r *http.Request) {
		if cfg.App.Coordinator || svc.auth != nil {
			accessToken, err := bearerToken(r.Header.Get("Authorization"))
			if err != nil {
				writeJSONError(w, http.StatusUnauthorized, "missing authenticated user")
				return
			}

			if _, err := svc.auth.Me(accessToken); err != nil {
				status, message := mapMeHTTPError(err)
				writeJSONError(w, status, message)
				return
			}
		}

		writeJSON(w, http.StatusOK, serviceInfoBody{
			Service:     ServiceName,
			Version:     info.Version,
			Commit:      info.Commit,
			BuildDate:   info.BuildDate,
			NodeID:      cfg.App.NodeID,
			Coordinator: cfg.App.Coordinator,
			Storage:     cfg.App.Storage,
		})
	})
}

type serviceInfoBody struct {
	Service     string `json:"service"`
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	BuildDate   string `json:"build_date"`
	NodeID      string `json:"node_id"`
	Coordinator bool   `json:"coordinator"`
	Storage     bool   `json:"storage"`
}
