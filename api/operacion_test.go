package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type fakeAgendaStore struct {
	zona         string
	owners       map[string]string // profesionalID -> usuarioID
	byUsuario    map[string]string // usuarioID -> profesionalID
	citas        []AgendaCita
	lastClinica  string
	lastInicio   time.Time
	lastFin      time.Time
	lastProfFilt *string
}

func (f *fakeAgendaStore) ZonaHoraria(_ context.Context, clinicaIdent string) (string, error) {
	f.lastClinica = clinicaIdent
	if f.zona == "" {
		return "America/Costa_Rica", nil
	}
	return f.zona, nil
}

func (f *fakeAgendaStore) ProfesionalUsuarioID(_ context.Context, clinicaIdent, profesionalID string) (string, error) {
	f.lastClinica = clinicaIdent
	u, ok := f.owners[profesionalID]
	if !ok {
		return "", errAgendaProfesionalFuera
	}
	return u, nil
}

func (f *fakeAgendaStore) ProfesionalIDByUsuarioID(_ context.Context, clinicaIdent, usuarioID string) (string, error) {
	f.lastClinica = clinicaIdent
	id, ok := f.byUsuario[usuarioID]
	if !ok {
		return "", errAgendaProfesionalFuera
	}
	return id, nil
}

func (f *fakeAgendaStore) ListAgendaDia(_ context.Context, clinicaIdent string, inicio, fin time.Time, profesionalID *string) ([]AgendaCita, error) {
	f.lastClinica = clinicaIdent
	f.lastInicio = inicio
	f.lastFin = fin
	f.lastProfFilt = profesionalID
	out := make([]AgendaCita, 0)
	for _, c := range f.citas {
		if profesionalID != nil && c.ProfesionalID != *profesionalID {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

type fakeStatsStore struct {
	zona        string
	owners      map[string]string
	byUsuario   map[string]string
	counts      map[string]int
	lastClinica string
	lastProf    *string
}

func (f *fakeStatsStore) ZonaHoraria(_ context.Context, clinicaIdent string) (string, error) {
	f.lastClinica = clinicaIdent
	if f.zona == "" {
		return "America/Costa_Rica", nil
	}
	return f.zona, nil
}

func (f *fakeStatsStore) ProfesionalUsuarioID(_ context.Context, clinicaIdent, profesionalID string) (string, error) {
	f.lastClinica = clinicaIdent
	u, ok := f.owners[profesionalID]
	if !ok {
		return "", errAgendaProfesionalFuera
	}
	return u, nil
}

func (f *fakeStatsStore) ProfesionalIDByUsuarioID(_ context.Context, clinicaIdent, usuarioID string) (string, error) {
	f.lastClinica = clinicaIdent
	id, ok := f.byUsuario[usuarioID]
	if !ok {
		return "", errAgendaProfesionalFuera
	}
	return id, nil
}

func (f *fakeStatsStore) CountByEstado(_ context.Context, clinicaIdent string, _, _ time.Time, profesionalID *string) (map[string]int, error) {
	f.lastClinica = clinicaIdent
	f.lastProf = profesionalID
	out := emptyEstadoCounts()
	for k, v := range f.counts {
		out[k] = v
	}
	return out, nil
}

func setupOperacionEnv(t *testing.T, srvURL string, agenda AgendaStore, stats StatsStore, users map[string]DomainUser) *gin.Engine {
	t.Helper()
	withTestIssuer(t, srvURL)
	prevPing := pingDBFn
	pingDBFn = func(context.Context) error { return nil }
	t.Cleanup(func() { pingDBFn = prevPing })

	prevUsers := userStore
	userStore = &fakeUserStore{bySub: users}
	t.Cleanup(func() { userStore = prevUsers })

	prevA := agendaStore
	agendaStore = agenda
	t.Cleanup(func() { agendaStore = prevA })

	prevS := statsStore
	statsStore = stats
	t.Cleanup(func() { statsStore = prevS })

	gin.SetMode(gin.TestMode)
	return setupRouter()
}

func recepOperacionUser() map[string]DomainUser {
	clinica := "clinica-norte"
	return map[string]DomainUser{
		"test-user": {
			UsuarioID: "u-recep", IdpSub: "test-user", Activo: true,
			ClinicaIdentificador: &clinica, RolCodigo: "recepcion",
		},
	}
}

func profOperacionUser(usuarioID string) map[string]DomainUser {
	clinica := "clinica-norte"
	return map[string]DomainUser{
		"test-user": {
			UsuarioID: usuarioID, IdpSub: "test-user", Activo: true,
			ClinicaIdentificador: &clinica, RolCodigo: "profesional",
		},
	}
}

func TestAgendaDiaOK(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	store := &fakeAgendaStore{
		citas: []AgendaCita{
			{ID: "c1", Estado: "reservada", ProfesionalID: "p1", PacienteNombre: "Ana", ServicioNombre: "Ctrl"},
		},
		owners: map[string]string{"p1": "u-prof"},
	}
	r := setupOperacionEnv(t, srv.URL, store, &fakeStatsStore{}, recepOperacionUser())
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")

	code, w := doGet(t, r, "/agenda?fecha=2026-11-12", tok)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	var body struct {
		ClinicaIdentificador string       `json:"clinicaIdentificador"`
		Fecha                string       `json:"fecha"`
		ZonaHoraria          string       `json:"zonaHoraria"`
		Citas                []AgendaCita `json:"citas"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ClinicaIdentificador != "clinica-norte" || body.Fecha != "2026-11-12" {
		t.Fatalf("body=%+v", body)
	}
	if body.ZonaHoraria != "America/Costa_Rica" || len(body.Citas) != 1 {
		t.Fatalf("zona/citas=%+v", body)
	}
	if store.lastClinica != "clinica-norte" {
		t.Fatalf("I5 clinica=%s", store.lastClinica)
	}
	// Día en America/Costa_Rica = UTC-6 → 2026-11-12 06:00Z .. 2026-11-13 06:00Z
	wantStart := time.Date(2026, 11, 12, 6, 0, 0, 0, time.UTC)
	if !store.lastInicio.UTC().Equal(wantStart) {
		t.Fatalf("inicio=%v want %v", store.lastInicio.UTC(), wantStart)
	}
}

func TestAgendaDiaI5IgnoresQuery(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakeAgendaStore{citas: []AgendaCita{}}
	r := setupOperacionEnv(t, srv.URL, store, &fakeStatsStore{}, recepOperacionUser())
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")

	code, _ := doGet(t, r, "/agenda?fecha=2026-11-12&clinicaId=clinica-sur", tok)
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	if store.lastClinica != "clinica-norte" {
		t.Fatalf("tenant leak: %s", store.lastClinica)
	}
}

func TestAgendaProfesionalSoloPropia(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakeAgendaStore{
		owners:    map[string]string{"p-mio": "u-prof", "p-otro": "u-otro"},
		byUsuario: map[string]string{"u-prof": "p-mio"},
		citas: []AgendaCita{
			{ID: "c1", ProfesionalID: "p-mio"},
			{ID: "c2", ProfesionalID: "p-otro"},
		},
	}
	r := setupOperacionEnv(t, srv.URL, store, &fakeStatsStore{}, profOperacionUser("u-prof"))
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleProfesional}, testKid, "clinica-norte")

	code, w := doGet(t, r, "/agenda?fecha=2026-11-12", tok)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	if store.lastProfFilt == nil || *store.lastProfFilt != "p-mio" {
		t.Fatalf("filtro=%v", store.lastProfFilt)
	}
	var body struct {
		Citas []AgendaCita `json:"citas"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if len(body.Citas) != 1 || body.Citas[0].ID != "c1" {
		t.Fatalf("citas=%+v", body.Citas)
	}
}

func TestAgendaProfesionalOtro403(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakeAgendaStore{
		owners:    map[string]string{"p-mio": "u-prof", "p-otro": "u-otro"},
		byUsuario: map[string]string{"u-prof": "p-mio"},
	}
	r := setupOperacionEnv(t, srv.URL, store, &fakeStatsStore{}, profOperacionUser("u-prof"))
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleProfesional}, testKid, "clinica-norte")

	code, _ := doGet(t, r, "/agenda?fecha=2026-11-12&profesionalId=p-otro", tok)
	if code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", code)
	}
}

func TestAgendaRecepProfesionalInexistente404(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakeAgendaStore{owners: map[string]string{}}
	r := setupOperacionEnv(t, srv.URL, store, &fakeStatsStore{}, recepOperacionUser())
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")

	code, _ := doGet(t, r, "/agenda?fecha=2026-11-12&profesionalId=no-existe", tok)
	if code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", code)
	}
}

