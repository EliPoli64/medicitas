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
	byClinica   map[string][]Patient
	byID        map[string]Patient // id -> patient (clinic norte assumed in tests)
	contactos   map[string]PatientContact
	citas       map[string][]PatientAppointment
	lastClinica string
	lastActor   AuditActor
	createErr   error
}

func (f *fakePatientStore) ensure() {
	if f.byClinica == nil {
		f.byClinica = map[string][]Patient{}
	}
	if f.byID == nil {
		f.byID = map[string]Patient{}
	}
	if f.contactos == nil {
		f.contactos = map[string]PatientContact{}
	}
	if f.citas == nil {
		f.citas = map[string][]PatientAppointment{}
	}
}

func (f *fakePatientStore) ListActiveByClinicaIdentificador(_ context.Context, clinicaIdentificador string) ([]Patient, error) {
	f.ensure()
	f.lastClinica = clinicaIdentificador
	out := f.byClinica[clinicaIdentificador]
	if out == nil {
		return []Patient{}, nil
	}
	return out, nil
}

func (f *fakePatientStore) GetPaciente(_ context.Context, clinicaIdent, id string) (Patient, error) {
	f.ensure()
	f.lastClinica = clinicaIdent
	p, ok := f.byID[id]
	if !ok || clinicaIdent != "clinica-norte" {
		return Patient{}, errPacienteFueraDeClinica
	}
	return p, nil
}

func (f *fakePatientStore) CreatePaciente(_ context.Context, clinicaIdent, identificacion, nombre, fechaNac string, contactos []ContactInput, actor AuditActor) (Patient, error) {
	f.ensure()
	f.lastClinica = clinicaIdent
	f.lastActor = actor
	if f.createErr != nil {
		return Patient{}, f.createErr
	}
	for _, p := range f.byClinica[clinicaIdent] {
		if p.Identificacion == identificacion {
			return Patient{}, errPacienteDuplicado
		}
	}
	p := Patient{
		ID: "p-new", Identificacion: identificacion, Nombre: nombre,
		FechaNacimiento: fechaNac, Activo: true, Contactos: []PatientContact{},
	}
	for i, in := range contactos {
		ct := PatientContact{
			ID: "c-new-" + string(rune('a'+i)), PacienteID: p.ID,
			TipoCodigo: in.TipoCodigo, TipoNombre: in.TipoCodigo,
			Valor: in.Valor, EsPrincipal: in.EsPrincipal, Activo: true,
		}
		p.Contactos = append(p.Contactos, ct)
		f.contactos[ct.ID] = ct
	}
	f.byClinica[clinicaIdent] = append(f.byClinica[clinicaIdent], p)
	f.byID[p.ID] = p
	return p, nil
}

func (f *fakePatientStore) UpdatePaciente(_ context.Context, clinicaIdent, id string, patch PatientPatch) (Patient, error) {
	f.ensure()
	f.lastClinica = clinicaIdent
	p, err := f.GetPaciente(context.Background(), clinicaIdent, id)
	if err != nil {
		return Patient{}, err
	}
	if patch.Nombre != nil {
		p.Nombre = *patch.Nombre
	}
	if patch.Activo != nil {
		p.Activo = *patch.Activo
	}
	if patch.Identificacion != nil {
		p.Identificacion = *patch.Identificacion
	}
	if patch.FechaNacimiento != nil {
		p.FechaNacimiento = *patch.FechaNacimiento
	}
	f.byID[id] = p
	return p, nil
}

func (f *fakePatientStore) CreateContacto(_ context.Context, clinicaIdent, pacienteID string, in ContactInput, actor AuditActor) (PatientContact, error) {
	f.ensure()
	f.lastActor = actor
	f.ensure()
	f.lastClinica = clinicaIdent
	if _, err := f.GetPaciente(context.Background(), clinicaIdent, pacienteID); err != nil {
		return PatientContact{}, err
	}
	ct := PatientContact{
		ID: "ct-new", PacienteID: pacienteID, TipoCodigo: in.TipoCodigo,
		TipoNombre: in.TipoCodigo, Valor: in.Valor, EsPrincipal: in.EsPrincipal, Activo: true,
	}
	f.contactos[ct.ID] = ct
	return ct, nil
}

