package main

import (
	"net/http"
	"os"
	"strings"

	"github.com/norify/platform/packages/go-common/auth"
	"github.com/norify/platform/packages/go-common/httpapi"
)

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type manager struct {
	ID           string
	Email        string
	PasswordHash string
	Role         string
}

var jwtSecret = env("JWT_SECRET", "local-dev-secret")
var managers = map[string]manager{
	"admin@example.com":   {ID: "admin-1", Email: "admin@example.com", PasswordHash: auth.HashPassword("admin123"), Role: auth.RoleAdmin},
	"manager@example.com": {ID: "manager-1", Email: "manager@example.com", PasswordHash: auth.HashPassword("manager123"), Role: auth.RoleManager},
}

func main() {
	mux := httpapi.NewMux(httpapi.Service{Name: "auth-service", Version: "0.1.0"})
	mux.HandleFunc("/auth/login", login)
	mux.HandleFunc("/auth/refresh", refresh)
	mux.HandleFunc("/auth/logout", logout)
	mux.HandleFunc("/auth/me", me)
	mux.HandleFunc("/admin/managers", require(auth.RoleAdmin, listManagers))
	_ = httpapi.Listen("auth-service", mux)
}

func login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	var req loginRequest
	if err := httpapi.ReadJSON(r, &req); err != nil {
		httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	manager, ok := managers[req.Email]
	if !ok || !auth.CheckPassword(manager.PasswordHash, req.Password) {
		httpapi.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_credentials"})
		return
	}
	token, _ := auth.SignToken(auth.Claims{Subject: manager.ID, Email: manager.Email, Role: manager.Role}, jwtSecret)
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"access_token": token, "refresh_token": token, "token_type": "bearer"})
}

func refresh(w http.ResponseWriter, r *http.Request) {
	me(w, r)
}

func logout(w http.ResponseWriter, r *http.Request) {
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

func me(w http.ResponseWriter, r *http.Request) {
	claims, err := claimsFromRequest(r)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, claims)
}

func listManagers(w http.ResponseWriter, r *http.Request) {
	out := make([]map[string]string, 0, len(managers))
	for _, manager := range managers {
		out = append(out, map[string]string{"id": manager.ID, "email": manager.Email, "role": manager.Role})
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func require(role string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, err := claimsFromRequest(r)
		if err != nil || claims.Role != role {
			httpapi.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		next(w, r)
	}
}

func claimsFromRequest(r *http.Request) (auth.Claims, error) {
	header := r.Header.Get("Authorization")
	token := strings.TrimPrefix(header, "Bearer ")
	return auth.VerifyToken(token, jwtSecret)
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
