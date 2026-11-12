package main

// Pruebas de integración entre los handlers de RF5/RF6 y la invalidación de
// disponibilidad. Usan stores falsos y una Cache real con Store en memoria:
// no requieren PostgreSQL ni Redis.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func setupInvalidationHandlerTest(t *testing.T, sched ScheduleStore, blocks BlockStore) (*gin.Engine, *testKeys) {
	t.Helper()

	tk := newTestKeys(t)
	srv := tk.jwksServer()
	t.Cleanup(srv.Close)

	return setupAgendaTestEnv(t, srv.URL, sched, blocks, admAgendaUser()), tk
}

func newInvalidationCacheStore(t *testing.T) *recordingCacheStore {
	t.Helper()
	store := newRecordingCacheStore()
	installAvailabilityCache(t, store)
	return store
}

func TestCreateHorarioInvalidaSoloLaAgendaDelToken(t *testing.T) {
	store := &fakeScheduleStore{
		owners:       map[string]string{"p1": "u-prof", "p2": "u-prof-2"},
		byProf:       map[string][]Schedule{},
		horarioOwner: map[string]string{},
	}
	cacheStore := newInvalidationCacheStore(t)
	old := strings.Repeat("a", 32)
	seedEpoch(cacheStore, "clinica-norte", "p1", old)
	seedEpoch(cacheStore, "clinica-norte", "p2", old)
	seedEpoch(cacheStore, "clinica-sur", "p1", old)

	r, tk := setupInvalidationHandlerTest(t, store, &fakeBlockStore{})
	body := `{"diaSemana":1,"horaInicio":"09:00","horaFin":"12:00","vigenteDesde":"2026-01-01"}`
	code, w := doJSON(t, r, http.MethodPost, "/profesionales/p1/horarios?clinicaId=clinica-sur",
		mintAgenda(t, tk, []string{RoleAdmClinica}), body)
	if code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}

	assertEpochRotated(t, cacheStore, "clinica-norte", "p1", old)
	assertEpochUnchanged(t, cacheStore, "clinica-norte", "p2", old)
	assertEpochUnchanged(t, cacheStore, "clinica-sur", "p1", old)
}

func TestCreateHorarioRechazadoNoInvalidaCache(t *testing.T) {
	store := &fakeScheduleStore{
		owners:       map[string]string{"p1": "u-prof"},
		byProf:       map[string][]Schedule{},
		horarioOwner: map[string]string{},
		createErr:    errHorarioSolapado,
	}
	cacheStore := newInvalidationCacheStore(t)
	old := strings.Repeat("b", 32)
	seedEpoch(cacheStore, "clinica-norte", "p1", old)

	r, tk := setupInvalidationHandlerTest(t, store, &fakeBlockStore{})
	body := `{"diaSemana":1,"horaInicio":"09:00","horaFin":"12:00","vigenteDesde":"2026-01-01"}`
	code, _ := doJSON(t, r, http.MethodPost, "/profesionales/p1/horarios",
		mintAgenda(t, tk, []string{RoleAdmClinica}), body)
	if code != http.StatusConflict {
		t.Fatalf("status=%d want 409", code)
	}
	if got := cacheStore.setCount(); got != 0 {
		t.Fatalf("Set() calls = %d, want 0 cuando PostgreSQL rechaza", got)
	}
	assertEpochUnchanged(t, cacheStore, "clinica-norte", "p1", old)
}

