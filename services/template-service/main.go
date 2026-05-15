package main

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/norify/platform/packages/go-common/httpapi"
	appruntime "github.com/norify/platform/packages/go-common/runtime"
	"github.com/norify/platform/packages/go-common/templates"
)

var templateStore = struct {
	sync.Mutex
	items map[string]templates.Template
}{items: map[string]templates.Template{}}

type templateVariable struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Source string `json:"source"`
}

var db *pgxpool.Pool

func main() {
	ctx := context.Background()
	var err error
	db, err = appruntime.OpenPostgres(ctx)
	appruntime.LogStartup("template-service postgres", err)
	if db != nil {
		defer db.Close()
	}

	mux := httpapi.NewMux(httpapi.Service{Name: "template-service", Version: "0.2.0"})
	mux.HandleFunc("/templates", templatesCollection)
	mux.HandleFunc("/templates/variables", templateVariables)
	mux.HandleFunc("/templates/", templateItem)
	_ = httpapi.Listen("template-service", mux)
}

func templatesCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		templateStore.Lock()
		defer templateStore.Unlock()
		out := make([]templates.Template, 0, len(templateStore.items))
		for _, item := range templateStore.items {
			out = append(out, item)
		}
		httpapi.WriteJSON(w, http.StatusOK, out)
	case http.MethodPost:
		var tpl templates.Template
		if err := httpapi.ReadJSON(r, &tpl); err != nil {
			httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
			return
		}
		if err := templates.Validate(tpl); err != nil {
			httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if tpl.ID == "" {
			tpl.ID = "template-" + tpl.Name
		}
		tpl.Version = templates.NextVersion(0)
		templateStore.Lock()
		templateStore.items[tpl.ID] = tpl
		templateStore.Unlock()
		httpapi.WriteJSON(w, http.StatusCreated, tpl)
	default:
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
	}
}

func templateVariables(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	out, err := listUserColumns(r.Context())
	if err != nil {
		httpapi.WriteJSON(w, http.StatusOK, fallbackUserColumns())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func listUserColumns(ctx context.Context) ([]templateVariable, error) {
	if db == nil {
		return nil, http.ErrServerClosed
	}
	rows, err := db.Query(ctx, `
		SELECT column_name, data_type
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'users'
		ORDER BY ordinal_position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []templateVariable{}
	for rows.Next() {
		var item templateVariable
		item.Source = "users"
		if err := rows.Scan(&item.Name, &item.Type); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func fallbackUserColumns() []templateVariable {
	names := []struct {
		name string
		typ  string
	}{
		{"id", "text"},
		{"email", "text"},
		{"phone", "text"},
		{"telegram_id", "text"},
		{"vk_id", "text"},
		{"custom_app_id", "text"},
		{"age", "integer"},
		{"gender", "text"},
		{"location", "text"},
		{"tags", "ARRAY"},
		{"created_at", "timestamp with time zone"},
		{"updated_at", "timestamp with time zone"},
	}
	out := make([]templateVariable, 0, len(names))
	for _, item := range names {
		out = append(out, templateVariable{Name: item.name, Type: item.typ, Source: "users"})
	}
	return out
}

func templateItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/templates/")
	templateStore.Lock()
	defer templateStore.Unlock()
	tpl, ok := templateStore.items[id]
	if !ok {
		httpapi.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		httpapi.WriteJSON(w, http.StatusOK, tpl)
	case http.MethodPut:
		var updated templates.Template
		if err := httpapi.ReadJSON(r, &updated); err != nil {
			httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
			return
		}
		if err := templates.Validate(updated); err != nil {
			httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		updated.ID = id
		updated.Version = templates.NextVersion(tpl.Version)
		templateStore.items[id] = updated
		httpapi.WriteJSON(w, http.StatusOK, updated)
	case http.MethodDelete:
		delete(templateStore.items, id)
		httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
	}
}
