package auth

import "testing"

func TestTokenRoundTripAndRBAC(t *testing.T) {
	token, err := SignToken(Claims{Subject: "m1", Email: "manager@example.com", Role: RoleManager}, "secret")
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	claims, err := VerifyToken(token, "secret")
	if err != nil {
		t.Fatalf("verify token: %v", err)
	}
	if claims.Subject != "m1" || claims.Email != "manager@example.com" || claims.Role != RoleManager {
		t.Fatalf("unexpected claims: %#v", claims)
	}
	if Can(RoleManager, "channels:disable") {
		t.Fatal("manager must not disable channels")
	}
	if !Can(RoleAdmin, "channels:disable") {
		t.Fatal("admin must disable channels")
	}
}

func TestWrongSecretFails(t *testing.T) {
	token, err := SignToken(Claims{Subject: "a1", Email: "admin@example.com", Role: RoleAdmin}, "secret")
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	if _, err := VerifyToken(token, "wrong"); err == nil {
		t.Fatal("expected verification failure")
	}
}
