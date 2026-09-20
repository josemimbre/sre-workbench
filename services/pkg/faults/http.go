package faults

import (
	"encoding/json"
	"net/http"
	"os"
	"time"
)

// Handler returns the admin API. It is mounted outside the RED middleware on purpose:
// the control plane is not user traffic and must stay reachable while the service is
// being broken.
//
//	GET    /admin/faults      list active faults
//	POST   /admin/faults      add one
//	DELETE /admin/faults      remove all
//	DELETE /admin/faults/{id} remove one
//	POST   /admin/crash       exit the process
func (e *Engine) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /admin/faults", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, listResponse{Faults: e.List(), Now: time.Now()})
	})

	mux.HandleFunc("POST /admin/faults", func(w http.ResponseWriter, r *http.Request) {
		var in Fault
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "malformed body: " + err.Error()})
			return
		}
		f, err := e.Add(in)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, f)
	})

	mux.HandleFunc("DELETE /admin/faults", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]int{"removed": e.Clear()})
	})

	mux.HandleFunc("DELETE /admin/faults/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !e.Remove(r.PathValue("id")) {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "no such fault"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// A crash has no TTL and nothing to undo: the process dies and the container
	// restart policy brings it back, counters reset included. That reset is itself
	// worth seeing on a dashboard.
	mux.HandleFunc("POST /admin/crash", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "exiting"})
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.AfterFunc(300*time.Millisecond, func() { os.Exit(1) })
	})

	return mux
}

type listResponse struct {
	Faults []Fault   `json:"faults"`
	Now    time.Time `json:"now"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