func (f *fakePatientStore) UpdateContacto(_ context.Context, clinicaIdent, contactoID string, patch ContactPatch, actor AuditActor) (PatientContact, error) {
	f.ensure()
	f.lastActor = actor
	f.ensure()
	f.lastClinica = clinicaIdent
	ct, ok := f.contactos[contactoID]
	if !ok || clinicaIdent != "clinica-norte" {
		return PatientContact{}, errContactoFueraDeClinica
	}
	if patch.Valor != nil {
		ct.Valor = *patch.Valor
	}
	if patch.Activo != nil {
		ct.Activo = *patch.Activo
	}
	if patch.EsPrincipal != nil {
		ct.EsPrincipal = *patch.EsPrincipal
	}
	f.contactos[contactoID] = ct
	return ct, nil
}

func (f *fakePatientStore) ListCitasByPaciente(_ context.Context, clinicaIdent, pacienteID string) ([]PatientAppointment, error) {
	f.ensure()
	f.lastClinica = clinicaIdent
	if _, err := f.GetPaciente(context.Background(), clinicaIdent, pacienteID); err != nil {
		return nil, err
	}
	out := f.citas[pacienteID]
	if out == nil {
		return []PatientAppointment{}, nil
	}
	return out, nil
}

func setupPacientesWriteEnv(t *testing.T, srvURL string, store PatientStore, users map[string]DomainUser) *gin.Engine {
	t.Helper()
	withTestIssuer(t, srvURL)
	prevPing := pingDBFn
	pingDBFn = func(context.Context) error { return nil }
	t.Cleanup(func() { pingDBFn = prevPing })
	prevUsers := userStore
	userStore = &fakeUserStore{bySub: users}
	t.Cleanup(func() { userStore = prevUsers })
	prev := patientStore
	patientStore = store
	t.Cleanup(func() { patientStore = prev })
	// La lectura de contactos exige bitácora (RF18): spy por defecto si el
	// test no inyecta el suyo antes.
	prevAudit := auditStore
	if auditStore == nil {
		auditStore = &fakeAuditStore{}
	}
	t.Cleanup(func() { auditStore = prevAudit })
	gin.SetMode(gin.TestMode)
	return setupRouter()
}

func recepUser() map[string]DomainUser {
	clinica := "clinica-norte"
	return map[string]DomainUser{
		"test-user": {
			UsuarioID: "u-recep", IdpSub: "test-user", Activo: true,
			ClinicaIdentificador: &clinica, RolCodigo: "recepcion",
		},
	}
}

func mintRecep(t *testing.T, tk *testKeys) string {
	t.Helper()
	return tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")
}

