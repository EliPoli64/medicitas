package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type fakeAppointmentStore struct {
	lastClinica string
	lastInicio  time.Time
	cita        Appointment
	byID        map[string]Appointment
	byUsuario   map[string]string // usuarioID -> profesionalID
	err         error
	calls       int
}

func (f *fakeAppointmentStore) ensure() {
	if f.byID == nil {
		f.byID = map[string]Appointment{}
	}
	if f.byUsuario == nil {
		f.byUsuario = map[string]string{}
	}
}

func (f *fakeAppointmentStore) Reservar(_ context.Context, clinicaIdent, pacienteID, profesionalID, servicioID string, inicio time.Time) (Appointment, error) {
	f.ensure()
	f.calls++
	f.lastClinica = clinicaIdent
	f.lastInicio = inicio
	if f.err != nil {
		return Appointment{}, f.err
	}
	a := f.cita
	if a.ID == "" {
		a = Appointment{
			ID: "cita-1", PacienteID: pacienteID, ProfesionalID: profesionalID,
			ServicioID: servicioID, Inicio: inicio.UTC().Format(time.RFC3339),
			Fin: inicio.Add(30 * time.Minute).UTC().Format(time.RFC3339),
			DuracionMinutos: 30, Estado: "reservada",
		}
	}
	f.byID[a.ID] = a
	return a, nil
}

func (f *fakeAppointmentStore) GetCita(_ context.Context, clinicaIdent, citaID string) (Appointment, error) {
	f.ensure()
	f.lastClinica = clinicaIdent
	a, ok := f.byID[citaID]
	if !ok {
		return Appointment{}, errCitaFueraDeClinica
	}
	return a, nil
}

func (f *fakeAppointmentStore) ProfesionalIDByUsuarioID(_ context.Context, clinicaIdent, usuarioID string) (string, error) {
	f.ensure()
	f.lastClinica = clinicaIdent
	id, ok := f.byUsuario[usuarioID]
	if !ok {
		return "", errCitaSinProfesional
	}
	return id, nil
}

func (f *fakeAppointmentStore) Reprogramar(_ context.Context, clinicaIdent, citaID string, nuevoInicio time.Time, nuevoProfesionalID, nuevoServicioID *string) (ReprogramacionResult, error) {
	f.ensure()
	f.lastClinica = clinicaIdent
	if f.err != nil {
		return ReprogramacionResult{}, f.err
	}
	origen, err := f.GetCita(context.Background(), clinicaIdent, citaID)
	if err != nil {
		return ReprogramacionResult{}, err
	}
	if origen.Estado != "reservada" {
		return ReprogramacionResult{}, errCitaNegocio
	}
	origen.Estado = "reprogramada"
	f.byID[citaID] = origen
	nueva := Appointment{
		ID: "cita-nueva", PacienteID: origen.PacienteID,
		ProfesionalID: origen.ProfesionalID, ServicioID: origen.ServicioID,
		Inicio: nuevoInicio.UTC().Format(time.RFC3339),
		Fin:    nuevoInicio.Add(30 * time.Minute).UTC().Format(time.RFC3339),
		DuracionMinutos: 30, Estado: "reservada", CitaOrigenID: citaID,
	}
	if nuevoProfesionalID != nil && *nuevoProfesionalID != "" {
		nueva.ProfesionalID = *nuevoProfesionalID
	}
	if nuevoServicioID != nil && *nuevoServicioID != "" {
		nueva.ServicioID = *nuevoServicioID
	}
	f.byID[nueva.ID] = nueva
	return ReprogramacionResult{
		CitaAnteriorID: citaID, ProfesionalAnteriorID: origen.ProfesionalID, CitaNueva: nueva,
	}, nil
}

func (f *fakeAppointmentStore) Cancelar(_ context.Context, clinicaIdent, citaID, _, motivo string) (Appointment, error) {
	f.ensure()
	f.lastClinica = clinicaIdent
	if f.err != nil {
		return Appointment{}, f.err
	}
	a, err := f.GetCita(context.Background(), clinicaIdent, citaID)
	if err != nil {
		return Appointment{}, err
	}
	if a.Estado != "reservada" {
		return Appointment{}, errCitaNegocio
	}
	if motivo == "" {
		return Appointment{}, errCitaDatosInvalidos
	}
	a.Estado = "cancelada"
	f.byID[citaID] = a
	return a, nil
}

