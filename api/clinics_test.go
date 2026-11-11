package main

// Tests unitarios de RF1 (POST /clinicas). Sin Docker: fakes de stores y de
// IdentityAdmin sobre el router real con JWT firmados por la llave de prueba.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type fakeClinicStore struct {
	created []NewClinic
	gotSub  string
	clinic  Clinic
	admin   ClinicAdmin
	err     error
}

func (f *fakeClinicStore) CreateClinicaConAdmin(_ context.Context, c NewClinic, adminIDPSub, adminUsername string) (Clinic, ClinicAdmin, error) {
	f.created = append(f.created, c)
	f.gotSub = adminIDPSub
	if f.err != nil {
		return Clinic{}, ClinicAdmin{}, f.err
	}
	clinic := f.clinic
	if clinic.ID == "" {
		clinic = Clinic{ID: "uuid-clinica", Identificador: c.Identificador, Nombre: c.Nombre, ZonaHoraria: c.ZonaHoraria, Activo: true}
	}
	admin := f.admin
	if admin.UsuarioID == "" {
		admin = ClinicAdmin{UsuarioID: "uuid-usuario", IdpSub: adminIDPSub, Username: adminUsername, RolCodigo: "adm_clinica"}
	}
	return clinic, admin, nil
}

type fakeIdentityAdmin struct {
	created []NewIDPUser
	sub     string
	err     error
	deleted []string
	delErr  error

	// Rol / estado aplicados a cada sub (para compensación en tests).
	rolesSet  map[string]string
	enabledSet map[string]bool
	setErr    error
}

func (f *fakeIdentityAdmin) CreateUser(_ context.Context, u NewIDPUser) (string, error) {
	f.created = append(f.created, u)
	if f.err != nil {
		return "", f.err
	}
	if f.sub == "" {
		return "sub-nuevo", nil
	}
	return f.sub, nil
}

func (f *fakeIdentityAdmin) DeleteUser(_ context.Context, sub string) error {
	f.deleted = append(f.deleted, sub)
	return f.delErr
}

func (f *fakeIdentityAdmin) SetRealmRole(_ context.Context, sub, roleName string) error {
	if f.rolesSet == nil {
		f.rolesSet = map[string]string{}
	}
	f.rolesSet[sub] = roleName
	return f.setErr
}

func (f *fakeIdentityAdmin) SetEnabled(_ context.Context, sub string, enabled bool) error {
	if f.enabledSet == nil {
		f.enabledSet = map[string]bool{}
	}
	f.enabledSet[sub] = enabled
	return f.setErr
}

// setupClinicasTestEnv arma el router con stores falsos y token real.
func setupClinicasTestEnv(t *testing.T, srvURL string, store ClinicStore, idp IdentityAdmin, users map[string]DomainUser) *gin.Engine {
	t.Helper()
	withTestIssuer(t, srvURL)

	prevPing := pingDBFn
	pingDBFn = func(context.Context) error { return nil }
	t.Cleanup(func() { pingDBFn = prevPing })

	prevUsers := userStore
	userStore = &fakeUserStore{bySub: users}
	t.Cleanup(func() { userStore = prevUsers })

	prevClinics := clinicStore
	clinicStore = store
	t.Cleanup(func() { clinicStore = prevClinics })

	prevIDP := identityAdmin
	identityAdmin = idp
	t.Cleanup(func() { identityAdmin = prevIDP })

	gin.SetMode(gin.TestMode)
	return setupRouter()
}

func platAdminUser() map[string]DomainUser {
	return map[string]DomainUser{
		"test-user": {
			UsuarioID: "u-plat", IdpSub: "test-user", Activo: true,
			ClinicaIdentificador: nil, RolCodigo: "adm_plataforma",
		},
	}
}

func doJSON(t *testing.T, r http.Handler, method, path, token, body string) (int, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code, w
}

const validBody = `{
	"nombre": "Clínica Este",
	"identificador": "clinica-este",
	"zonaHoraria": "America/Costa_Rica",
	"administrador": {"username": "adm-este", "password": "secret123"}
}`

