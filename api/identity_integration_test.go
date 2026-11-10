//go:build integration

package main

// Prueba de integración contra Compose real (Keycloak + API + Postgres).
//
// El token debe pedirse al mismo host que KC_ISSUER_URL (localhost). Si se pide
// a http://keycloak:6767 el iss del JWT no coincide y /me responde 401.
//
//	docker compose up -d --build
//	go test -tags=integration -count=1 -run TestIntegrationIdentity ./...
//
// Desde un contenedor (Docker Desktop):
//
//	API_BASE=http://host.docker.internal:8080
//	KC_TOKEN_URL=http://host.docker.internal:6767/realms/medicitas/protocol/openid-connect/token
//
// Variables opcionales: API_BASE, KC_TOKEN_URL, SMOKE_USER, SMOKE_PASS.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestIntegrationIdentityStack(t *testing.T) {
	apiBase := envOr("API_BASE", "http://localhost:8080")
	tokenURL := envOr("KC_TOKEN_URL", "http://localhost:6767/realms/medicitas/protocol/openid-connect/token")
	user := envOr("SMOKE_USER", "recep-norte")
	pass := envOr("SMOKE_PASS", "recep123")

	client := &http.Client{Timeout: 10 * time.Second}

	waitReady(t, client, apiBase+"/ready")

	tok := fetchToken(t, client, tokenURL, user, pass)

	// /me
	me := getJSON(t, client, apiBase+"/me", tok)
	if me["username"] != user {
		t.Fatalf("/me username=%v want %s", me["username"], user)
	}
	if me["usuarioId"] == nil || me["usuarioId"] == "" {
		t.Fatalf("/me sin usuarioId de BD: %v", me)
	}
	if me["clinicaIdentificador"] != "clinica-norte" {
		t.Fatalf("/me clinicaIdentificador=%v", me["clinicaIdentificador"])
	}

	// /pacientes + I5
	pac := getJSON(t, client, apiBase+"/pacientes?clinicaId=clinica-sur", tok)
	if pac["clinicaIdentificador"] != "clinica-norte" {
		t.Fatalf("I5: clinica=%v", pac["clinicaIdentificador"])
	}
	lista, ok := pac["pacientes"].([]any)
	if !ok || len(lista) == 0 {
		t.Fatalf("esperaba pacientes norte: %v", pac)
	}

	// plat-admin no lee pacientes
	platTok := fetchToken(t, client, tokenURL, "plat-admin", "plat123")
	code, _ := getRaw(t, client, apiBase+"/pacientes", platTok)
	if code != http.StatusForbidden {
		t.Fatalf("plat /pacientes=%d want 403", code)
	}

	// sin token
	code, _ = getRaw(t, client, apiBase+"/me", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("/me sin token=%d want 401", code)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func waitReady(t *testing.T, client *http.Client, readyURL string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(readyURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("API no ready: %s", readyURL)
}

func fetchToken(t *testing.T, client *http.Client, tokenURL, user, pass string) string {
	t.Helper()
	form := url.Values{}
	form.Set("client_id", "api")
	form.Set("username", user)
	form.Set("password", pass)
	form.Set("grant_type", "password")
	resp, err := client.Post(tokenURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token status=%d body=%s", resp.StatusCode, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	tok, _ := out["access_token"].(string)
	if tok == "" {
		t.Fatalf("sin access_token: %s", body)
	}
	return tok
}

func getJSON(t *testing.T, client *http.Client, rawURL, token string) map[string]any {
	t.Helper()
	code, body := getRaw(t, client, rawURL, token)
	if code != http.StatusOK {
		t.Fatalf("GET %s status=%d body=%s", rawURL, code, body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func getRaw(t *testing.T, client *http.Client, rawURL, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
