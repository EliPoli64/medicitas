package main

// Cliente mínimo de la Admin REST API de Keycloak (RF1 / RF2).
//
// La identidad se delega en Keycloak: dar de alta un usuario de dominio exige
// crear antes su identidad en el proveedor, porque "Usuario"."idpSub" es NOT
// NULL y UNIQUE. Este cliente usa el mismo mecanismo que el job de semilla:
// client `admin-cli` contra el realm master con las credenciales de arranque
// (KC_BOOTSTRAP_ADMIN_*).
//
// Alternativa considerada: un client dedicado con service account y solo el
// rol `realm-management/manage-users` (menor privilegio). Se dejó para el
// endurecimiento de Etapa 2 porque exige cambiar el realm importado y sus
// service-account role mappings; el trade-off queda documentado en
// docs/keycloak.md. La API nunca emite ni firma tokens de usuario: solo llama
// a esta API para provisionar (RF1/RF2), igual que el seed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// NewIDPUser describe la identidad a crear en Keycloak.
// ClinicaIdentificador viaja como atributo `clinica_id`, que el mapper del
// realm convierte en el claim del token (asociación identidad ↔ clínica).
type NewIDPUser struct {
	Username             string
	Password             string
	Email                string
	FirstName            string
	LastName             string
	ClinicaIdentificador string
	RealmRole            string
}

// IdentityAdmin abstrae el provisionamiento para tests sin Keycloak.
type IdentityAdmin interface {
	// CreateUser crea el usuario y le asigna el rol de realm. Devuelve el
	// sub (id de Keycloak) que se guarda en "Usuario"."idpSub".
	CreateUser(ctx context.Context, u NewIDPUser) (string, error)
	// DeleteUser elimina la identidad (compensación cuando falla la BD).
	DeleteUser(ctx context.Context, sub string) error
}

var (
	errIDPUserExists = errors.New("usuario ya existe en el proveedor de identidad")
	errIDPNotFound   = errors.New("usuario no encontrado en el proveedor de identidad")
)

// httpIdentityAdmin implementa IdentityAdmin contra la Admin API de Keycloak.
type httpIdentityAdmin struct {
	baseURL  string // p. ej. http://keycloak:6767
	realm    string // realm de la aplicación (medicitas)
	username string // admin de arranque
	password string
	client   *http.Client

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

func newHTTPIdentityAdminFromEnv() *httpIdentityAdmin {
	baseURL := os.Getenv("KC_ADMIN_URL")
	if baseURL == "" {
		baseURL = "http://keycloak:6767"
	}
	realm := os.Getenv("KC_REALM")
	if realm == "" {
		realm = "medicitas"
	}
	return &httpIdentityAdmin{
		baseURL:  strings.TrimRight(baseURL, "/"),
		realm:    realm,
		username: os.Getenv("KC_BOOTSTRAP_ADMIN_USERNAME"),
		password: os.Getenv("KC_BOOTSTRAP_ADMIN_PASSWORD"),
		client:   &http.Client{Timeout: 15 * time.Second},
	}
}

// adminToken obtiene (y cachea hasta expirar) un token de admin-cli contra el
// realm master. Es el mismo flujo que seed/keycloak_admin.py.
func (a *httpIdentityAdmin) adminToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && time.Now().Before(a.tokenExp) {
		return a.token, nil
	}
	form := url.Values{}
	form.Set("client_id", "admin-cli")
	form.Set("username", a.username)
	form.Set("password", a.password)
	form.Set("grant_type", "password")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.baseURL+"/realms/master/protocol/openid-connect/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("token de admin: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token de admin: HTTP %d: %s", resp.StatusCode, truncate(body, 300))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.AccessToken == "" {
		return "", fmt.Errorf("token de admin: respuesta sin access_token")
	}
	a.token = out.AccessToken
	// Margen de 30 s para no reusar un token a punto de expirar.
	a.tokenExp = time.Now().Add(time.Duration(out.ExpiresIn-30) * time.Second)
	return a.token, nil
}

