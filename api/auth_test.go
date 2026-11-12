package main

// Unit tests for Keycloak auth. No Docker: throwaway RSA + httptest JWKS.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

const (
	testKid    = "test-key"
	testIssuer = "http://test/realms/medicitas"
	testRole   = "recepcion"
)

type testKeys struct {
	priv *rsa.PrivateKey
}

func newTestKeys(t *testing.T) *testKeys {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &testKeys{priv: priv}
}

func b64urlBig(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func (tk *testKeys) jwksServer() *httptest.Server {
	n := b64urlBig(tk.priv.N.Bytes())
	e := b64urlBig(big.NewInt(int64(tk.priv.E)).Bytes())
	body := fmt.Sprintf(`{"keys":[{"kty":"RSA","kid":%q,"n":%q,"e":%q}]}`, testKid, n, e)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
}

func withTestIssuer(t *testing.T, jwksURL string) {
	t.Helper()
	oldIssuer, oldJWKS := kcIssuer, kcJWKSURL
	kcIssuer, kcJWKSURL = testIssuer, jwksURL
	jwksCache.Lock()
	jwksCache.keys = make(map[string]*rsa.PublicKey)
	jwksCache.lastFetch = time.Time{}
	jwksCache.Unlock()
	t.Cleanup(func() {
		kcIssuer, kcJWKSURL = oldIssuer, oldJWKS
		jwksCache.Lock()
		jwksCache.keys = make(map[string]*rsa.PublicKey)
		jwksCache.lastFetch = time.Time{}
		jwksCache.Unlock()
	})
}

func (tk *testKeys) mint(issuer string, exp time.Time, withExp bool, roles []string, kid string, clinicaID string) string {
	return tk.mintWithSub(issuer, exp, withExp, roles, kid, clinicaID, "test-user")
}

func (tk *testKeys) mintWithSub(issuer string, exp time.Time, withExp bool, roles []string, kid, clinicaID, sub string) string {
	claims := jwt.MapClaims{"iss": issuer, "sub": sub, "preferred_username": "tester"}
	if withExp {
		claims["exp"] = exp.Unix()
	}
	if roles != nil {
		rs := make([]any, len(roles))
		for i, r := range roles {
			rs[i] = r
		}
		claims["realm_access"] = map[string]any{"roles": rs}
	}
	if clinicaID != "" {
		claims["clinica_id"] = clinicaID
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(tk.priv)
	if err != nil {
		panic(err)
	}
	return s
}

func probeRequireRole(role string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/x", requireRole(role), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return r
}

func probeAuthRequired() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/me", authRequired(), func(c *gin.Context) {
		id, _ := identityFromContext(c)
		c.JSON(http.StatusOK, id)
	})
	return r
}

func doGet(t *testing.T, r http.Handler, path, token string) (int, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code, w
}

func TestAuthNoHeader401(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	withTestIssuer(t, srv.URL)
	if got, _ := doGet(t, probeRequireRole(testRole), "/x", ""); got != http.StatusUnauthorized {
		t.Fatalf("sin header: got %d, want 401", got)
	}
}

func TestAuthGarbage401(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	withTestIssuer(t, srv.URL)
	for _, bad := range []string{"basura", "a.b.c"} {
		got, _ := doGet(t, probeRequireRole(testRole), "/x", bad)
		if got != http.StatusUnauthorized {
			t.Fatalf("token %q: got %d, want 401", bad, got)
		}
	}
}

func TestAuthExpired401(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	withTestIssuer(t, srv.URL)
	tok := tk.mint(testIssuer, time.Now().Add(-time.Hour), true, []string{testRole}, testKid, "clinica-norte")
	if got, _ := doGet(t, probeRequireRole(testRole), "/x", tok); got != http.StatusUnauthorized {
		t.Fatalf("expirado: got %d, want 401", got)
	}
}

func TestAuthWrongIssuer401(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	withTestIssuer(t, srv.URL)
	tok := tk.mint("http://evil/realms/x", time.Now().Add(time.Hour), true, []string{testRole}, testKid, "")
	if got, _ := doGet(t, probeRequireRole(testRole), "/x", tok); got != http.StatusUnauthorized {
		t.Fatalf("issuer ajeno: got %d, want 401", got)
	}
}

func TestAuthNoRole403(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	withTestIssuer(t, srv.URL)
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, nil, testKid, "")
	if got, _ := doGet(t, probeRequireRole(testRole), "/x", tok); got != http.StatusForbidden {
		t.Fatalf("sin roles: got %d, want 403", got)
	}
	tok = tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{"otro-rol"}, testKid, "")
	if got, _ := doGet(t, probeRequireRole(testRole), "/x", tok); got != http.StatusForbidden {
		t.Fatalf("rol distinto: got %d, want 403", got)
	}
}

func TestAuthWithRolePasses(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	withTestIssuer(t, srv.URL)
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{"otro", testRole}, testKid, "clinica-norte")
	if got, _ := doGet(t, probeRequireRole(testRole), "/x", tok); got != http.StatusOK {
		t.Fatalf("con rol: got %d, want 200", got)
	}
}

