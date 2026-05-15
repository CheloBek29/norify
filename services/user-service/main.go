package main

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/norify/platform/packages/go-common/httpapi"
	"github.com/norify/platform/packages/go-common/users"
)

var seededUsers = users.SeedUsers(50000)

func main() {
	mux := httpapi.NewMux(httpapi.Service{Name: "user-service", Version: "0.1.0"})
	mux.HandleFunc("/users", listUsers)
	mux.HandleFunc("/users/count", countUsers)
	mux.HandleFunc("/users/import", importUsers)
	mux.HandleFunc("/segments/preview", previewSegment)
	_ = httpapi.Listen("user-service", mux)
}

func listUsers(w http.ResponseWriter, r *http.Request) {
	filtered := users.Filter(seededUsers, filterFromQuery(r))
	limit := intQuery(r, "limit", 50)
	if limit > len(filtered) {
		limit = len(filtered)
	}
	httpapi.WriteJSON(w, http.StatusOK, filtered[:limit])
}

func countUsers(w http.ResponseWriter, r *http.Request) {
	httpapi.WriteJSON(w, http.StatusOK, map[string]int{"count": users.PreviewCount(seededUsers, filterFromQuery(r))})
}

func previewSegment(w http.ResponseWriter, r *http.Request) {
	countUsers(w, r)
}

func importUsers(w http.ResponseWriter, r *http.Request) {
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "imported": 0})
}

func filterFromQuery(r *http.Request) users.FilterSpec {
	q := r.URL.Query()
	return users.FilterSpec{
		MinAge:   intQuery(r, "min_age", 0),
		MaxAge:   intQuery(r, "max_age", 0),
		Gender:   q.Get("gender"),
		Location: q.Get("location"),
		TagsAny:  splitCSV(q.Get("tags")),
	}
}

func intQuery(r *http.Request, key string, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return fallback
	}
	return value
}

func splitCSV(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}