func mintProf(t *testing.T, tk *testKeys) string {
	t.Helper()
	return tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleProfesional}, testKid, "clinica-norte")
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
	r := setupPacientesWriteEnv(t, srv.URL, store, map[string]DomainUser{
		"test-user": {
			UsuarioID: "u1", IdpSub: "test-user", Activo: true,
			ClinicaIdentificador: &clinica, RolCodigo: "recepcion",
		},
	})

	tok := mintRecep(t, tk)
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
	r := setupPacientesWriteEnv(t, srv.URL, store, map[string]DomainUser{
		"test-user": {
			UsuarioID: "u1", IdpSub: "test-user", Activo: true,
			ClinicaIdentificador: &clinica, RolCodigo: "recepcion",
		},
	})

	code, w := doGet(t, r, "/pacientes?clinicaId=clinica-sur", mintRecep(t, tk))
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	if store.lastClinica != "clinica-norte" {
		t.Fatalf("I5 fail: store got %q", store.lastClinica)
	}
	var body struct {
		Pacientes []Patient `json:"pacientes"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if len(body.Pacientes) != 1 || body.Pacientes[0].ID != "p-norte" {
		t.Fatalf("leak: %+v", body.Pacientes)
	}
}

func TestListPacientesUnauthorized(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	r := setupPacientesWriteEnv(t, srv.URL, &fakePatientStore{}, recepUser())
	code, _ := doGet(t, r, "/pacientes", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", code)
	}
}

func TestListPacientesForbiddenPlataforma(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	r := setupPacientesWriteEnv(t, srv.URL, &fakePatientStore{}, map[string]DomainUser{
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
	r := setupPacientesWriteEnv(t, srv.URL, &fakePatientStore{}, map[string]DomainUser{})
	code, _ := doGet(t, r, "/pacientes", mintRecep(t, tk))
	if code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", code)
	}
}

func TestListPacientesForbiddenSinClinica(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	r := setupPacientesWriteEnv(t, srv.URL, &fakePatientStore{}, map[string]DomainUser{
		"test-user": {UsuarioID: "u1", IdpSub: "test-user", Activo: true, ClinicaIdentificador: nil, RolCodigo: "recepcion"},
	})
	code, w := doGet(t, r, "/pacientes", mintRecep(t, tk))
	if code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 body=%s", code, w.Body.String())
	}
}

func TestCreatePacienteOK(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakePatientStore{}
	r := setupPacientesWriteEnv(t, srv.URL, store, recepUser())
	body := `{"identificacion":"9-9999-9999","nombre":"Nueva","fechaNacimiento":"1995-03-03","contactos":[{"tipoCodigo":"telefono","valor":"+50611112222","esPrincipal":true}]}`
	code, w := doJSON(t, r, http.MethodPost, "/pacientes", mintRecep(t, tk), body)
	if code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	if store.lastClinica != "clinica-norte" {
		t.Fatalf("clinica=%s", store.lastClinica)
	}
}

func TestCreatePacienteDuplicado409(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakePatientStore{createErr: errPacienteDuplicado}
	r := setupPacientesWriteEnv(t, srv.URL, store, recepUser())
	body := `{"identificacion":"1","nombre":"X","fechaNacimiento":"1990-01-01"}`
	code, _ := doJSON(t, r, http.MethodPost, "/pacientes", mintRecep(t, tk), body)
	if code != http.StatusConflict {
		t.Fatalf("status=%d want 409", code)
	}
}

func TestCreatePacienteProfesional403(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	clinica := "clinica-norte"
	r := setupPacientesWriteEnv(t, srv.URL, &fakePatientStore{}, map[string]DomainUser{
		"test-user": {UsuarioID: "u-p", IdpSub: "test-user", Activo: true, ClinicaIdentificador: &clinica, RolCodigo: "profesional"},
	})
	body := `{"identificacion":"1","nombre":"X","fechaNacimiento":"1990-01-01"}`
	code, _ := doJSON(t, r, http.MethodPost, "/pacientes", mintProf(t, tk), body)
	if code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", code)
	}
}

func TestCreatePacienteI5IgnoresQuery(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakePatientStore{}
	r := setupPacientesWriteEnv(t, srv.URL, store, recepUser())
	body := `{"identificacion":"8-8888-8888","nombre":"Y","fechaNacimiento":"1991-01-01"}`
	code, _ := doJSON(t, r, http.MethodPost, "/pacientes?clinicaId=clinica-sur", mintRecep(t, tk), body)
	if code != http.StatusCreated {
		t.Fatalf("status=%d", code)
	}
	if store.lastClinica != "clinica-norte" {
		t.Fatalf("I5 fail clinica=%s", store.lastClinica)
	}
}

func TestHistorialPacienteOK(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakePatientStore{
		byID: map[string]Patient{"p1": {ID: "p1", Identificacion: "1", Nombre: "A", FechaNacimiento: "1990-01-01", Activo: true}},
		citas: map[string][]PatientAppointment{
			"p1": {{ID: "c1", Inicio: "2026-01-01T15:00:00Z", Fin: "2026-01-01T15:30:00Z", Estado: "reservada", ProfesionalID: "pr1", ServicioID: "s1", DuracionMinutos: 30}},
		},
	}
	r := setupPacientesWriteEnv(t, srv.URL, store, recepUser())
	code, w := doGet(t, r, "/pacientes/p1/citas", mintRecep(t, tk))
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	var body struct {
		Citas []PatientAppointment `json:"citas"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Citas) != 1 || body.Citas[0].Estado != "reservada" {
		t.Fatalf("unexpected: %+v", body.Citas)
	}
}

func TestHistorialPaciente404(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	r := setupPacientesWriteEnv(t, srv.URL, &fakePatientStore{}, recepUser())
	code, _ := doGet(t, r, "/pacientes/missing/citas", mintRecep(t, tk))
	if code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", code)
	}
}

func TestGetPacienteOK(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakePatientStore{
		byID: map[string]Patient{"p1": {ID: "p1", Identificacion: "1", Nombre: "Ana", FechaNacimiento: "1990-01-01", Activo: true}},
	}
	r := setupPacientesWriteEnv(t, srv.URL, store, recepUser())
	code, w := doGet(t, r, "/pacientes/p1", mintRecep(t, tk))
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	_ = w
}
