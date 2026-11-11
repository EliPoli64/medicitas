package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type fakeScheduleStore struct {
	// profesionalID -> usuarioID
	owners map[string]string
	// profesionalID -> horarios
	byProf map[string][]Schedule
	// horarioID -> profesionalID
	horarioOwner map[string]string
	createErr    error
	updateErr    error
	lastClinica  string
}

func (f *fakeScheduleStore) ProfesionalUsuarioID(_ context.Context, clinicaIdent, profesionalID string) (string, error) {
	f.lastClinica = clinicaIdent
	u, ok := f.owners[profesionalID]
	if !ok {
		return "", errProfesionalAgendaFuera
	}
	return u, nil
}

func (f *fakeScheduleStore) HorarioProfesionalID(_ context.Context, _, horarioID string) (string, error) {
	p, ok := f.horarioOwner[horarioID]
	if !ok {
		return "", errHorarioFueraDeClinica
	}
	return p, nil
}

func (f *fakeScheduleStore) ListHorarios(_ context.Context, clinicaIdent, profesionalID string, soloActivos bool) ([]Schedule, error) {
	f.lastClinica = clinicaIdent
	out := make([]Schedule, 0)
	for _, h := range f.byProf[profesionalID] {
		if soloActivos && !h.Activo {
			continue
		}
		out = append(out, h)
	}
	return out, nil
}

func (f *fakeScheduleStore) CreateHorario(_ context.Context, clinicaIdent, profesionalID string, in Schedule) (Schedule, error) {
	f.lastClinica = clinicaIdent
	if _, ok := f.owners[profesionalID]; !ok {
		return Schedule{}, errProfesionalAgendaFuera
	}
	if f.createErr != nil {
		return Schedule{}, f.createErr
	}
	in.ID = "h-new"
	in.ProfesionalID = profesionalID
	f.byProf[profesionalID] = append(f.byProf[profesionalID], in)
	f.horarioOwner[in.ID] = profesionalID
	return in, nil
}

func (f *fakeScheduleStore) UpdateHorario(_ context.Context, clinicaIdent, horarioID string, patch SchedulePatch) (Schedule, error) {
	f.lastClinica = clinicaIdent
	if f.updateErr != nil {
		return Schedule{}, f.updateErr
	}
	prof, ok := f.horarioOwner[horarioID]
	if !ok {
		return Schedule{}, errHorarioFueraDeClinica
	}
	list := f.byProf[prof]
	for i := range list {
		if list[i].ID != horarioID {
			continue
		}
		if patch.Activo != nil {
			list[i].Activo = *patch.Activo
		}
		if patch.DiaSemana != nil {
			list[i].DiaSemana = *patch.DiaSemana
		}
		f.byProf[prof] = list
		return list[i], nil
	}
	return Schedule{}, errHorarioFueraDeClinica
}

func setupAgendaTestEnv(t *testing.T, srvURL string, schedules ScheduleStore, blocks BlockStore, users map[string]DomainUser) *gin.Engine {
	t.Helper()
	withTestIssuer(t, srvURL)

	prevPing := pingDBFn
	pingDBFn = func(context.Context) error { return nil }
	t.Cleanup(func() { pingDBFn = prevPing })

	prevUsers := userStore
	userStore = &fakeUserStore{bySub: users}
	t.Cleanup(func() { userStore = prevUsers })

	prevS := scheduleStore
	scheduleStore = schedules
	t.Cleanup(func() { scheduleStore = prevS })

	prevB := blockStore
	blockStore = blocks
	t.Cleanup(func() { blockStore = prevB })

	gin.SetMode(gin.TestMode)
	return setupRouter()
}

func admAgendaUser() map[string]DomainUser {
	clinica := "clinica-norte"
	return map[string]DomainUser{
		"test-user": {
			UsuarioID: "u-adm", IdpSub: "test-user", Activo: true,
			ClinicaIdentificador: &clinica, RolCodigo: "adm_clinica",
		},
	}
}

func profAgendaUser(usuarioID string) map[string]DomainUser {
	clinica := "clinica-norte"
	return map[string]DomainUser{
		"test-user": {
			UsuarioID: usuarioID, IdpSub: "test-user", Activo: true,
			ClinicaIdentificador: &clinica, RolCodigo: "profesional",
		},
	}
}

func mintAgenda(t *testing.T, tk *testKeys, roles []string) string {
	t.Helper()
	return tk.mint(testIssuer, time.Now().Add(time.Hour), true, roles, testKid, "clinica-norte")
}

func TestCreateHorarioOK(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	store := &fakeScheduleStore{
		owners:       map[string]string{"p1": "u-prof"},
		byProf:       map[string][]Schedule{},
		horarioOwner: map[string]string{},
	}
	r := setupAgendaTestEnv(t, srv.URL, store, &fakeBlockStore{}, admAgendaUser())
	body := `{"diaSemana":1,"horaInicio":"09:00","horaFin":"12:00","vigenteDesde":"2026-01-01"}`
	code, w := doJSON(t, r, http.MethodPost, "/profesionales/p1/horarios", mintAgenda(t, tk, []string{RoleAdmClinica}), body)
	if code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	if store.lastClinica != "clinica-norte" {
		t.Fatalf("clinica=%s", store.lastClinica)
	}
}