// adminRequest ejecuta una llamada autenticada a la Admin API y devuelve el
// cuerpo. expected son los códigos de éxito aceptados.
func (a *httpIdentityAdmin) adminRequest(ctx context.Context, method, path string, payload any, expected ...int) (int, []byte, error) {
	tok, err := a.adminToken(ctx)
	if err != nil {
		return 0, nil, err
	}
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		body = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+"/admin/realms/"+url.PathEscape(a.realm)+path, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	for _, code := range expected {
		if resp.StatusCode == code {
			return resp.StatusCode, respBody, nil
		}
	}
	return resp.StatusCode, respBody, fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, truncate(respBody, 300))
}

// CreateUser crea el usuario en Keycloak con atributo clinica_id, credencial
// temporal=false y el rol de realm indicado. Si falla la asignación del rol,
// elimina el usuario recién creado (compensación local).
func (a *httpIdentityAdmin) CreateUser(ctx context.Context, u NewIDPUser) (string, error) {
	payload := map[string]any{
		"username":      u.Username,
		"enabled":       true,
		"email":         u.Email,
		"emailVerified": true,
		"firstName":     u.FirstName,
		"lastName":      u.LastName,
		"attributes":    map[string]any{"clinica_id": []string{u.ClinicaIdentificador}},
		"credentials": []any{map[string]any{
			"type":      "password",
			"value":     u.Password,
			"temporary": false,
		}},
	}
	status, _, err := a.adminRequest(ctx, http.MethodPost, "/users", payload, http.StatusCreated)
	if err != nil {
		if status == http.StatusConflict ||
			(status == http.StatusBadRequest && strings.Contains(err.Error(), "exists")) {
			return "", errIDPUserExists
		}
		return "", fmt.Errorf("crear usuario en Keycloak: %w", err)
	}

	sub, err := a.findUserID(ctx, u.Username)
	if err != nil {
		return "", err
	}
	if err := a.assignRealmRole(ctx, sub, u.RealmRole); err != nil {
		// Compensación: no dejar una identidad sin rol asignado.
		_ = a.DeleteUser(ctx, sub)
		return "", err
	}
	return sub, nil
}

// findUserID busca el id (sub) por username exacto. Keycloak no siempre
// devuelve la cabecera Location, así que se consulta por nombre.
func (a *httpIdentityAdmin) findUserID(ctx context.Context, username string) (string, error) {
	_, body, err := a.adminRequest(ctx, http.MethodGet,
		"/users?username="+url.QueryEscape(username)+"&exact=true", nil, http.StatusOK)
	if err != nil {
		return "", fmt.Errorf("buscar usuario en Keycloak: %w", err)
	}
	var users []struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(body, &users); err != nil {
		return "", err
	}
	for _, user := range users {
		if user.Username == username {
			return user.ID, nil
		}
	}
	return "", errIDPNotFound
}

// assignRealmRole agrega un rol de realm al usuario (idempotente).
func (a *httpIdentityAdmin) assignRealmRole(ctx context.Context, sub, roleName string) error {
	_, body, err := a.adminRequest(ctx, http.MethodGet,
		"/roles/"+url.PathEscape(roleName), nil, http.StatusOK)
	if err != nil {
		return fmt.Errorf("obtener rol %q: %w", roleName, err)
	}
	var role map[string]any
	if err := json.Unmarshal(body, &role); err != nil {
		return err
	}
	_, _, err = a.adminRequest(ctx, http.MethodPost,
		"/users/"+url.PathEscape(sub)+"/role-mappings/realm",
		[]any{role}, http.StatusNoContent)
	if err != nil {
		return fmt.Errorf("asignar rol %q: %w", roleName, err)
	}
	return nil
}

// DeleteUser elimina la identidad. 404 se tolera (ya no existe).
func (a *httpIdentityAdmin) DeleteUser(ctx context.Context, sub string) error {
	status, _, err := a.adminRequest(ctx, http.MethodDelete,
		"/users/"+url.PathEscape(sub), nil, http.StatusNoContent, http.StatusNotFound)
	if err != nil && status != http.StatusNotFound {
		return fmt.Errorf("eliminar usuario de Keycloak: %w", err)
	}
	return nil
}

func truncate(b []byte, n int) string {
	s := string(b)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