func TestUpdateHorarioInvalidaCacheDelProfesionalResuelto(t *testing.T) {
	store := &fakeScheduleStore{
		owners: map[string]string{"p1": "u-prof", "p2": "u-prof-2"},
		byProf: map[string][]Schedule{
			"p1": {{ID: "h1", ProfesionalID: "p1", DiaSemana: 1, HoraInicio: "09:00:00", HoraFin: "12:00:00", VigenteDesde: "2026-01-01", Activo: true}},
		},
		horarioOwner: map[string]string{"h1": "p1"},
	}
	cacheStore := newInvalidationCacheStore(t)
	old := strings.Repeat("c", 32)
	seedEpoch(cacheStore, "clinica-norte", "p1", old)
	seedEpoch(cacheStore, "clinica-norte", "p2", old)

	r, tk := setupInvalidationHandlerTest(t, store, &fakeBlockStore{})
	code, w := doJSON(t, r, http.MethodPatch, "/horarios/h1",
		mintAgenda(t, tk, []string{RoleAdmClinica}), `{"activo":false}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}

	assertEpochRotated(t, cacheStore, "clinica-norte", "p1", old)
	assertEpochUnchanged(t, cacheStore, "clinica-norte", "p2", old)
}

func TestUpdateHorarioRechazadoNoInvalidaCache(t *testing.T) {
	store := &fakeScheduleStore{
		owners:       map[string]string{"p1": "u-prof"},
		byProf:       map[string][]Schedule{"p1": {{ID: "h1", ProfesionalID: "p1", Activo: true}}},
		horarioOwner: map[string]string{"h1": "p1"},
		updateErr:    errHorarioDatosInvalidos,
	}
	cacheStore := newInvalidationCacheStore(t)
	old := strings.Repeat("d", 32)
	seedEpoch(cacheStore, "clinica-norte", "p1", old)

	r, tk := setupInvalidationHandlerTest(t, store, &fakeBlockStore{})
	code, _ := doJSON(t, r, http.MethodPatch, "/horarios/h1",
		mintAgenda(t, tk, []string{RoleAdmClinica}), `{"activo":false}`)
	if code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", code)
	}
	if got := cacheStore.setCount(); got != 0 {
		t.Fatalf("Set() calls = %d, want 0 cuando PostgreSQL rechaza", got)
	}
	assertEpochUnchanged(t, cacheStore, "clinica-norte", "p1", old)
}

func TestCreateBloqueoInvalidaCache(t *testing.T) {
	sched := &fakeScheduleStore{
		owners: map[string]string{"p1": "u-prof"}, byProf: map[string][]Schedule{}, horarioOwner: map[string]string{},
	}
	cacheStore := newInvalidationCacheStore(t)
	old := strings.Repeat("e", 32)
	seedEpoch(cacheStore, "clinica-norte", "p1", old)

	r, tk := setupInvalidationHandlerTest(t, sched, &fakeBlockStore{})
	body := `{"inicio":"2026-11-20T14:00:00Z","fin":"2026-11-20T18:00:00Z","motivo":"Vacaciones"}`
	code, w := doJSON(t, r, http.MethodPost, "/profesionales/p1/bloqueos",
		mintAgenda(t, tk, []string{RoleAdmClinica}), body)
	if code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}

	assertEpochRotated(t, cacheStore, "clinica-norte", "p1", old)
}

func TestCreateBloqueoRechazadoNoInvalidaCache(t *testing.T) {
	sched := &fakeScheduleStore{
		owners: map[string]string{"p1": "u-prof"}, byProf: map[string][]Schedule{}, horarioOwner: map[string]string{},
	}
	cacheStore := newInvalidationCacheStore(t)
	old := strings.Repeat("f", 32)
	seedEpoch(cacheStore, "clinica-norte", "p1", old)

	r, tk := setupInvalidationHandlerTest(t, sched, &fakeBlockStore{createErr: errBloqueoDatosInvalidos})
	body := `{"inicio":"2026-11-20T14:00:00Z","fin":"2026-11-20T18:00:00Z","motivo":"Vacaciones"}`
	code, _ := doJSON(t, r, http.MethodPost, "/profesionales/p1/bloqueos",
		mintAgenda(t, tk, []string{RoleAdmClinica}), body)
	if code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", code)
	}
	if got := cacheStore.setCount(); got != 0 {
		t.Fatalf("Set() calls = %d, want 0 cuando PostgreSQL rechaza", got)
	}
	assertEpochUnchanged(t, cacheStore, "clinica-norte", "p1", old)
}

// partialCommitBlockStore simula el caso en que el INSERT del bloqueo se
// confirma pero falla la consulta posterior de conflictos.
type partialCommitBlockStore struct {
	*fakeBlockStore
	conflictErr error
}

func (s *partialCommitBlockStore) CreateBloqueo(ctx context.Context, clinicaIdent, profesionalID, creadoPor string, inicio, fin time.Time, motivo string) (Block, []BlockConflict, error) {
	b, conflicts, err := s.fakeBlockStore.CreateBloqueo(ctx, clinicaIdent, profesionalID, creadoPor, inicio, fin, motivo)
	if err != nil {
		return b, conflicts, err
	}
	return b, conflicts, s.conflictErr
}

func TestCreateBloqueoCommitParcialInvalidaCache(t *testing.T) {
	sched := &fakeScheduleStore{
		owners: map[string]string{"p1": "u-prof"}, byProf: map[string][]Schedule{}, horarioOwner: map[string]string{},
	}
	blocks := &partialCommitBlockStore{
		fakeBlockStore: &fakeBlockStore{},
		conflictErr:    errors.New("falló el listado de conflictos"),
	}
	cacheStore := newInvalidationCacheStore(t)
	old := strings.Repeat("1", 32)
	seedEpoch(cacheStore, "clinica-norte", "p1", old)

	r, tk := setupInvalidationHandlerTest(t, sched, blocks)
	body := `{"inicio":"2026-11-20T14:00:00Z","fin":"2026-11-20T18:00:00Z","motivo":"Vacaciones"}`
	code, _ := doJSON(t, r, http.MethodPost, "/profesionales/p1/bloqueos",
		mintAgenda(t, tk, []string{RoleAdmClinica}), body)
	if code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", code)
	}
	if len(blocks.byProf["p1"]) != 1 {
		t.Fatalf("el bloqueo no quedó creado en el store falso")
	}
	assertEpochRotated(t, cacheStore, "clinica-norte", "p1", old)
}

func TestDeleteBloqueoInvalidaCache(t *testing.T) {
	sched := &fakeScheduleStore{
		owners: map[string]string{"p1": "u-prof", "p2": "u-prof-2"}, byProf: map[string][]Schedule{}, horarioOwner: map[string]string{},
	}
	blocks := &fakeBlockStore{
		byProf:       map[string][]Block{"p1": {{ID: "b1", ProfesionalID: "p1", Motivo: "x"}}},
		bloqueoOwner: map[string]string{"b1": "p1"},
	}
	cacheStore := newInvalidationCacheStore(t)
	old := strings.Repeat("2", 32)
	seedEpoch(cacheStore, "clinica-norte", "p1", old)
	seedEpoch(cacheStore, "clinica-norte", "p2", old)

	r, tk := setupInvalidationHandlerTest(t, sched, blocks)
	code, _ := doJSON(t, r, http.MethodDelete, "/bloqueos/b1",
		mintAgenda(t, tk, []string{RoleAdmClinica}), "")
	if code != http.StatusNoContent {
		t.Fatalf("status=%d want 204", code)
	}

	assertEpochRotated(t, cacheStore, "clinica-norte", "p1", old)
	assertEpochUnchanged(t, cacheStore, "clinica-norte", "p2", old)
}

func TestDeleteBloqueoRechazadoNoInvalidaCache(t *testing.T) {
	sched := &fakeScheduleStore{
		owners: map[string]string{"p1": "u-prof"}, byProf: map[string][]Schedule{}, horarioOwner: map[string]string{},
	}
	cacheStore := newInvalidationCacheStore(t)
	old := strings.Repeat("3", 32)
	seedEpoch(cacheStore, "clinica-norte", "p1", old)

	r, tk := setupInvalidationHandlerTest(t, sched, &fakeBlockStore{})
	code, _ := doJSON(t, r, http.MethodDelete, "/bloqueos/b1",
		mintAgenda(t, tk, []string{RoleAdmClinica}), "")
	if code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", code)
	}
	if got := cacheStore.setCount(); got != 0 {
		t.Fatalf("Set() calls = %d, want 0 cuando PostgreSQL rechaza", got)
	}
	assertEpochUnchanged(t, cacheStore, "clinica-norte", "p1", old)
}

func TestCreateHorarioInvalidacionFallidaResponde201(t *testing.T) {
	store := &fakeScheduleStore{
		owners:       map[string]string{"p1": "u-prof"},
		byProf:       map[string][]Schedule{},
		horarioOwner: map[string]string{},
	}
	cacheStore := newInvalidationCacheStore(t)
	cacheStore.setErr = errors.New("redis caído")

	r, tk := setupInvalidationHandlerTest(t, store, &fakeBlockStore{})
	body := `{"diaSemana":1,"horaInicio":"09:00","horaFin":"12:00","vigenteDesde":"2026-01-01"}`
	code, w := doJSON(t, r, http.MethodPost, "/profesionales/p1/horarios",
		mintAgenda(t, tk, []string{RoleAdmClinica}), body)
	if code != http.StatusCreated {
		t.Fatalf("status=%d body=%s; el COMMIT ya ocurrió", code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"horario"`) {
		t.Fatalf("respuesta sin horario: %s", w.Body.String())
	}
}

func TestDeleteBloqueoInvalidacionFallidaResponde204(t *testing.T) {
	sched := &fakeScheduleStore{
		owners: map[string]string{"p1": "u-prof"}, byProf: map[string][]Schedule{}, horarioOwner: map[string]string{},
	}
	blocks := &fakeBlockStore{
		byProf:       map[string][]Block{"p1": {{ID: "b1", ProfesionalID: "p1", Motivo: "x"}}},
		bloqueoOwner: map[string]string{"b1": "p1"},
	}
	cacheStore := newInvalidationCacheStore(t)
	cacheStore.setErr = errors.New("redis caído")

	r, tk := setupInvalidationHandlerTest(t, sched, blocks)
	code, _ := doJSON(t, r, http.MethodDelete, "/bloqueos/b1",
		mintAgenda(t, tk, []string{RoleAdmClinica}), "")
	if code != http.StatusNoContent {
		t.Fatalf("status=%d want 204; el COMMIT ya ocurrió", code)
	}
}
