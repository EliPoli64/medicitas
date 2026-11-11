//go:build integration

package main

// Integración RF2–RF4 contra la pila real (docker compose up +
// go test -tags=integration ./...).
//
// Ejercita el ciclo completo del catálogo de una clínica con el admin creado
// por RF1: alta/baja/cambio de rol de usuarios (con identidad real en
// Keycloak), servicios con su especialidad y profesionales con sus servicios
// habilitados. Comprueba además el aislamiento (I5): un id de otra clínica
// responde 404 y los listados ignoran ?clinicaId= manipulado.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestIntegrationRF2aRF4Catalogo(t *testing.T) {
	apiBase := envOr("API_BASE", "http://localhost:8080")
	tokenURL := envOr("KC_TOKEN_URL", "http://localhost:6767/realms/medicitas/protocol/openid-connect/token")
	client := &http.Client{Timeout: 15 * time.Second}

	waitReady(t, client, apiBase+"/ready")

	// --- Fixtures: clínica propia del test (RF1) y su admin ---
	suffix := fmt.Sprintf("%d", time.Now().Unix()%1000000)
	ident := "clinica-cat-" + suffix
	admUser := "adm-cat-" + suffix
	platTok := fetchToken(t, client, tokenURL, "plat-admin", "plat123")

	altaBody := fmt.Sprintf(`{
		"nombre": "Clínica Catálogo",
		"identificador": %q,
		"administrador": {"username": %q, "password": "cat12345"}
	}`, ident, admUser)
	code, raw := postJSON(t, client, apiBase+"/clinicas", platTok, altaBody)
	if code != http.StatusCreated {
		t.Fatalf("alta clínica = %d body=%s", code, raw)
	}
	admTok := fetchToken(t, client, tokenURL, admUser, "cat12345")

	// --- RF2: alta de usuarios ---
	nuevoUser := "recep-cat-" + suffix
	body := fmt.Sprintf(`{"username":%q,"password":"cat12345","rol":"recepcion"}`, nuevoUser)
	code, raw = postJSON(t, client, apiBase+"/usuarios", admTok, body)
	if code != http.StatusCreated {
		t.Fatalf("alta usuario = %d body=%s", code, raw)
	}
	var alta struct {
		ClinicaIdentificador string   `json:"clinicaIdentificador"`
		Usuario              AdminUser `json:"usuario"`
	}
	if err := json.Unmarshal([]byte(raw), &alta); err != nil {
		t.Fatal(err)
	}
	if alta.ClinicaIdentificador != ident {
		t.Fatalf("I5: clínica = %s, want %s", alta.ClinicaIdentificador, ident)
	}
	if alta.Usuario.RolCodigo != "recepcion" || !alta.Usuario.Activo {
		t.Fatalf("usuario = %+v", alta.Usuario)
	}

	// La identidad nueva loguea y queda anclada a la clínica (clave de RF2).
	nuevoTok := fetchToken(t, client, tokenURL, nuevoUser, "cat12345")
	me := getJSON(t, client, apiBase+"/me", nuevoTok)
	if me["clinicaIdentificador"] != ident {
		t.Fatalf("/me = %v", me)
	}

	// RF2: rol no asignable → 400; duplicado → 409.
	code, _ = postJSON(t, client, apiBase+"/usuarios", admTok,
		fmt.Sprintf(`{"username":"plat-x-%s","password":"cat12345","rol":"adm_plataforma"}`, suffix))
	if code != http.StatusBadRequest {
		t.Fatalf("rol no asignable = %d", code)
	}
	code, _ = postJSON(t, client, apiBase+"/usuarios", admTok, body)
	if code != http.StatusConflict {
		t.Fatalf("duplicado = %d", code)
	}

	// RF2: baja lógica → el usuario inactivo no loguea (401/403).
	userID := alta.Usuario.ID
	code, raw = doJSONRaw(t, client, http.MethodPatch, apiBase+"/usuarios/"+userID, admTok, `{"activo":false}`)
	if code != http.StatusOK {
		t.Fatalf("baja lógica = %d body=%s", code, raw)
	}
	code, _ = getTokenRaw(t, client, tokenURL, nuevoUser, "cat12345")
	if code == http.StatusOK {
		// Puede loguear, pero /me debe rechazarlo como usuario inactivo.
		tok, err := getToken(t, client, tokenURL, nuevoUser, "cat12345")
		if err == nil {
			code, _ = getRaw(t, client, apiBase+"/me", tok)
			if code != http.StatusForbidden {
				t.Fatalf("/me de inactivo = %d, want 403", code)
			}
		}
	}

	// RF2: reactivación + cambio de rol (reflejado en el token).
	code, _ = doJSONRaw(t, client, http.MethodPatch, apiBase+"/usuarios/"+userID, admTok, `{"activo":true,"rol":"profesional"}`)
	if code != http.StatusOK {
		t.Fatalf("cambio de rol = %d", code)
	}
	tokRol := fetchToken(t, client, tokenURL, nuevoUser, "cat12345")
	me = getJSON(t, client, apiBase+"/me", tokRol)
	if me["rolCodigo"] != "profesional" {
		t.Fatalf("/me tras cambio de rol = %v", me)
	}

	// --- RF3: catálogo de servicios ---
	espRaw := getJSON(t, client, apiBase+"/especialidades", admTok)
	var esp struct {
		Especialidades []Especialidad `json:"especialidades"`
	}
	espJSON, _ := json.Marshal(espRaw)
	if err := json.Unmarshal(espJSON, &esp); err != nil || len(esp.Especialidades) == 0 {
		t.Fatalf("especialidades = %v", espRaw)
	}
	var espID string
	for _, e := range esp.Especialidades {
		if e.Activo {
			espID = e.ID
			break
		}
	}
	if espID == "" {
		t.Fatal("sin especialidades activas en la semilla")
	}

	svcBody := fmt.Sprintf(`{"nombre":"Consulta cat %s","duracionMinutos":30,"especialidadId":%q}`, suffix, espID)
	code, raw = postJSON(t, client, apiBase+"/servicios", admTok, svcBody)
	if code != http.StatusCreated {
		t.Fatalf("alta servicio = %d body=%s", code, raw)
	}
	var svcOut struct {
		ClinicaIdentificador string  `json:"clinicaIdentificador"`
		Servicio             Service `json:"servicio"`
	}
	if err := json.Unmarshal([]byte(raw), &svcOut); err != nil {
		t.Fatal(err)
	}
	svcID := svcOut.Servicio.ID

	// RF3: duración inválida → 400; nombre duplicado → 409.
	code, _ = postJSON(t, client, apiBase+"/servicios", admTok,
		fmt.Sprintf(`{"nombre":"X %s","duracionMinutos":0,"especialidadId":%q}`, suffix, espID))
	if code != http.StatusBadRequest {
		t.Fatalf("duración inválida = %d", code)
	}
	code, _ = postJSON(t, client, apiBase+"/servicios", admTok, svcBody)
	if code != http.StatusConflict {
		t.Fatalf("servicio duplicado = %d", code)
	}

	// RF3: actualización (mantiene el catálogo).
	code, raw = doJSONRaw(t, client, http.MethodPatch, apiBase+"/servicios/"+svcID, admTok, `{"duracionMinutos":45}`)
	if code != http.StatusOK {
		t.Fatalf("update servicio = %d body=%s", code, raw)
	}

	// --- RF4: profesional + servicios habilitados ---
	// El usuario del rol cambiado arriba ya es profesional.
	profBody := fmt.Sprintf(`{"usuarioId":%q,"especialidadId":%q}`, userID, espID)
	code, raw = postJSON(t, client, apiBase+"/profesionales", admTok, profBody)
	if code != http.StatusCreated {
		t.Fatalf("alta profesional = %d body=%s", code, raw)
	}
	var profOut struct {
		ClinicaIdentificador string       `json:"clinicaIdentificador"`
		Profesional          Professional `json:"profesional"`
	}
	if err := json.Unmarshal([]byte(raw), &profOut); err != nil {
		t.Fatal(err)
	}
	profID := profOut.Profesional.ID

	// RF4: asociar servicios habilitados (duplicado en la lista se normaliza).
	code, raw = doJSONRaw(t, client, http.MethodPut, apiBase+"/profesionales/"+profID+"/servicios", admTok,
		fmt.Sprintf(`{"servicioIds":[%q,%q]}`, svcID, svcID))
	if code != http.StatusOK {
		t.Fatalf("set servicios = %d body=%s", code, raw)
	}

	// RF4: servicio de otra especialidad → 400 (coherencia con I3/RF4).
	// Se crea un servicio de otra especialidad para el intento.
	var otraEsp string
	for _, e := range esp.Especialidades {
		if e.Activo && e.ID != espID {
			otraEsp = e.ID
			break
		}
	}
	if otraEsp != "" {
		otroBody := fmt.Sprintf(`{"nombre":"Otra esp %s","duracionMinutos":30,"especialidadId":%q}`, suffix, otraEsp)
		code, raw = postJSON(t, client, apiBase+"/servicios", admTok, otroBody)
		if code != http.StatusCreated {
			t.Fatalf("servicio otra esp = %d body=%s", code, raw)
		}
		var otroOut struct {
			Servicio Service `json:"servicio"`
		}
		if err := json.Unmarshal([]byte(raw), &otroOut); err != nil {
			t.Fatal(err)
		}
		code, _ = doJSONRaw(t, client, http.MethodPut, apiBase+"/profesionales/"+profID+"/servicios", admTok,
			fmt.Sprintf(`{"servicioIds":[%q]}`, otroOut.Servicio.ID))
		if code != http.StatusBadRequest {
			t.Fatalf("servicio incompatible = %d, want 400", code)
		}
	}

	// RF4: el listado muestra el profesional con sus servicios (I5: solo su clínica).
	lista := getJSON(t, client, apiBase+"/profesionales?clinicaId=clinica-sur", admTok)
	if lista["clinicaIdentificador"] != ident {
		t.Fatalf("I5: listado = %v", lista["clinicaIdentificador"])
	}
	var profList struct {
		Profesionales []Professional `json:"profesionales"`
	}
	plJSON, _ := json.Marshal(lista)
	if err := json.Unmarshal(plJSON, &profList); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range profList.Profesionales {
		if p.ID == profID && len(p.Servicios) > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("profesional con servicios no aparece: %+v", profList.Profesionales)
	}

	// --- I5: recursos de otra clínica responden 404 ---
	// El id del usuario de la clínica del seed (clinica-norte) no es visible.
	// Se usa un id arbitrario: la respuesta correcta es 404, nunca datos.
	code, raw = doJSONRaw(t, client, http.MethodPatch, apiBase+"/usuarios/00000000-0000-0000-0000-000000000000", admTok, `{"activo":false}`)
	if code != http.StatusNotFound {
		t.Fatalf("usuario ajeno = %d body=%s", code, raw)
	}
	code, raw = doJSONRaw(t, client, http.MethodPut, apiBase+"/profesionales/00000000-0000-0000-0000-000000000000/servicios", admTok, `{"servicioIds":[]}`)
	if code != http.StatusNotFound {
		t.Fatalf("profesional ajeno = %d body=%s", code, raw)
	}

	// --- Matriz: un rol sin permiso no gestiona el catálogo ---
	recepTok := fetchToken(t, client, tokenURL, "recep-norte", "recep123")
	for _, path := range []string{"/usuarios", "/servicios", "/profesionales"} {
		code, _ = postJSON(t, client, apiBase+path, recepTok, "{}")
		if code != http.StatusForbidden {
			t.Fatalf("recepción en %s = %d, want 403", path, code)
		}
	}

	// El servicio sigue operativo.
	if code, _ := getRaw(t, client, apiBase+"/health", ""); code != http.StatusOK {
		t.Fatalf("servicio caído: %d", code)
	}
	_ = strings.Builder{}
}

// --- Helpers HTTP con cuerpo ---

func postJSON(t *testing.T, client *http.Client, url, token, body string) (int, string) {
	t.Helper()
	return doJSONRaw(t, client, http.MethodPost, url, token, body)
}

func doJSONRaw(t *testing.T, client *http.Client, method, url, token, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.String()
}

func getToken(t *testing.T, client *http.Client, tokenURL, user, pass string) (string, error) {
	t.Helper()
	form := fmt.Sprintf("client_id=api&username=%s&password=%s&grant_type=password", user, pass)
	resp, err := client.Post(tokenURL, "application/x-www-form-urlencoded", strings.NewReader(form))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	tok, _ := out["access_token"].(string)
	return tok, nil
}

func getTokenRaw(t *testing.T, client *http.Client, tokenURL, user, pass string) (int, string) {
	t.Helper()
	form := fmt.Sprintf("client_id=api&username=%s&password=%s&grant_type=password", user, pass)
	resp, err := client.Post(tokenURL, "application/x-www-form-urlencoded", strings.NewReader(form))
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.String()
}