func TestCreateClinicaOK(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	store := &fakeClinicStore{}
	idp := &fakeIdentityAdmin{}
	r := setupClinicasTestEnv(t, srv.URL, store, idp, platAdminUser())

	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleAdmPlataforma}, testKid, "")
	code, w := doJSON(t, r, http.MethodPost, "/clinicas", tok, validBody)
	if code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "clinica-este") {
		t.Fatalf("respuesta sin la clínica: %s", w.Body.String())
	}

	// La identidad se crea con el rol de la matriz y el claim de clínica.
	if len(idp.created) != 1 {
		t.Fatalf("identidades creadas=%d", len(idp.created))
	}
	u := idp.created[0]
	if u.RealmRole != "adm-clinica" || u.ClinicaIdentificador != "clinica-este" {
		t.Fatalf("identidad creada = %+v", u)
	}
	// El sub de Keycloak llega a la BD como idpSub.
	if store.gotSub != "sub-nuevo" || len(store.created) != 1 {
		t.Fatalf("store sub=%q created=%d", store.gotSub, len(store.created))
	}
	if store.created[0].ZonaHoraria != "America/Costa_Rica" {
		t.Fatalf("zona=%q", store.created[0].ZonaHoraria)
	}
}

func TestCreateClinicaZonaDefault(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	store := &fakeClinicStore{}
	idp := &fakeIdentityAdmin{}
	r := setupClinicasTestEnv(t, srv.URL, store, idp, platAdminUser())

	body := `{"nombre":"Clínica Oeste","identificador":"clinica-oeste",
	          "administrador":{"username":"adm-oeste","password":"secret123"}}`
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleAdmPlataforma}, testKid, "")
	code, _ := doJSON(t, r, http.MethodPost, "/clinicas", tok, body)
	if code != http.StatusCreated {
		t.Fatalf("status=%d", code)
	}
	if store.created[0].ZonaHoraria != "America/Costa_Rica" {
		t.Fatalf("zona default = %q", store.created[0].ZonaHoraria)
	}
}

func TestCreateClinicaValidacion400(t *testing.T) {
	casos := map[string]string{
		"json malformado":     `{"nombre":`,
		"nombre vacío":        `{"nombre":"","identificador":"clinica-x","administrador":{"username":"adm-x","password":"secret123"}}`,
		"identificador inválido": `{"nombre":"C","identificador":"Clínica Este","administrador":{"username":"adm-x","password":"secret123"}}`,
		"zona inválida":       `{"nombre":"C","identificador":"clinica-x","zonaHoraria":"Marte/Olympus","administrador":{"username":"adm-x","password":"secret123"}}`,
		"username inválido":   `{"nombre":"C","identificador":"clinica-x","administrador":{"username":"a b","password":"secret123"}}`,
		"password corta":      `{"nombre":"C","identificador":"clinica-x","administrador":{"username":"adm-x","password":"corto"}}`,
		"email inválido":      `{"nombre":"C","identificador":"clinica-x","administrador":{"username":"adm-x","password":"secret123","email":"no-es-correo"}}`,
	}
	for nombre, body := range casos {
		t.Run(nombre, func(t *testing.T) {
			tk := newTestKeys(t)
			srv := tk.jwksServer()
			defer srv.Close()

			store := &fakeClinicStore{}
			idp := &fakeIdentityAdmin{}
			r := setupClinicasTestEnv(t, srv.URL, store, idp, platAdminUser())

			tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleAdmPlataforma}, testKid, "")
			code, w := doJSON(t, r, http.MethodPost, "/clinicas", tok, body)
			if code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", code, w.Body.String())
			}
			// Una entrada mal formada no debe provisionar nada.
			if len(idp.created) != 0 || len(store.created) != 0 {
				t.Fatalf("se provisionó con entrada inválida")
			}
		})
	}
}

func TestCreateClinicaSinToken401(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	store := &fakeClinicStore{}
	idp := &fakeIdentityAdmin{}
	r := setupClinicasTestEnv(t, srv.URL, store, idp, platAdminUser())

	code, _ := doJSON(t, r, http.MethodPost, "/clinicas", "", validBody)
	if code != http.StatusUnauthorized {
		t.Fatalf("status=%d", code)
	}
}

