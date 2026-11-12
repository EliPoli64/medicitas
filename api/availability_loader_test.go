package main

// Pruebas del adaptador availabilitycache.LoaderFunc.
//
// La prueba principal compara el Snapshot del Loader contra la respuesta real
// del handler RF7 (GET /profesionales/:id/disponibilidad). Así se verifica que
// ambos caminos usan el mismo cálculo sin duplicarlo en el test.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"medicitas/api/internal/availabilitycache"
)

// baseLoaderStore devuelve un store válido para una consulta que llega hasta
// el cálculo. Cada prueba ajusta solo lo que necesita.
func baseLoaderStore() *fakeDispoStore {
	return &fakeDispoStore{
		duracion: 30,
		activo:   true,
		prof:     ProfesionalDispo{ID: "p1", IdpSub: "sub-1", Activo: true},
		hab:      true,
	}
}

func loaderRequest() availabilitycache.Request {
	return availabilitycache.Request{
		ClinicaID:     "clinica-norte",
		ProfesionalID: "p1",
		ServicioID:    "s1",
		Fecha:         "2030-03-11",
		PasoMinutos:   30,
	}
}

// congelarReloj fija nowFn durante la prueba. RF7 y el Loader comparten el
// mismo reloj, así que ambos ven exactamente el mismo "ahora".
func congelarReloj(t *testing.T, ahora time.Time) {
	t.Helper()
	prev := nowFn
	nowFn = func() time.Time { return ahora }
	t.Cleanup(func() { nowFn = prev })
}

func datosLunesDeMarzo(t *testing.T) DatosAgenda {
	t.Helper()
	fecha := fechaLocal(t, 2030, 3, 11, 12, 0) // lunes
	return DatosAgenda{
		Horarios: []HorarioAgenda{{
			DiaSemana:    isoDow(fecha.Weekday()),
			MinutoInicio: 8 * 60,
			MinutoFin:    12 * 60,
			VigenteDesde: "2030-01-01",
		}},
		Citas: []Rango{{
			Inicio: fechaLocal(t, 2030, 3, 11, 9, 0),
			Fin:    fechaLocal(t, 2030, 3, 11, 9, 30),
		}},
	}
}

