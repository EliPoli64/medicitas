package main

// Tests unitarios de RF18/RF19: bitácora con store falso. Sin Docker.
//
// La escritura atómica (mismo tx que el hecho) se verifica contra la pila
// real en audit_notas_integration_test.go; aquí se prueba el contrato de los
// handlers: matriz, filtros, I5 y la política "sin rastro no se sirve".

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// --- Store falso ---

type fakeAuditStore struct {
	eventos    []AuditEvento
	actores    []AuditActor
	regErr     error
	entradas   []BitacoraEntrada
	listarErr  error
	gotClinica string
	gotFiltro  BitacoraFiltro
}

func (f *fakeAuditStore) Registrar(_ context.Context, clinicaIdent string, actor AuditActor, evs ...AuditEvento) error {
	if f.regErr != nil {
		return f.regErr
	}
	f.gotClinica = clinicaIdent
	for _, ev := range evs {
		f.eventos = append(f.eventos, ev)
		f.actores = append(f.actores, actor)
	}
	return nil
}

func (f *fakeAuditStore) Listar(_ context.Context, clinicaIdent string, filtro BitacoraFiltro) ([]BitacoraEntrada, error) {
	f.gotClinica = clinicaIdent
	f.gotFiltro = filtro
	if f.listarErr != nil {
		return nil, f.listarErr
	}
	if f.entradas == nil {
		return []BitacoraEntrada{}, nil
	}
	return f.entradas, nil
}

// setupAuditTestEnv arma el router con stores falso de bitácora y pacientes.
func setupAuditTestEnv(t *testing.T, srvURL string, audit AuditStore, pacientes PatientStore, users map[string]DomainUser) *gin.Engine {
	t.Helper()
	r := setupPacientesWriteEnv(t, srvURL, pacientes, users)
	prev := auditStore
	auditStore = audit
	t.Cleanup(func() { auditStore = prev })
	return r
}

// --- RF19: consulta de la bitácora ---

func TestListBitacoraOK(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	spy := &fakeAuditStore{entradas: []BitacoraEntrada{
		{ID: 2, Accion: "escritura", Entidad: "contacto_paciente", PacienteID: "00000000-0000-4000-8000-000000000009"},
	}}
	r := setupAuditTestEnv(t, srv.URL, spy, &fakePatientStore{}, admClinicaUser())

	code, w := doGet(t, r,
		"/bitacora?usuarioId=00000000-0000-4000-8000-000000000002&pacienteId=00000000-0000-4000-8000-000000000009&desde=2026-11-01&hasta=2026-11-30&limit=50&clinicaId=clinica-sur",
		mintAdm(t, tk))
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	// I5: la clínica viene del token; ?clinicaId= se ignora.
	if spy.gotClinica != "clinica-norte" {
		t.Fatalf("I5: clínica = %s", spy.gotClinica)
	}
	f := spy.gotFiltro
	if f.UsuarioID != "00000000-0000-4000-8000-000000000002" ||
		f.PacienteID != "00000000-0000-4000-8000-000000000009" ||
		f.Desde != "2026-11-01" || f.Hasta != "2026-11-30" || f.Limit != 50 {
		t.Fatalf("filtros = %+v", f)
	}
	if !contains(w.Body.String(), `"escritura"`) {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestListBitacoraFiltrosInvalidos400(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	spy := &fakeAuditStore{}
	r := setupAuditTestEnv(t, srv.URL, spy, &fakePatientStore{}, admClinicaUser())

	for nombre, query := range map[string]string{
		"usuarioId no uuid":  "/bitacora?usuarioId=abc",
		"pacienteId no uuid": "/bitacora?pacienteId=123",
		"desde inválido":     "/bitacora?desde=01-11-2026",
		"hasta inválido":     "/bitacora?hasta=ayer",
		"desde > hasta":      "/bitacora?desde=2026-11-30&hasta=2026-11-01",
		"limit excesivo":     "/bitacora?limit=501",
		"limit cero":         "/bitacora?limit=0",
		"limit no numérico":  "/bitacora?limit=abc",
	} {
		t.Run(nombre, func(t *testing.T) {
			code, _ := doGet(t, r, query, mintAdm(t, tk))
			if code != http.StatusBadRequest {
				t.Fatalf("status=%d", code)
			}
		})
	}
}

func TestListBitacoraMatriz403(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	spy := &fakeAuditStore{}
	r := setupAuditTestEnv(t, srv.URL, spy, &fakePatientStore{}, admClinicaUser())

	// Matriz RF17: OpBitacora solo adm-clinica.
	tokRecep := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")
	tokProf := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleProfesional}, testKid, "clinica-norte")
	tokPlat := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleAdmPlataforma}, testKid, "")
	for nombre, tok := range map[string]string{
		"recepcion":      tokRecep,
		"profesional":    tokProf,
		"adm-plataforma": tokPlat,
	} {
		t.Run(nombre, func(t *testing.T) {
			if code, _ := doGet(t, r, "/bitacora", tok); code != http.StatusForbidden {
				t.Fatalf("status=%d, want 403", code)
			}
		})
	}
	if code, _ := doGet(t, r, "/bitacora", ""); code != http.StatusUnauthorized {
		t.Fatalf("sin token = %d, want 401", code)
	}
}