func (f *fakeAppointmentStore) RegistrarDesenlace(_ context.Context, clinicaIdent, citaID, estado string) (Appointment, error) {
	f.ensure()
	f.lastClinica = clinicaIdent
	if f.err != nil {
		return Appointment{}, f.err
	}
	a, err := f.GetCita(context.Background(), clinicaIdent, citaID)
	if err != nil {
		return Appointment{}, err
	}
	if a.Estado != "reservada" {
		return Appointment{}, errCitaNegocio
	}
	a.Estado = estado
	f.byID[citaID] = a
	return a, nil
}

func setupCitasEnv(t *testing.T, srvURL string, store AppointmentStore, users map[string]DomainUser) *gin.Engine {
	t.Helper()
	withTestIssuer(t, srvURL)
	prevPing := pingDBFn
	pingDBFn = func(context.Context) error { return nil }
	t.Cleanup(func() { pingDBFn = prevPing })

	prevUsers := userStore
	userStore = &fakeUserStore{bySub: users}
	t.Cleanup(func() { userStore = prevUsers })

	prev := appointmentStore
	appointmentStore = store
	t.Cleanup(func() { appointmentStore = prev })

	gin.SetMode(gin.TestMode)
	return setupRouter()
}

func recepCitaUser() map[string]DomainUser {
	clinica := "clinica-norte"
	return map[string]DomainUser{
		"test-user": {
			UsuarioID: "u-recep", IdpSub: "test-user", Activo: true,
			ClinicaIdentificador: &clinica, RolCodigo: "recepcion",
		},
	}
}

func profCitaUser() map[string]DomainUser {
	clinica := "clinica-norte"
	return map[string]DomainUser{
		"test-user": {
			UsuarioID: "u-prof", IdpSub: "test-user", Activo: true,
			ClinicaIdentificador: &clinica, RolCodigo: "profesional",
		},
	}
}

