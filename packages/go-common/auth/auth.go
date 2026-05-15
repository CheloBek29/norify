package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

const (
	RoleManager = "manager"
	RoleAdmin   = "admin"
)

type Claims struct {
	Subject string `json:"sub"`
	Email   string `json:"email"`
	Role    string `json:"role"`
}

func SignToken(claims Claims, secret string) (string, error) {
	if secret == "" {
		return "", errors.New("secret is required")
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	sig := sign(encoded, secret)
	return encoded + "." + sig, nil
}

func VerifyToken(token, secret string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return Claims{}, errors.New("invalid token")
	}
	if !hmac.Equal([]byte(parts[1]), []byte(sign(parts[0], secret))) {
		return Claims{}, errors.New("invalid signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, err
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Claims{}, err
	}
	return claims, nil
}

func Can(role, action string) bool {
	if role == RoleAdmin {
		return true
	}
	managerActions := map[string]bool{
		"campaigns:create":      true,
		"campaigns:start":       true,
		"campaigns:cancel":      true,
		"campaigns:retry":       true,
		"templates:crud":        true,
		"users:preview":         true,
		"channels:list":         true,
		"deliveries:list":       true,
		"status:read":           true,
		"error-actions:execute": true,
	}
	return managerActions[action]
}

func sign(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func HashPassword(password string) string {
	sum := sha256.Sum256([]byte("norify:" + password))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func CheckPassword(hash, password string) bool {
	return hmac.Equal([]byte(hash), []byte(HashPassword(password)))
}
