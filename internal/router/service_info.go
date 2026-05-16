package router

import (
	"net/http"

	"dropoutbox/internal/buildinfo"
)

func registerServiceInfoRoute(mux *http.ServeMux, info buildinfo.Info, svc services) {
	mux.HandleFunc("GET /api/{$}", func(w http.ResponseWriter, r *http.Request) {
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

		writeJSON(w, http.StatusOK, serviceInfoBody{
			Service:   "dropoutbox",
			Version:   info.Version,
			Commit:    info.Commit,
			BuildDate: info.BuildDate,
		})
	})
}

type serviceInfoBody struct {
	Service   string `json:"service"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
}