func TestAgendaFechaObligatoria(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	r := setupOperacionEnv(t, srv.URL, &fakeAgendaStore{}, &fakeStatsStore{}, recepOperacionUser())
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")
	code, _ := doGet(t, r, "/agenda", tok)
	if code != http.StatusBadRequest {
		t.Fatalf("status=%d", code)
	}
}

func TestCitasResumenOK(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakeStatsStore{counts: map[string]int{"reservada": 12, "atendida": 8, "ausente": 1, "cancelada": 2, "reprogramada": 1}}
	r := setupOperacionEnv(t, srv.URL, &fakeAgendaStore{}, store, recepOperacionUser())
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")

	code, w := doGet(t, r, "/citas/resumen?desde=2026-11-01&hasta=2026-11-30&clinicaId=clinica-sur", tok)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	var body struct {
		ClinicaIdentificador string         `json:"clinicaIdentificador"`
		PorEstado            map[string]int `json:"porEstado"`
		Total                int            `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ClinicaIdentificador != "clinica-norte" {
		t.Fatalf("I5: %s", body.ClinicaIdentificador)
	}
	if store.lastClinica != "clinica-norte" {
		t.Fatalf("store clinica=%s", store.lastClinica)
	}
	for _, e := range estadosCitaOrdenados {
		if _, ok := body.PorEstado[e]; !ok {
			t.Fatalf("falta clave %s", e)
		}
	}
	if body.Total != 24 {
		t.Fatalf("total=%d", body.Total)
	}
}

func TestCitasResumenCeros(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakeStatsStore{counts: map[string]int{}}
	r := setupOperacionEnv(t, srv.URL, &fakeAgendaStore{}, store, recepOperacionUser())
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")

	code, w := doGet(t, r, "/citas/resumen?desde=2026-01-01&hasta=2026-01-01", tok)
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	var body struct {
		PorEstado map[string]int `json:"porEstado"`
		Total     int            `json:"total"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if len(body.PorEstado) != 5 || body.Total != 0 {
		t.Fatalf("body=%+v", body)
	}
}

func TestCitasResumenProfesionalSoloPropio(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	store := &fakeStatsStore{
		byUsuario: map[string]string{"u-prof": "p-mio"},
		owners:    map[string]string{"p-mio": "u-prof"},
		counts:    map[string]int{"reservada": 3},
	}
	r := setupOperacionEnv(t, srv.URL, &fakeAgendaStore{}, store, profOperacionUser("u-prof"))
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleProfesional}, testKid, "clinica-norte")

	code, _ := doGet(t, r, "/citas/resumen?desde=2026-11-01&hasta=2026-11-30", tok)
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	if store.lastProf == nil || *store.lastProf != "p-mio" {
		t.Fatalf("filtro=%v", store.lastProf)
	}
}

func TestDiaEnZonaCostaRica(t *testing.T) {
	fecha, _ := parseFechaCalendario("2026-11-12")
	inicio, fin, err := diaEnZona(fecha, "America/Costa_Rica")
	if err != nil {
		t.Fatal(err)
	}
	if !inicio.UTC().Equal(time.Date(2026, 11, 12, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("inicio=%v", inicio.UTC())
	}
	if !fin.UTC().Equal(time.Date(2026, 11, 13, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("fin=%v", fin.UTC())
	}
}