func TestMeReturnsClinicaID(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	withTestIssuer(t, srv.URL)
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{testRole}, testKid, "clinica-norte")
	code, w := doGet(t, probeAuthRequired(), "/me", tok)
	if code != http.StatusOK {
		t.Fatalf("got %d, want 200", code)
	}
	var id Identity
	if err := json.Unmarshal(w.Body.Bytes(), &id); err != nil {
		t.Fatal(err)
	}
	if id.ClinicaID != "clinica-norte" {
		t.Fatalf("clinicaId=%q, want clinica-norte", id.ClinicaID)
	}
	if id.Username != "tester" {
		t.Fatalf("username=%q", id.Username)
	}
}

func TestHasRoleTable(t *testing.T) {
	cases := []struct {
		name   string
		claims jwt.MapClaims
		want   bool
	}{
		{"presente", jwt.MapClaims{"realm_access": map[string]any{"roles": []any{"a", testRole}}}, true},
		{"ausente", jwt.MapClaims{"realm_access": map[string]any{"roles": []any{"a"}}}, false},
		{"sin claim", jwt.MapClaims{}, false},
	}
	for _, tc := range cases {
		if got := hasRole(tc.claims, testRole); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAuthTokenSinKid401(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	withTestIssuer(t, srv.URL)
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss":          testIssuer,
		"sub":          "test-user",
		"exp":          time.Now().Add(time.Hour).Unix(),
		"realm_access": map[string]any{"roles": []any{testRole}},
	})
	s, err := tok.SignedString(tk.priv)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := doGet(t, probeRequireRole(testRole), "/x", s); got != http.StatusUnauthorized {
		t.Fatalf("token sin kid: got %d, want 401", got)
	}
}

func probeRequirePerm(op Operation) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/x", requirePerm(op), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return r
}

func TestRequirePermForbidden(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	withTestIssuer(t, srv.URL)
	// recepción no puede AltaClinica
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")
	if got, _ := doGet(t, probeRequirePerm(OpAltaClinica), "/x", tok); got != http.StatusForbidden {
		t.Fatalf("recepción AltaClinica: got %d, want 403", got)
	}
}

func TestRequirePermAllowed(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	withTestIssuer(t, srv.URL)
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")
	if got, _ := doGet(t, probeRequirePerm(OpReservarReprogramarCancelar), "/x", tok); got != http.StatusOK {
		t.Fatalf("recepción reservar: got %d, want 200", got)
	}
}

func TestRequirePermUnauthorized(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	withTestIssuer(t, srv.URL)
	if got, _ := doGet(t, probeRequirePerm(OpBitacora), "/x", ""); got != http.StatusUnauthorized {
		t.Fatalf("sin token: got %d, want 401", got)
	}
}

func TestHealthReadyMePermisosRouter(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	withTestIssuer(t, srv.URL)

	prevPing := pingDBFn
	pingDBFn = func(context.Context) error { return nil }
	t.Cleanup(func() { pingDBFn = prevPing })

	clinica := "clinica-norte"
	prevStore := userStore
	userStore = &fakeUserStore{bySub: map[string]DomainUser{
		"test-user": {
			UsuarioID:            "uuid-test",
			IdpSub:               "test-user",
			Activo:               true,
			ClinicaIdentificador: &clinica,
			RolCodigo:            "recepcion",
		},
	}}
	t.Cleanup(func() { userStore = prevStore })

	gin.SetMode(gin.TestMode)
	r := setupRouter()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/health: %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/ready", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/ready: %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/me", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("/me sin token: %d", w.Code)
	}

	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")
	code, w := doGet(t, r, "/permisos", tok)
	if code != http.StatusOK {
		t.Fatalf("/permisos: %d body=%s", code, w.Body.String())
	}
	var body struct {
		Roles       []string `json:"roles"`
		Operaciones []string `json:"operaciones"`
		UsuarioID   string   `json:"usuarioId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.UsuarioID != "uuid-test" {
		t.Fatalf("usuarioId=%q want uuid-test", body.UsuarioID)
	}
	foundReserve, foundAlta := false, false
	for _, o := range body.Operaciones {
		if o == string(OpReservarReprogramarCancelar) {
			foundReserve = true
		}
		if o == string(OpAltaClinica) {
			foundAlta = true
		}
	}
	if !foundReserve {
		t.Fatalf("permisos recepción sin Reservar…: %v", body.Operaciones)
	}
	if foundAlta {
		t.Fatalf("permisos recepción no debe incluir AltaClinica: %v", body.Operaciones)
	}

	// JWT válido sin fila Usuario → 403
	userStore = &fakeUserStore{bySub: map[string]DomainUser{}}
	code, w = doGet(t, r, "/me", tok)
	if code != http.StatusForbidden {
		t.Fatalf("/me sin usuario BD: %d body=%s", code, w.Body.String())
	}
}