// TestDisponibilidadLoaderMatchesRF7Handler es la prueba de paridad: pide la
// disponibilidad por HTTP con el handler real y, por separado, ejecuta el
// Loader con el mismo Request. Ambos deben devolver los mismos intervalos.
func TestDisponibilidadLoaderMatchesRF7Handler(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	congelarReloj(t, fechaLocal(t, 2030, 3, 11, 7, 0))

	dispo := baseLoaderStore()
	dispo.datos = map[string]DatosAgenda{"p1": datosLunesDeMarzo(t)}
	r := setupDispoTestEnv(t, srv.URL, dispo, admClinicaUser())

	code, w := doGet(t, r,
		"/profesionales/p1/disponibilidad?fecha=2030-03-11&servicioId=s1&pasoMinutos=15",
		mintAdm(t, tk))
	if code != http.StatusOK {
		t.Fatalf("RF7 status = %d, body = %s", code, w.Body.String())
	}

	var respuesta struct {
		ClinicaIdentificador string    `json:"clinicaIdentificador"`
		ProfesionalID        string    `json:"profesionalId"`
		ServicioID           string    `json:"servicioId"`
		Fecha                string    `json:"fecha"`
		ZonaHoraria          string    `json:"zonaHoraria"`
		DuracionMinutos      int       `json:"duracionMinutos"`
		PasoMinutos          int       `json:"pasoMinutos"`
		Espacios             []Espacio `json:"espacios"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &respuesta); err != nil {
		t.Fatalf("decode RF7 response: %v", err)
	}

	req := loaderRequest()
	req.PasoMinutos = 15
	snap, err := newDisponibilidadLoader(dispo).Load(context.Background(), req)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Metadatos: el Snapshot debe describir exactamente la consulta.
	if snap.SchemaVersion != availabilitycache.SnapshotSchemaVersion {
		t.Fatalf("SchemaVersion = %d", snap.SchemaVersion)
	}
	if snap.ClinicaID != respuesta.ClinicaIdentificador {
		t.Fatalf("ClinicaID = %q, want %q", snap.ClinicaID, respuesta.ClinicaIdentificador)
	}
	if snap.ProfesionalID != respuesta.ProfesionalID {
		t.Fatalf("ProfesionalID = %q, want %q", snap.ProfesionalID, respuesta.ProfesionalID)
	}
	if snap.ServicioID != respuesta.ServicioID {
		t.Fatalf("ServicioID = %q, want %q", snap.ServicioID, respuesta.ServicioID)
	}
	if snap.Fecha != respuesta.Fecha {
		t.Fatalf("Fecha = %q, want %q", snap.Fecha, respuesta.Fecha)
	}
	if snap.ZonaHoraria != respuesta.ZonaHoraria {
		t.Fatalf("ZonaHoraria = %q, want %q", snap.ZonaHoraria, respuesta.ZonaHoraria)
	}
	if snap.DuracionMinutos != respuesta.DuracionMinutos {
		t.Fatalf("DuracionMinutos = %d, want %d", snap.DuracionMinutos, respuesta.DuracionMinutos)
	}
	if snap.PasoMinutos != respuesta.PasoMinutos {
		t.Fatalf("PasoMinutos = %d, want %d", snap.PasoMinutos, respuesta.PasoMinutos)
	}

	// Espacios: mismos instantes y misma hora local reconstruida.
	loc := mustLoc(t)
	if len(snap.Espacios) != len(respuesta.Espacios) {
		t.Fatalf("espacios = %d, want %d", len(snap.Espacios), len(respuesta.Espacios))
	}
	if len(snap.Espacios) == 0 {
		t.Fatal("la prueba no está ejercitando ningún espacio")
	}
	for i, want := range respuesta.Espacios {
		got := snap.Espacios[i]
		if !got.Inicio.Equal(want.Inicio) {
			t.Fatalf("espacio %d Inicio = %s, want %s", i, got.Inicio, want.Inicio)
		}
		if !got.Fin.Equal(want.Fin) {
			t.Fatalf("espacio %d Fin = %s, want %s", i, got.Fin, want.Fin)
		}
		if hora := got.Inicio.In(loc).Format("15:04"); hora != want.HoraLocal {
			t.Fatalf("espacio %d HoraLocal reconstruida = %q, want %q", i, hora, want.HoraLocal)
		}
	}
}

// TestDisponibilidadLoaderUsesRequestedStep comprueba que el Loader respeta el
// paso efectivo del Request y no una rejilla fija.
func TestDisponibilidadLoaderUsesRequestedStep(t *testing.T) {
	congelarReloj(t, fechaLocal(t, 2030, 3, 11, 7, 0))

	dispo := baseLoaderStore()
	dispo.datos = map[string]DatosAgenda{"p1": {
		Horarios: []HorarioAgenda{{
			DiaSemana:    isoDow(fechaLocal(t, 2030, 3, 11, 12, 0).Weekday()),
			MinutoInicio: 8 * 60,
			MinutoFin:    12 * 60,
			VigenteDesde: "2030-01-01",
		}},
	}}
	loader := newDisponibilidadLoader(dispo)

	req15 := loaderRequest()
	req15.PasoMinutos = 15
	snap15, err := loader.Load(context.Background(), req15)
	if err != nil {
		t.Fatalf("Load(step 15) error = %v", err)
	}

	req30 := loaderRequest()
	req30.PasoMinutos = 30
	snap30, err := loader.Load(context.Background(), req30)
	if err != nil {
		t.Fatalf("Load(step 30) error = %v", err)
	}

	// 08:00–12:00 con duración 30: 15 inicios cada 15 min, 8 cada 30 min.
	if len(snap15.Espacios) != 15 {
		t.Fatalf("step 15: %d espacios, want 15", len(snap15.Espacios))
	}
	if len(snap30.Espacios) != 8 {
		t.Fatalf("step 30: %d espacios, want 8", len(snap30.Espacios))
	}
	if snap15.PasoMinutos != 15 || snap30.PasoMinutos != 30 {
		t.Fatalf("PasoMinutos = (%d, %d), want (15, 30)", snap15.PasoMinutos, snap30.PasoMinutos)
	}

	loc := mustLoc(t)
	if got := snap15.Espacios[1].Inicio.In(loc).Format("15:04"); got != "08:15" {
		t.Fatalf("step 15 segundo inicio = %q, want 08:15", got)
	}
	if got := snap30.Espacios[1].Inicio.In(loc).Format("15:04"); got != "08:30" {
		t.Fatalf("step 30 segundo inicio = %q, want 08:30", got)
	}
}

// TestDisponibilidadLoaderSnapshotMetadata verifica que el Snapshot quede
// autocontenido: incluye la zona, la duración, el paso y el instante UTC de
// generación, y usa una lista vacía (no nil) cuando no hay espacios.
func TestDisponibilidadLoaderSnapshotMetadata(t *testing.T) {
	ahora := fechaLocal(t, 2030, 3, 11, 7, 0)
	congelarReloj(t, ahora)

	dispo := baseLoaderStore()
	// Horario para otro día: la fecha consultada no tiene espacios.
	dispo.datos = map[string]DatosAgenda{"p1": {
		Horarios: []HorarioAgenda{{
			DiaSemana:    isoDow(fechaLocal(t, 2030, 3, 12, 12, 0).Weekday()),
			MinutoInicio: 8 * 60,
			MinutoFin:    12 * 60,
			VigenteDesde: "2030-01-01",
		}},
	}}

	req := loaderRequest()
	req.PasoMinutos = 20
	snap, err := newDisponibilidadLoader(dispo).Load(context.Background(), req)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if snap.SchemaVersion != availabilitycache.SnapshotSchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", snap.SchemaVersion, availabilitycache.SnapshotSchemaVersion)
	}
	if snap.ClinicaID != req.ClinicaID || snap.ProfesionalID != req.ProfesionalID ||
		snap.ServicioID != req.ServicioID || snap.Fecha != req.Fecha {
		t.Fatalf("identificadores del Snapshot no coinciden con el Request: %+v", snap)
	}
	if snap.ZonaHoraria != "America/Costa_Rica" {
		t.Fatalf("ZonaHoraria = %q", snap.ZonaHoraria)
	}
	if snap.DuracionMinutos != 30 || snap.PasoMinutos != 20 {
		t.Fatalf("duración/paso = %d/%d, want 30/20", snap.DuracionMinutos, snap.PasoMinutos)
	}
	if !snap.GeneradoEn.Equal(ahora.UTC()) {
		t.Fatalf("GeneradoEn = %s, want %s", snap.GeneradoEn, ahora.UTC())
	}
	if snap.Espacios == nil {
		t.Fatal("Espacios es nil; debe ser una lista vacía para serializar []")
	}
	if len(snap.Espacios) != 0 {
		t.Fatalf("Espacios = %d, want 0", len(snap.Espacios))
	}
}

// capturaDatosStore registra los argumentos con los que el Loader consulta la
// agenda, sin modificar el fake compartido de availability_test.go.
type capturaDatosStore struct {
	*fakeDispoStore
	desde, hasta         time.Time
	clinica, profesional string
}

func (c *capturaDatosStore) DatosAgenda(ctx context.Context, clinica, profesional string, desde, hasta time.Time) (DatosAgenda, error) {
	c.desde, c.hasta = desde, hasta
	c.clinica, c.profesional = clinica, profesional
	return c.fakeDispoStore.DatosAgenda(ctx, clinica, profesional, desde, hasta)
}

// TestDisponibilidadLoaderQueriesLocalDay comprueba que la agenda se pida por
// la medianoche local de la clínica y que la clínica del Request sea la que
// llega al store (I5), sin ventanas en UTC que puedan correr la fecha.
func TestDisponibilidadLoaderQueriesLocalDay(t *testing.T) {
	congelarReloj(t, fechaLocal(t, 2030, 3, 11, 7, 0))

	store := &capturaDatosStore{fakeDispoStore: baseLoaderStore()}
	store.datos = map[string]DatosAgenda{"p1": datosLunesDeMarzo(t)}

	req := loaderRequest()
	if _, err := newDisponibilidadLoader(store).Load(context.Background(), req); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	loc := mustLoc(t)
	wantDesde := time.Date(2030, 3, 11, 0, 0, 0, 0, loc)
	wantHasta := time.Date(2030, 3, 12, 0, 0, 0, 0, loc)
	if !store.desde.Equal(wantDesde) {
		t.Fatalf("DatosAgenda desde = %s, want %s", store.desde, wantDesde)
	}
	if !store.hasta.Equal(wantHasta) {
		t.Fatalf("DatosAgenda hasta = %s, want %s", store.hasta, wantHasta)
	}
	if store.hasta.Sub(store.desde) != 24*time.Hour {
		t.Fatalf("ventana = %v, want 24h", store.hasta.Sub(store.desde))
	}
	if store.clinica != req.ClinicaID || store.profesional != req.ProfesionalID {
		t.Fatalf("DatosAgenda recibió (%q, %q), want (%q, %q)",
			store.clinica, store.profesional, req.ClinicaID, req.ProfesionalID)
	}
}

func TestDisponibilidadLoaderErrors(t *testing.T) {
	boom := errors.New("postgres caído")

	tests := []struct {
		name     string
		mutate   func(*fakeDispoStore)
		reqMut   func(*availabilitycache.Request)
		wantErr  error
		wantText string
	}{
		{
			name:    "servicio de otra clínica",
			mutate:  func(s *fakeDispoStore) { s.svcErr = errServicioDispoFuera },
			wantErr: errServicioDispoFuera,
		},
		{
			name:    "error al consultar el servicio",
			mutate:  func(s *fakeDispoStore) { s.svcErr = boom },
			wantErr: boom,
		},
		{
			name:    "servicio inactivo",
			mutate:  func(s *fakeDispoStore) { s.activo = false },
			wantErr: errServicioDispoInactivo,
		},
		{
			name:     "duración inválida",
			mutate:   func(s *fakeDispoStore) { s.duracion = 0 },
			wantText: "duración de servicio inválida",
		},
		{
			name:    "profesional de otra clínica",
			mutate:  func(s *fakeDispoStore) { s.profErr = errProfesionalDispoFuera },
			wantErr: errProfesionalDispoFuera,
		},
		{
			name:    "profesional inactivo",
			mutate:  func(s *fakeDispoStore) { s.prof.Activo = false },
			wantErr: errProfesionalDispoInactivo,
		},
		{
			name:    "profesional sin el servicio",
			mutate:  func(s *fakeDispoStore) { s.hab = false },
			wantErr: errProfesionalSinServicio,
		},
		{
			name:    "error al verificar habilitación",
			mutate:  func(s *fakeDispoStore) { s.habErr = boom },
			wantErr: boom,
		},
		{
			name:    "error al cargar la agenda",
			mutate:  func(s *fakeDispoStore) { s.datosErr = boom },
			wantErr: boom,
		},
		{
			name:    "error al resolver la zona",
			mutate:  func(s *fakeDispoStore) { s.zonaErr = boom },
			wantErr: boom,
		},
		{
			name:     "zona horaria inválida",
			mutate:   func(s *fakeDispoStore) { s.zona = "No/Existe" },
			wantText: "zona horaria inválida",
		},
		{
			name:    "fecha inválida",
			reqMut:  func(r *availabilitycache.Request) { r.Fecha = "11-03-2030" },
			wantErr: errFechaDispoInvalida,
		},
		{
			name:    "paso no positivo",
			reqMut:  func(r *availabilitycache.Request) { r.PasoMinutos = 0 },
			wantErr: availabilitycache.ErrInvalidRequest,
		},
		{
			name:    "clínica vacía",
			reqMut:  func(r *availabilitycache.Request) { r.ClinicaID = "" },
			wantErr: availabilitycache.ErrInvalidRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := baseLoaderStore()
			if tt.mutate != nil {
				tt.mutate(store)
			}
			req := loaderRequest()
			if tt.reqMut != nil {
				tt.reqMut(&req)
			}

			_, err := newDisponibilidadLoader(store).Load(context.Background(), req)
			if err == nil {
				t.Fatal("Load() error = nil")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("Load() error = %v, want errors.Is(%v)", err, tt.wantErr)
			}
			if tt.wantText != "" && !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("Load() error = %v, want it to contain %q", err, tt.wantText)
			}
		})
	}
}

func TestDisponibilidadLoaderRequiresStore(t *testing.T) {
	req := loaderRequest()
	if _, err := newDisponibilidadLoader(nil).Load(context.Background(), req); err == nil {
		t.Fatal("Load() con store nil devolvió nil")
	}

	var loader *disponibilidadLoader
	if _, err := loader.Load(context.Background(), req); err == nil {
		t.Fatal("Load() con loader nil devolvió nil")
	}
}

// loaderMemStore es un Store mínimo en memoria para probar el contrato de la
// caché sin Redis. No se usa en producción.
type loaderMemStore struct {
	mu     sync.Mutex
	values map[string]string
}

func (m *loaderMemStore) Get(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.values[key]
	if !ok {
		return "", availabilitycache.ErrMiss
	}
	return v, nil
}

func (m *loaderMemStore) Set(_ context.Context, key, value string, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[key] = value
	return nil
}

func (m *loaderMemStore) SetNX(_ context.Context, key, value string, _ time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.values[key]; ok {
		return false, nil
	}
	m.values[key] = value
	return true, nil
}

// TestDisponibilidadLoaderIsAcceptedByCache conecta el Loader con la caché
// real (con un Store en memoria). Verifica que el Snapshot pase por
// normalizeSnapshot, validateSnapshot y la serialización JSON, y que la
// segunda consulta sea un HIT con los mismos intervalos.
func TestDisponibilidadLoaderIsAcceptedByCache(t *testing.T) {
	congelarReloj(t, fechaLocal(t, 2030, 3, 11, 7, 0))

	dispo := baseLoaderStore()
	dispo.datos = map[string]DatosAgenda{"p1": datosLunesDeMarzo(t)}
	loader := newDisponibilidadLoader(dispo)

	store := &loaderMemStore{values: make(map[string]string)}
	cache, err := availabilitycache.NewCache(store, availabilitycache.DefaultConfig())
	if err != nil {
		t.Fatalf("NewCache() error = %v", err)
	}

	req := loaderRequest()
	req.PasoMinutos = 15

	first, err := cache.GetOrLoad(context.Background(), req, loader.Load)
	if err != nil {
		t.Fatalf("GetOrLoad(first) error = %v", err)
	}
	if first.Hit {
		t.Fatal("la primera consulta no puede ser un HIT")
	}
	if first.CacheErr != nil {
		t.Fatalf("CacheErr = %v", first.CacheErr)
	}

	second, err := cache.GetOrLoad(context.Background(), req, loader.Load)
	if err != nil {
		t.Fatalf("GetOrLoad(second) error = %v", err)
	}
	if !second.Hit {
		t.Fatal("la segunda consulta debía ser un HIT")
	}
	if len(second.Snapshot.Espacios) != len(first.Snapshot.Espacios) {
		t.Fatalf("HIT devolvió %d espacios, MISS %d", len(second.Snapshot.Espacios), len(first.Snapshot.Espacios))
	}
	for i := range first.Snapshot.Espacios {
		if !second.Snapshot.Espacios[i].Inicio.Equal(first.Snapshot.Espacios[i].Inicio) ||
			!second.Snapshot.Espacios[i].Fin.Equal(first.Snapshot.Espacios[i].Fin) {
			t.Fatalf("espacio %d cambió entre MISS y HIT", i)
		}
	}
	// La caché normaliza los instantes a UTC antes de serializarlos.
	if got := second.Snapshot.Espacios[0].Inicio.Location().String(); got != "UTC" {
		t.Fatalf("Location() = %q, want UTC", got)
	}
}
