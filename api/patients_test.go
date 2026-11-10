package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type fakePatientStore struct {
	byClinica map[string][]Patient
	lastQuery string
	err       error
}

func (f *fakePatientStore) ListActiveByClinicaIdentificador(_ context.Context, clinicaIdentificador string) ([]Patient, error) {
	f.lastQuery = clinicaIdentificador
	if f.err != nil {
		return nil, f.err
	}
	out := f.byClinica[clinicaIdentificador]
	if out == nil {
		return []Patient{}, nil
	}
	return out, nil
}

func setupPacientesTestEnv(t *testing.T, srvURL string, store PatientStore, users map[string]DomainUser) *gin.Engine {
	t.Helper()
	withTestIssuer(t, srvURL)

	prevPing := pingDBFn
	pingDBFn = func(context.Context) error { return nil }
	t.Cleanup(func() { pingDBFn = prevPing })

	prevUsers := userStore
	userStore = &fakeUserStore{bySub: users}
	t.Cleanup(func() { userStore = prevUsers })

	prevPatients := patientStore
	patientStore = store
	t.Cleanup(func() { patientStore = prevPatients })

	gin.SetMode(gin.TestMode)
	return setupRouter()
}

func TestListPacientesOK(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	clinica := "clinica-norte"
	store := &fakePatientStore{byClinica: map[string][]Patient{
		"clinica-norte": {
			{ID: "p1", Identificacion: "1", Nombre: "Ana", FechaNacimiento: "1990-01-01", Activo: true},
		},
		"clinica-sur": {
			{ID: "p2", Identificacion: "2", Nombre: "Sur", FechaNacimiento: "1991-01-01", Activo: true},
		},
	}}
	r := setupPacientesTestEnv(t, srv.URL, store, map[string]DomainUser{
		"test-user": {
			UsuarioID: "u1", IdpSub: "test-user", Activo: true,
			ClinicaIdentificador: &clinica, RolCodigo: "recepcion",
		},
	})

	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")
	code, w := doGet(t, r, "/pacientes", tok)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	var body struct {
		ClinicaIdentificador string    `json:"clinicaIdentificador"`
		Pacientes            []Patient `json:"pacientes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ClinicaIdentificador != "clinica-norte" || len(body.Pacientes) != 1 || body.Pacientes[0].ID != "p1" {
		t.Fatalf("unexpected body: %+v", body)
	}
	if store.lastQuery != "clinica-norte" {
		t.Fatalf("store queried %q", store.lastQuery)
	}
}

func TestListPacientesI5IgnoresQueryClinicaId(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	clinica := "clinica-norte"
	store := &fakePatientStore{byClinica: map[string][]Patient{
		"clinica-norte": {{ID: "p-norte", Identificacion: "n", Nombre: "Norte", FechaNacimiento: "1990-01-01", Activo: true}},
		"clinica-sur":   {{ID: "p-sur", Identificacion: "s", Nombre: "Sur", FechaNacimiento: "1991-01-01", Activo: true}},
	}}
	r := setupPacientesTestEnv(t, srv.URL, store, map[string]DomainUser{
		"test-user": {
			UsuarioID: "u1", IdpSub: "test-user", Activo: true,
			ClinicaIdentificador: &clinica, RolCodigo: "recepcion",
		},
	})

	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")
	code, w := doGet(t, r, "/pacientes?clinicaId=clinica-sur", tok)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	if store.lastQuery != "clinica-norte" {
		t.Fatalf("I5 fail: store got %q want clinica-norte", store.lastQuery)
	}
	var body struct {
		Pacientes []Patient `json:"pacientes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Pacientes) != 1 || body.Pacientes[0].ID != "p-norte" {
		t.Fatalf("leak or wrong set: %+v", body.Pacientes)
	}
}

func TestListPacientesUnauthorized(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	clinica := "clinica-norte"
	r := setupPacientesTestEnv(t, srv.URL, &fakePatientStore{}, map[string]DomainUser{
		"test-user": {UsuarioID: "u1", IdpSub: "test-user", Activo: true, ClinicaIdentificador: &clinica, RolCodigo: "recepcion"},
	})
	code, _ := doGet(t, r, "/pacientes", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", code)
	}
}

func TestListPacientesForbiddenPlataforma(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	r := setupPacientesTestEnv(t, srv.URL, &fakePatientStore{}, map[string]DomainUser{
		"test-user": {UsuarioID: "u1", IdpSub: "test-user", Activo: true, RolCodigo: "adm_plataforma"},
	})
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleAdmPlataforma}, testKid, "")
	code, _ := doGet(t, r, "/pacientes", tok)
	if code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", code)
	}
}

func TestListPacientesForbiddenNoDomainUser(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	r := setupPacientesTestEnv(t, srv.URL, &fakePatientStore{}, map[string]DomainUser{})
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")
	code, _ := doGet(t, r, "/pacientes", tok)
	if code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", code)
	}
}

func TestListPacientesForbiddenSinClinica(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	r := setupPacientesTestEnv(t, srv.URL, &fakePatientStore{}, map[string]DomainUser{
		"test-user": {UsuarioID: "u1", IdpSub: "test-user", Activo: true, ClinicaIdentificador: nil, RolCodigo: "recepcion"},
	})
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")
	code, w := doGet(t, r, "/pacientes", tok)
	if code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 body=%s", code, w.Body.String())
	}
}