func TestListBitacoraCaida503(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	r := setupAuditTestEnv(t, srv.URL, nil, &fakePatientStore{}, admClinicaUser())
	if code, _ := doGet(t, r, "/bitacora", mintAdm(t, tk)); code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", code)
	}

	spy := &fakeAuditStore{listarErr: errors.New("boom")}
	r = setupAuditTestEnv(t, srv.URL, spy, &fakePatientStore{}, admClinicaUser())
	if code, _ := doGet(t, r, "/bitacora", mintAdm(t, tk)); code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500", code)
	}
}

// --- RF18: captura en los puntos de contacto ---

func TestGetPacienteAuditaLectura(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	store := &fakePatientStore{byID: map[string]Patient{
		"p1": {ID: "p1", Identificacion: "1-1", Nombre: "Ana",
			Contactos: []PatientContact{{ID: "c1", PacienteID: "p1", TipoCodigo: "telefono"}}},
	}}
	spy := &fakeAuditStore{}
	r := setupAuditTestEnv(t, srv.URL, spy, store, admClinicaUser())

	code, w := doGet(t, r, "/pacientes/p1", mintAdm(t, tk))
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	if len(spy.eventos) != 1 {
		t.Fatalf("eventos = %+v", spy.eventos)
	}
	ev := spy.eventos[0]
	if ev.Accion != accionLectura || ev.Entidad != entidadContactoPaciente || ev.PacienteID != "p1" {
		t.Fatalf("evento = %+v", ev)
	}
	if ev.Metadata["contactos"] != 1 {
		t.Fatalf("metadata = %+v", ev.Metadata)
	}
	// El actor viene del contexto autenticado, no del cliente.
	if len(spy.actores) != 1 || spy.actores[0].IdpSub != "test-user" || spy.actores[0].UsuarioID != "u-adm" {
		t.Fatalf("actor = %+v", spy.actores)
	}
	// Nunca se guarda el valor del contacto en la bitácora.
	if contains(w.Body.String(), `"valor"`) && ev.Metadata["valor"] != nil {
		t.Fatalf("la metadata contiene valores sensibles: %+v", ev.Metadata)
	}
}

func TestGetPacienteSinRastroNoSirve(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	store := &fakePatientStore{byID: map[string]Patient{"p1": {ID: "p1"}}}

	// Sin bitácora disponible: 503 y sin datos.
	r := setupAuditTestEnv(t, srv.URL, nil, store, admClinicaUser())
	if code, _ := doGet(t, r, "/pacientes/p1", mintAdm(t, tk)); code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", code)
	}

	// Falla el registro: 500 y sin datos (fail-closed).
	spy := &fakeAuditStore{regErr: errors.New("boom")}
	r = setupAuditTestEnv(t, srv.URL, spy, store, admClinicaUser())
	code, w := doGet(t, r, "/pacientes/p1", mintAdm(t, tk))
	if code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500", code)
	}
	if contains(w.Body.String(), "paciente") {
		t.Fatalf("se sirvió el dato sin rastro: %s", w.Body.String())
	}
}

func TestGetPacienteFueraNoAudita(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	store := &fakePatientStore{byID: map[string]Patient{}}
	spy := &fakeAuditStore{}
	r := setupAuditTestEnv(t, srv.URL, spy, store, admClinicaUser())

	if code, _ := doGet(t, r, "/pacientes/missing", mintAdm(t, tk)); code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", code)
	}
	if len(spy.eventos) != 0 {
		t.Fatalf("se audita un 404: %+v", spy.eventos)
	}
}

func TestEscriturasRecibenElActor(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	store := &fakePatientStore{}
	spy := &fakeAuditStore{}
	r := setupAuditTestEnv(t, srv.URL, spy, store, admClinicaUser())

	code, _ := doJSON(t, r, http.MethodPost, "/pacientes", mintAdm(t, tk),
		`{"identificacion":"9-9","nombre":"Nuevo","fechaNacimiento":"1990-01-01",
		  "contactos":[{"tipoCodigo":"telefono","valor":"8888"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("status=%d", code)
	}
	// El store de escritura recibe el actor auditable del contexto.
	if store.lastActor.IdpSub != "test-user" || store.lastActor.UsuarioID != "u-adm" {
		t.Fatalf("actor = %+v", store.lastActor)
	}
	if store.lastActor.RequestID == "" {
		t.Fatalf("falta requestId: %+v", store.lastActor)
	}
}
