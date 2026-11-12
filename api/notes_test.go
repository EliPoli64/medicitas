package main

// Tests unitarios de RF15: nota clínica con stores falsos. Sin Docker.
//
// Cubren la matriz (solo profesional), la propiedad ("solo el profesional que
// atendió"), la máquina de estados (solo cita atendida), la duplicación y la
// auditoría de lectura/escritura (RF18).

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// --- Store falso ---

type fakeNotaStore struct {
	cita         CitaNotaInfo
	citaErr      error
	profID       string
	profErr      error
	nota         NotaClinica
	notaErr      error
	createErr    error
	gotClinica   string
	gotCitaID    string
	gotProfID    string
	gotContenido string
	gotActor     AuditActor
}

func (f *fakeNotaStore) CitaParaNota(_ context.Context, clinicaIdent, citaID string) (CitaNotaInfo, error) {
	f.gotClinica = clinicaIdent
	f.gotCitaID = citaID
	if f.citaErr != nil {
		return CitaNotaInfo{}, f.citaErr
	}
	return f.cita, nil
}

func (f *fakeNotaStore) ProfesionalDeUsuario(_ context.Context, _, _ string) (string, error) {
	if f.profErr != nil {
		return "", f.profErr
	}
	return f.profID, nil
}

func (f *fakeNotaStore) CreateNota(_ context.Context, clinicaIdent, citaID, profesionalID, contenido string, actor AuditActor) (NotaClinica, error) {
	f.gotClinica = clinicaIdent
	f.gotCitaID = citaID
	f.gotProfID = profesionalID
	f.gotContenido = contenido
	f.gotActor = actor
	if f.createErr != nil {
		return NotaClinica{}, f.createErr
	}
	return f.nota, nil
}

func (f *fakeNotaStore) GetNota(_ context.Context, clinicaIdent, citaID string) (NotaClinica, error) {
	f.gotClinica = clinicaIdent
	f.gotCitaID = citaID
	if f.notaErr != nil {
		return NotaClinica{}, f.notaErr
	}
	return f.nota, nil
}

// setupNotasTestEnv arma el router con stores falsos de notas y bitácora.
func setupNotasTestEnv(t *testing.T, srvURL string, notas NotaStore, audit AuditStore, users map[string]DomainUser) *gin.Engine {
	t.Helper()
	withTestIssuer(t, srvURL)
	prevPing := pingDBFn
	pingDBFn = func(context.Context) error { return nil }
	t.Cleanup(func() { pingDBFn = prevPing })
	prevUsers := userStore
	userStore = &fakeUserStore{bySub: users}
	t.Cleanup(func() { userStore = prevUsers })
	prevNotas := notaStore
	notaStore = notas
	t.Cleanup(func() { notaStore = prevNotas })
	prevAudit := auditStore
	auditStore = audit
	t.Cleanup(func() { auditStore = prevAudit })
	gin.SetMode(gin.TestMode)
	return setupRouter()
}

func profUser() map[string]DomainUser {
	clinica := "clinica-norte"
	return map[string]DomainUser{
		"test-user": {
			UsuarioID: "u-prof", IdpSub: "test-user", Activo: true,
			ClinicaIdentificador: &clinica, RolCodigo: "profesional",
		},
	}
}

var citaAtendidaPropia = CitaNotaInfo{
	ID: "c1", ProfesionalID: "prof-1", PacienteID: "p1", Estado: "atendida",
}

// --- RF15: creación ---

func TestCreateNotaOK(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	notas := &fakeNotaStore{
		cita:   citaAtendidaPropia,
		profID: "prof-1",
		nota:   NotaClinica{ID: "n1", CitaID: "c1", ProfesionalID: "prof-1", Contenido: "evolución", CreadoEn: time.Now()},
	}
	spy := &fakeAuditStore{}
	r := setupNotasTestEnv(t, srv.URL, notas, spy, profUser())

	code, w := doJSON(t, r, http.MethodPost, "/citas/c1/nota", mintProf(t, tk),
		`{"contenido":"  evolución del paciente  "}`)
	if code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	if notas.gotContenido != "evolución del paciente" {
		t.Fatalf("contenido = %q", notas.gotContenido)
	}
	if notas.gotProfID != "prof-1" || notas.gotCitaID != "c1" {
		t.Fatalf("args = %s %s", notas.gotProfID, notas.gotCitaID)
	}
	if notas.gotActor.IdpSub != "test-user" || notas.gotActor.UsuarioID != "u-prof" {
		t.Fatalf("actor = %+v", notas.gotActor)
	}
	// I5: la clínica viene del token.
	if notas.gotClinica != "clinica-norte" {
		t.Fatalf("clínica = %s", notas.gotClinica)
	}
}

func TestCreateNotaSoloProfesional403(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	notas := &fakeNotaStore{cita: citaAtendidaPropia, profID: "prof-1"}
	r := setupNotasTestEnv(t, srv.URL, notas, &fakeAuditStore{}, admClinicaUser())

	// Matriz RF17: OpNotaClinica solo profesional.
	code, _ := doJSON(t, r, http.MethodPost, "/citas/c1/nota", mintAdm(t, tk), `{"contenido":"x"}`)
	if code != http.StatusForbidden {
		t.Fatalf("adm-clinica = %d, want 403", code)
	}
}

