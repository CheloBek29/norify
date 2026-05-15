package main

import (
	"net/http"
	"strings"
	"sync"

	"github.com/norify/platform/packages/go-common/httpapi"
	"github.com/norify/platform/packages/go-common/templates"
)

var templateStore = struct {
	sync.Mutex
	items map[string]templates.Template
}{items: map[string]templates.Template{}}

func main() {
	mux := httpapi.NewMux(httpapi.Service{Name: "template-service", Version: "0.1.0"})
	mux.HandleFunc("/templates", templatesCollection)
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
