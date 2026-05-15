package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"time"
)

type Service struct {
	Name    string
	Version string
	Ready   func() bool
}

func NewMux(service Service) *http.ServeMux {
	if service.Ready == nil {
		service.Ready = func() bool { return true }
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health/live", func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "live", "service": service.Name})
	})
	mux.HandleFunc("/health/ready", func(w http.ResponseWriter, r *http.Request) {
		if !service.Ready() {
			WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "service": service.Name})
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ready", "service": service.Name})
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("service_live_total{service=\"" + service.Name + "\"} 1\n"))
	})
	return mux
}

func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func ReadJSON(r *http.Request, dest any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(dest)
}

func Listen(serviceName string, mux *http.ServeMux) error {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           requestLog(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	slog.Info("service started", "service", serviceName, "addr", server.Addr)
	return server.ListenAndServe()
}

func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		start := time.Now()
		next.ServeHTTP(w, r)
		slog.Info("http request", "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(start).Milliseconds())
	})
}