func TestCreateHorarioI5IgnoresQuery(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakeScheduleStore{
		owners: map[string]string{"p1": "u-prof"}, byProf: map[string][]Schedule{}, horarioOwner: map[string]string{},
	}
	r := setupAgendaTestEnv(t, srv.URL, store, &fakeBlockStore{}, admAgendaUser())
	body := `{"diaSemana":2,"horaInicio":"09:00","horaFin":"10:00","vigenteDesde":"2026-01-01"}`
	code, _ := doJSON(t, r, http.MethodPost, "/profesionales/p1/horarios?clinicaId=clinica-sur",
		mintAgenda(t, tk, []string{RoleAdmClinica}), body)
	if code != http.StatusCreated {
		t.Fatalf("status=%d", code)
	}
	if store.lastClinica != "clinica-norte" {
		t.Fatalf("I5 fail clinica=%s", store.lastClinica)
	}
}

func TestCreateHorarioSolape409(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakeScheduleStore{
		owners: map[string]string{"p1": "u-prof"}, byProf: map[string][]Schedule{}, horarioOwner: map[string]string{},
		createErr: errHorarioSolapado,
	}
	r := setupAgendaTestEnv(t, srv.URL, store, &fakeBlockStore{}, admAgendaUser())
	body := `{"diaSemana":1,"horaInicio":"09:00","horaFin":"12:00","vigenteDesde":"2026-01-01"}`
	code, _ := doJSON(t, r, http.MethodPost, "/profesionales/p1/horarios", mintAgenda(t, tk, []string{RoleAdmClinica}), body)
	if code != http.StatusConflict {
		t.Fatalf("status=%d want 409", code)
	}
}

func TestHorarioProfesionalOtro403(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakeScheduleStore{
		owners: map[string]string{"p-otro": "u-otro", "p-mio": "u-mio"},
		byProf: map[string][]Schedule{}, horarioOwner: map[string]string{},
	}
	r := setupAgendaTestEnv(t, srv.URL, store, &fakeBlockStore{}, profAgendaUser("u-mio"))
	body := `{"diaSemana":1,"horaInicio":"09:00","horaFin":"12:00","vigenteDesde":"2026-01-01"}`
	code, _ := doJSON(t, r, http.MethodPost, "/profesionales/p-otro/horarios",
		mintAgenda(t, tk, []string{RoleProfesional}), body)
	if code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", code)
	}
}

func TestHorarioProfesionalPropioOK(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakeScheduleStore{
		owners: map[string]string{"p-mio": "u-mio"},
		byProf: map[string][]Schedule{}, horarioOwner: map[string]string{},
	}
	r := setupAgendaTestEnv(t, srv.URL, store, &fakeBlockStore{}, profAgendaUser("u-mio"))
	body := `{"diaSemana":1,"horaInicio":"09:00","horaFin":"12:00","vigenteDesde":"2026-01-01"}`
	code, _ := doJSON(t, r, http.MethodPost, "/profesionales/p-mio/horarios",
		mintAgenda(t, tk, []string{RoleProfesional}), body)
	if code != http.StatusCreated {
		t.Fatalf("status=%d want 201", code)
	}
}

func TestListHorariosUnauthorized(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakeScheduleStore{owners: map[string]string{"p1": "u"}, byProf: map[string][]Schedule{}}
	r := setupAgendaTestEnv(t, srv.URL, store, &fakeBlockStore{}, admAgendaUser())
	code, _ := doGet(t, r, "/profesionales/p1/horarios", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", code)
	}
}

func TestPatchHorarioDesactivar(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakeScheduleStore{
		owners: map[string]string{"p1": "u-prof"},
		byProf: map[string][]Schedule{
			"p1": {{ID: "h1", ProfesionalID: "p1", DiaSemana: 1, HoraInicio: "09:00:00", HoraFin: "12:00:00", VigenteDesde: "2026-01-01", Activo: true}},
		},
		horarioOwner: map[string]string{"h1": "p1"},
	}
	r := setupAgendaTestEnv(t, srv.URL, store, &fakeBlockStore{}, admAgendaUser())
	code, w := doJSON(t, r, http.MethodPatch, "/horarios/h1", mintAgenda(t, tk, []string{RoleAdmClinica}), `{"activo":false}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	var body struct {
		Horario Schedule `json:"horario"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Horario.Activo {
		t.Fatal("expected activo=false")
	}
}

func TestRecepcionHorarios403(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	clinica := "clinica-norte"
	store := &fakeScheduleStore{owners: map[string]string{"p1": "u"}, byProf: map[string][]Schedule{}}
	r := setupAgendaTestEnv(t, srv.URL, store, &fakeBlockStore{}, map[string]DomainUser{
		"test-user": {UsuarioID: "u-r", IdpSub: "test-user", Activo: true, ClinicaIdentificador: &clinica, RolCodigo: "recepcion"},
	})
	code, _ := doGet(t, r, "/profesionales/p1/horarios", mintAgenda(t, tk, []string{RoleRecepcion}))
	if code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", code)
	}
}
