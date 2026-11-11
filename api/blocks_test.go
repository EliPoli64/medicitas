package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

type fakeBlockStore struct {
	byProf       map[string][]Block
	bloqueoOwner map[string]string // bloqueoID -> profesionalID
	conflicts    []BlockConflict
	lastClinica  string
	createErr    error
}

func (f *fakeBlockStore) ensure() {
	if f.byProf == nil {
		f.byProf = map[string][]Block{}
	}
	if f.bloqueoOwner == nil {
		f.bloqueoOwner = map[string]string{}
	}
}

func (f *fakeBlockStore) ListBloqueos(_ context.Context, clinicaIdent, profesionalID string, _, _ *time.Time) ([]Block, error) {
	f.ensure()
	f.lastClinica = clinicaIdent
	out := f.byProf[profesionalID]
	if out == nil {
		return []Block{}, nil
	}
	return out, nil
}

func (f *fakeBlockStore) CreateBloqueo(_ context.Context, clinicaIdent, profesionalID, creadoPor string, inicio, fin time.Time, motivo string) (Block, []BlockConflict, error) {
	f.ensure()
	f.lastClinica = clinicaIdent
	if f.createErr != nil {
		return Block{}, nil, f.createErr
	}
	b := Block{
		ID: "b-new", ProfesionalID: profesionalID,
		Inicio: inicio.UTC().Format(time.RFC3339), Fin: fin.UTC().Format(time.RFC3339),
		Motivo: motivo, CreadoPorUsuarioID: creadoPor, CreadoEn: time.Now().UTC().Format(time.RFC3339),
	}
	f.byProf[profesionalID] = append(f.byProf[profesionalID], b)
	f.bloqueoOwner[b.ID] = profesionalID
	cf := f.conflicts
	if cf == nil {
		cf = []BlockConflict{}
	}
	return b, cf, nil
}

func (f *fakeBlockStore) DeleteBloqueo(_ context.Context, clinicaIdent, bloqueoID string) (string, error) {
	f.ensure()
	f.lastClinica = clinicaIdent
	prof, ok := f.bloqueoOwner[bloqueoID]
	if !ok {
		return "", errBloqueoFueraDeClinica
	}
	delete(f.bloqueoOwner, bloqueoID)
	list := f.byProf[prof]
	kept := list[:0]
	for _, b := range list {
		if b.ID != bloqueoID {
			kept = append(kept, b)
		}
	}
	f.byProf[prof] = kept
	return prof, nil
}

func (f *fakeBlockStore) BloqueoProfesionalID(_ context.Context, _, bloqueoID string) (string, error) {
	f.ensure()
	p, ok := f.bloqueoOwner[bloqueoID]
	if !ok {
		return "", errBloqueoFueraDeClinica
	}
	return p, nil
}

func TestCreateBloqueoConConflictos(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	sched := &fakeScheduleStore{
		owners: map[string]string{"p1": "u-prof"}, byProf: map[string][]Schedule{}, horarioOwner: map[string]string{},
	}
	blocks := &fakeBlockStore{
		conflicts: []BlockConflict{
			{CitaID: "c1", Inicio: "2026-11-20T15:00:00Z", Fin: "2026-11-20T15:30:00Z", Estado: "reservada", PacienteID: "pac1"},
		},
	}
	r := setupAgendaTestEnv(t, srv.URL, sched, blocks, admAgendaUser())
	body := `{"inicio":"2026-11-20T14:00:00Z","fin":"2026-11-20T18:00:00Z","motivo":"Vacaciones"}`
	code, w := doJSON(t, r, http.MethodPost, "/profesionales/p1/bloqueos", mintAgenda(t, tk, []string{RoleAdmClinica}), body)
	if code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	var resp struct {
		Bloqueo    Block           `json:"bloqueo"`
		Conflictos []BlockConflict `json:"conflictos"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Bloqueo.Motivo != "Vacaciones" || len(resp.Conflictos) != 1 || resp.Conflictos[0].CitaID != "c1" {
		t.Fatalf("unexpected: %+v", resp)
	}
}

func TestCreateBloqueoI5IgnoresQuery(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	sched := &fakeScheduleStore{owners: map[string]string{"p1": "u"}, byProf: map[string][]Schedule{}, horarioOwner: map[string]string{}}
	blocks := &fakeBlockStore{}
	r := setupAgendaTestEnv(t, srv.URL, sched, blocks, admAgendaUser())
	body := `{"inicio":"2026-11-21T14:00:00Z","fin":"2026-11-21T15:00:00Z","motivo":"Permiso"}`
	code, _ := doJSON(t, r, http.MethodPost, "/profesionales/p1/bloqueos?clinicaId=clinica-sur",
		mintAgenda(t, tk, []string{RoleAdmClinica}), body)
	if code != http.StatusCreated {
		t.Fatalf("status=%d", code)
	}
	if blocks.lastClinica != "clinica-norte" {
		t.Fatalf("I5 fail clinica=%s", blocks.lastClinica)
	}
}

func TestBloqueoProfesionalOtro403(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	sched := &fakeScheduleStore{owners: map[string]string{"p-otro": "u-otro"}, byProf: map[string][]Schedule{}, horarioOwner: map[string]string{}}
	r := setupAgendaTestEnv(t, srv.URL, sched, &fakeBlockStore{}, profAgendaUser("u-mio"))
	body := `{"inicio":"2026-11-21T14:00:00Z","fin":"2026-11-21T15:00:00Z","motivo":"x"}`
	code, _ := doJSON(t, r, http.MethodPost, "/profesionales/p-otro/bloqueos",
		mintAgenda(t, tk, []string{RoleProfesional}), body)
	if code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", code)
	}
}

func TestDeleteBloqueoOK(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	sched := &fakeScheduleStore{owners: map[string]string{"p1": "u-prof"}, byProf: map[string][]Schedule{}, horarioOwner: map[string]string{}}
	blocks := &fakeBlockStore{
		byProf: map[string][]Block{"p1": {{ID: "b1", ProfesionalID: "p1", Motivo: "x"}}},
		bloqueoOwner: map[string]string{"b1": "p1"},
	}
	r := setupAgendaTestEnv(t, srv.URL, sched, blocks, admAgendaUser())
	code, _ := doJSON(t, r, http.MethodDelete, "/bloqueos/b1", mintAgenda(t, tk, []string{RoleAdmClinica}), "")
	if code != http.StatusNoContent {
		t.Fatalf("status=%d want 204", code)
	}
}

func TestCreateBloqueoRangoInvalido400(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	sched := &fakeScheduleStore{owners: map[string]string{"p1": "u"}, byProf: map[string][]Schedule{}, horarioOwner: map[string]string{}}
	r := setupAgendaTestEnv(t, srv.URL, sched, &fakeBlockStore{}, admAgendaUser())
	body := `{"inicio":"2026-11-21T15:00:00Z","fin":"2026-11-21T14:00:00Z","motivo":"x"}`
	code, _ := doJSON(t, r, http.MethodPost, "/profesionales/p1/bloqueos", mintAgenda(t, tk, []string{RoleAdmClinica}), body)
	if code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", code)
	}
}