func TestReservarCitaOK(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	store := &fakeAppointmentStore{}
	r := setupCitasEnv(t, srv.URL, store, recepCitaUser())
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")

	body := `{
		"pacienteId":"pac-1",
		"profesionalId":"prof-1",
		"servicioId":"svc-1",
		"inicio":"2026-11-16T14:00:00Z"
	}`
	code, w := doJSON(t, r, http.MethodPost, "/citas", tok, body)
	if code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	var out struct {
		ClinicaIdentificador string      `json:"clinicaIdentificador"`
		Cita                 Appointment `json:"cita"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ClinicaIdentificador != "clinica-norte" || out.Cita.Estado != "reservada" {
		t.Fatalf("out=%+v", out)
	}
	if store.lastClinica != "clinica-norte" {
		t.Fatalf("tenant=%s", store.lastClinica)
	}
}

func TestReservarCitaI5IgnoresQuery(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakeAppointmentStore{}
	r := setupCitasEnv(t, srv.URL, store, recepCitaUser())
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")

	body := `{"pacienteId":"p","profesionalId":"pr","servicioId":"s","inicio":"2026-11-16T14:00:00Z"}`
	code, _ := doJSON(t, r, http.MethodPost, "/citas?clinicaId=clinica-sur", tok, body)
	if code != http.StatusCreated {
		t.Fatalf("status=%d", code)
	}
	if store.lastClinica != "clinica-norte" {
		t.Fatalf("I5 leak: %s", store.lastClinica)
	}
}

func TestReservarCitaProfesional403(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	r := setupCitasEnv(t, srv.URL, &fakeAppointmentStore{}, profCitaUser())
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleProfesional}, testKid, "clinica-norte")

	body := `{"pacienteId":"p","profesionalId":"pr","servicioId":"s","inicio":"2026-11-16T14:00:00Z"}`
	code, _ := doJSON(t, r, http.MethodPost, "/citas", tok, body)
	if code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", code)
	}
}

func TestReservarCitaConflicto409(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakeAppointmentStore{err: errCitaConflicto}
	r := setupCitasEnv(t, srv.URL, store, recepCitaUser())
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")

	body := `{"pacienteId":"p","profesionalId":"pr","servicioId":"s","inicio":"2026-11-16T14:00:00Z"}`
	code, _ := doJSON(t, r, http.MethodPost, "/citas", tok, body)
	if code != http.StatusConflict {
		t.Fatalf("status=%d want 409", code)
	}
}

func TestReservarCitaNegocio400(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakeAppointmentStore{err: errCitaNegocio}
	r := setupCitasEnv(t, srv.URL, store, recepCitaUser())
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")

	body := `{"pacienteId":"p","profesionalId":"pr","servicioId":"s","inicio":"2026-11-16T14:00:00Z"}`
	code, _ := doJSON(t, r, http.MethodPost, "/citas", tok, body)
	if code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", code)
	}
}

func TestReservarCitaInicioInvalido(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	r := setupCitasEnv(t, srv.URL, &fakeAppointmentStore{}, recepCitaUser())
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")

	body := `{"pacienteId":"p","profesionalId":"pr","servicioId":"s","inicio":"no-es-fecha"}`
	code, _ := doJSON(t, r, http.MethodPost, "/citas", tok, body)
	if code != http.StatusBadRequest {
		t.Fatalf("status=%d", code)
	}
}

func TestReprogramarCitaOK(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakeAppointmentStore{byID: map[string]Appointment{
		"c1": {ID: "c1", ProfesionalID: "p1", Estado: "reservada", PacienteID: "pac", ServicioID: "s1"},
	}}
	r := setupCitasEnv(t, srv.URL, store, recepCitaUser())
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")

	code, w := doJSON(t, r, http.MethodPost, "/citas/c1/reprogramar", tok,
		`{"inicio":"2026-11-16T15:00:00Z"}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	var out struct {
		CitaAnteriorID string      `json:"citaAnteriorId"`
		Cita           Appointment `json:"cita"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.CitaAnteriorID != "c1" || out.Cita.Estado != "reservada" || out.Cita.CitaOrigenID != "c1" {
		t.Fatalf("out=%+v", out)
	}
}

func TestReprogramarCitaProfesional403(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	r := setupCitasEnv(t, srv.URL, &fakeAppointmentStore{}, profCitaUser())
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleProfesional}, testKid, "clinica-norte")
	code, _ := doJSON(t, r, http.MethodPost, "/citas/c1/reprogramar", tok, `{"inicio":"2026-11-16T15:00:00Z"}`)
	if code != http.StatusForbidden {
		t.Fatalf("status=%d", code)
	}
}

func TestCancelarCitaOK(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakeAppointmentStore{byID: map[string]Appointment{
		"c1": {ID: "c1", ProfesionalID: "p1", Estado: "reservada"},
	}}
	r := setupCitasEnv(t, srv.URL, store, recepCitaUser())
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")
	code, w := doJSON(t, r, http.MethodPost, "/citas/c1/cancelar", tok, `{"motivo":"paciente no puede"}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	var out struct {
		Cita Appointment `json:"cita"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Cita.Estado != "cancelada" {
		t.Fatalf("estado=%s", out.Cita.Estado)
	}
}

func TestCancelarCitaProfesional403(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	r := setupCitasEnv(t, srv.URL, &fakeAppointmentStore{}, profCitaUser())
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleProfesional}, testKid, "clinica-norte")
	code, _ := doJSON(t, r, http.MethodPost, "/citas/c1/cancelar", tok, `{"motivo":"x"}`)
	if code != http.StatusForbidden {
		t.Fatalf("status=%d", code)
	}
}

func TestDesenlacePropioOK(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakeAppointmentStore{
		byID:      map[string]Appointment{"c1": {ID: "c1", ProfesionalID: "p-mio", Estado: "reservada"}},
		byUsuario: map[string]string{"u-prof": "p-mio"},
	}
	r := setupCitasEnv(t, srv.URL, store, profCitaUser())
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleProfesional}, testKid, "clinica-norte")
	code, w := doJSON(t, r, http.MethodPost, "/citas/c1/desenlace", tok, `{"estado":"atendida"}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	var out struct {
		Cita Appointment `json:"cita"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Cita.Estado != "atendida" {
		t.Fatalf("estado=%s", out.Cita.Estado)
	}
}

func TestDesenlaceAjeno403(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakeAppointmentStore{
		byID:      map[string]Appointment{"c1": {ID: "c1", ProfesionalID: "p-otro", Estado: "reservada"}},
		byUsuario: map[string]string{"u-prof": "p-mio"},
	}
	r := setupCitasEnv(t, srv.URL, store, profCitaUser())
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleProfesional}, testKid, "clinica-norte")
	code, _ := doJSON(t, r, http.MethodPost, "/citas/c1/desenlace", tok, `{"estado":"ausente"}`)
	if code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", code)
	}
}

func TestDesenlaceTransicionIlegal400(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakeAppointmentStore{byID: map[string]Appointment{
		"c1": {ID: "c1", ProfesionalID: "p1", Estado: "cancelada"},
	}}
	r := setupCitasEnv(t, srv.URL, store, recepCitaUser())
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")
	code, _ := doJSON(t, r, http.MethodPost, "/citas/c1/desenlace", tok, `{"estado":"atendida"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", code)
	}
}
