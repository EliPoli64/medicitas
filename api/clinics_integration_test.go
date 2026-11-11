//go:build integration

package main

// Integración RF1 contra la pila real (docker compose up + go test -tags=integration).
// Comprueba el ciclo completo: alta con identidad provisionada en Keycloak,
// login del administrador nuevo y lectura de su clínica en /me.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestIntegrationRF1AltaClinica(t *testing.T) {
	apiBase := envOr("API_BASE", "http://localhost:8080")
	tokenURL := envOr("KC_TOKEN_URL", "http://localhost:6767/realms/medicitas/protocol/openid-connect/token")
	client := &http.Client{Timeout: 15 * time.Second}

	waitReady(t, client, apiBase+"/ready")

	// Identificador único por corrida para no chocar con el seed.
	ident := fmt.Sprintf("clinica-demo-%d", time.Now().Unix()%1000000)
	username := "adm-demo-" + fmt.Sprintf("%d", time.Now().Unix()%1000000)
	body := fmt.Sprintf(`{
		"nombre": "Clínica Demo",
		"identificador": %q,
		"zonaHoraria": "America/Costa_Rica",
		"administrador": {"username": %q, "password": "demo12345"}
	}`, ident, username)

	// 1) Sin token: 401.
	req, _ := http.NewRequest(http.MethodPost, apiBase+"/clinicas", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /clinicas: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sin token = %d, want 401", resp.StatusCode)
	}

	// 2) Con rol que no corresponde (recepción): 403.
	recepTok := fetchToken(t, client, tokenURL, "recep-norte", "recep123")
	req, _ = http.NewRequest(http.MethodPost, apiBase+"/clinicas", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+recepTok)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("POST /clinicas: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("recepción = %d, want 403", resp.StatusCode)
	}

	// 3) Con plat-admin: 201 + clínica y administrador creados.
	platTok := fetchToken(t, client, tokenURL, "plat-admin", "plat123")
	req, _ = http.NewRequest(http.MethodPost, apiBase+"/clinicas", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+platTok)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("POST /clinicas: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("alta = %d body=%v", resp.StatusCode, out)
	}
	clinica, _ := out["clinica"].(map[string]any)
	admin, _ := out["administrador"].(map[string]any)
	if clinica["identificador"] != ident {
		t.Fatalf("clínica = %v", clinica)
	}
	if admin["rolCodigo"] != "adm_clinica" {
		t.Fatalf("admin = %v", admin)
	}

	// 4) El administrador nuevo loguea contra Keycloak y /me lo ancla a su clínica.
	admTok := fetchToken(t, client, tokenURL, username, "demo12345")
	me := getJSON(t, client, apiBase+"/me", admTok)
	if me["clinicaIdentificador"] != ident {
		t.Fatalf("/me del admin = %v, want %s", me, ident)
	}

	// 5) Duplicado: 409 y sin provisionar de nuevo.
	req, _ = http.NewRequest(http.MethodPost, apiBase+"/clinicas", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+platTok)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("POST /clinicas: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicado = %d, want 409", resp.StatusCode)
	}

	// 6) Entrada inválida: 400 y el servicio sigue operativo.
	req, _ = http.NewRequest(http.MethodPost, apiBase+"/clinicas", bytes.NewReader([]byte(`{"nombre":""}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+platTok)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("POST /clinicas: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("inválida = %d, want 400", resp.StatusCode)
	}
	if code, _ := getRaw(t, client, apiBase+"/health", ""); code != http.StatusOK {
		t.Fatalf("servicio caído tras errores: %d", code)
	}
}