func TestCreateClinicaRolIncorrecto403(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	clinica := "clinica-norte"
	store := &fakeClinicStore{}
	idp := &fakeIdentityAdmin{}
	r := setupClinicasTestEnv(t, srv.URL, store, idp, map[string]DomainUser{
		"test-user": {
			UsuarioID: "u-recep", IdpSub: "test-user", Activo: true,
			ClinicaIdentificador: &clinica, RolCodigo: "recepcion",
		},
	})

	// Recepción tiene token válido pero RF1 no le corresponde (matriz).
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")
	code, _ := doJSON(t, r, http.MethodPost, "/clinicas", tok, validBody)
	if code != http.StatusForbidden {
		t.Fatalf("status=%d", code)
	}
	if len(idp.created) != 0 {
		t.Fatalf("se provisionó sin permiso")
	}
}

func TestCreateClinicaUsuarioDuplicado409(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	store := &fakeClinicStore{}
	idp := &fakeIdentityAdmin{err: errIDPUserExists}
	r := setupClinicasTestEnv(t, srv.URL, store, idp, platAdminUser())

	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleAdmPlataforma}, testKid, "")
	code, _ := doJSON(t, r, http.MethodPost, "/clinicas", tok, validBody)
	if code != http.StatusConflict {
		t.Fatalf("status=%d", code)
	}
	// No se toca la BD si la identidad ya existe.
	if len(store.created) != 0 {
		t.Fatalf("BD tocada con identidad duplicada")
	}
}

func TestCreateClinicaIDPCaido502(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	store := &fakeClinicStore{}
	idp := &fakeIdentityAdmin{err: errors.New("keycloak caído")}
	r := setupClinicasTestEnv(t, srv.URL, store, idp, platAdminUser())

	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleAdmPlataforma}, testKid, "")
	code, _ := doJSON(t, r, http.MethodPost, "/clinicas", tok, validBody)
	if code != http.StatusBadGateway {
		t.Fatalf("status=%d", code)
	}
	if len(store.created) != 0 {
		t.Fatalf("BD tocada sin identidad")
	}
}

func TestCreateClinicaConflictoBD409ConCompensacion(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	store := &fakeClinicStore{err: errClinicaExists}
	idp := &fakeIdentityAdmin{}
	r := setupClinicasTestEnv(t, srv.URL, store, idp, platAdminUser())

	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleAdmPlataforma}, testKid, "")
	code, _ := doJSON(t, r, http.MethodPost, "/clinicas", tok, validBody)
	if code != http.StatusConflict {
		t.Fatalf("status=%d", code)
	}
	// La identidad creada se elimina: no quedan usuarios huérfanos.
	if len(idp.deleted) != 1 || idp.deleted[0] != "sub-nuevo" {
		t.Fatalf("compensación = %v", idp.deleted)
	}
}

func TestCreateClinicaErrorBD500ConCompensacion(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	store := &fakeClinicStore{err: errors.New("boom")}
	idp := &fakeIdentityAdmin{}
	r := setupClinicasTestEnv(t, srv.URL, store, idp, platAdminUser())

	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleAdmPlataforma}, testKid, "")
	code, _ := doJSON(t, r, http.MethodPost, "/clinicas", tok, validBody)
	if code != http.StatusInternalServerError {
		t.Fatalf("status=%d", code)
	}
	if len(idp.deleted) != 1 {
		t.Fatalf("compensación = %v", idp.deleted)
	}
}

func TestValidateCreateClinicaRequest(t *testing.T) {
	req := createClinicaRequest{Nombre: "C", Identificador: "clinica-este", ZonaHoraria: "America/Costa_Rica"}
	req.Administrador.Username = "adm-este"
	req.Administrador.Password = "secret123"
	if errs := req.validate(); len(errs) != 0 {
		t.Fatalf("válido marcado como inválido: %v", errs)
	}

	// Formato del identificador = CHECK de la BD.
	for _, id := range []string{"Clínica", "clinica_este", "-este", "este-", "clinica--este", ""} {
		req.Identificador = id
		if errs := req.validate(); len(errs) == 0 {
			t.Fatalf("identificador %q aceptado", id)
		}
	}
}