func TestCreateNotaSoloPropia403(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	// La cita es de otro profesional.
	notas := &fakeNotaStore{
		cita:   CitaNotaInfo{ID: "c1", ProfesionalID: "prof-otro", PacienteID: "p1", Estado: "atendida"},
		profID: "prof-1",
	}
	r := setupNotasTestEnv(t, srv.URL, notas, &fakeAuditStore{}, profUser())

	code, w := doJSON(t, r, http.MethodPost, "/citas/c1/nota", mintProf(t, tk), `{"contenido":"x"}`)
	if code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
}

func TestCreateNotaUsuarioNoProfesional403(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	notas := &fakeNotaStore{cita: citaAtendidaPropia, profErr: errNotaSinProfesional}
	r := setupNotasTestEnv(t, srv.URL, notas, &fakeAuditStore{}, profUser())

	if code, _ := doJSON(t, r, http.MethodPost, "/citas/c1/nota", mintProf(t, tk), `{"contenido":"x"}`); code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", code)
	}
}

func TestCreateNotaValidaciones(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	r := setupNotasTestEnv(t, srv.URL, &fakeNotaStore{}, &fakeAuditStore{}, profUser())

	// Contenido vacío → 400.
	code, _ := doJSON(t, r, http.MethodPost, "/citas/c1/nota", mintProf(t, tk), `{"contenido":"   "}`)
	if code != http.StatusBadRequest {
		t.Fatalf("contenido vacío = %d, want 400", code)
	}

	// Cita ajena a la clínica → 404 (I5, sin filtrar existencia).
	notas := &fakeNotaStore{citaErr: errNotaCitaFuera}
	r = setupNotasTestEnv(t, srv.URL, notas, &fakeAuditStore{}, profUser())
	code, _ = doJSON(t, r, http.MethodPost, "/citas/ajena/nota", mintProf(t, tk), `{"contenido":"x"}`)
	if code != http.StatusNotFound {
		t.Fatalf("cita ajena = %d, want 404", code)
	}

	// Cita no atendida → 409 (máquina de estados).
	notas = &fakeNotaStore{
		cita:   CitaNotaInfo{ID: "c1", ProfesionalID: "prof-1", PacienteID: "p1", Estado: "reservada"},
		profID: "prof-1",
	}
	r = setupNotasTestEnv(t, srv.URL, notas, &fakeAuditStore{}, profUser())
	code, _ = doJSON(t, r, http.MethodPost, "/citas/c1/nota", mintProf(t, tk), `{"contenido":"x"}`)
	if code != http.StatusConflict {
		t.Fatalf("cita no atendida = %d, want 409", code)
	}

	// Nota duplicada → 409.
	notas = &fakeNotaStore{cita: citaAtendidaPropia, profID: "prof-1", createErr: errNotaYaExiste}
	r = setupNotasTestEnv(t, srv.URL, notas, &fakeAuditStore{}, profUser())
	code, _ = doJSON(t, r, http.MethodPost, "/citas/c1/nota", mintProf(t, tk), `{"contenido":"x"}`)
	if code != http.StatusConflict {
		t.Fatalf("nota duplicada = %d, want 409", code)
	}
}

// --- RF15 + RF18: lectura ---

func TestGetNotaOKAuditaLectura(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	notas := &fakeNotaStore{
		cita:   citaAtendidaPropia,
		profID: "prof-1",
		nota:   NotaClinica{ID: "n1", CitaID: "c1", ProfesionalID: "prof-1", Contenido: "evolución"},
	}
	spy := &fakeAuditStore{}
	r := setupNotasTestEnv(t, srv.URL, notas, spy, profUser())

	code, w := doGet(t, r, "/citas/c1/nota", mintProf(t, tk))
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	if len(spy.eventos) != 1 {
		t.Fatalf("eventos = %+v", spy.eventos)
	}
	ev := spy.eventos[0]
	if ev.Accion != accionLectura || ev.Entidad != entidadNotaClinica ||
		ev.EntidadID != "n1" || ev.PacienteID != "p1" {
		t.Fatalf("evento = %+v", ev)
	}
}

func TestGetNotaSinRastroNoSirve(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	notas := &fakeNotaStore{
		cita:   citaAtendidaPropia,
		profID: "prof-1",
		nota:   NotaClinica{ID: "n1", Contenido: "evolución"},
	}
	spy := &fakeAuditStore{regErr: errors.New("boom")}
	r := setupNotasTestEnv(t, srv.URL, notas, spy, profUser())

	code, w := doGet(t, r, "/citas/c1/nota", mintProf(t, tk))
	if code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500", code)
	}
	if contains(w.Body.String(), "evolución") {
		t.Fatalf("se sirvió la nota sin rastro: %s", w.Body.String())
	}
}

func TestGetNotaNoExiste404SinAuditar(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	notas := &fakeNotaStore{cita: citaAtendidaPropia, profID: "prof-1", notaErr: errNotaNoExiste}
	spy := &fakeAuditStore{}
	r := setupNotasTestEnv(t, srv.URL, notas, spy, profUser())

	if code, _ := doGet(t, r, "/citas/c1/nota", mintProf(t, tk)); code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", code)
	}
	if len(spy.eventos) != 0 {
		t.Fatalf("se audita un 404: %+v", spy.eventos)
	}
}
