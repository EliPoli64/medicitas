package main

// Tests unitarios de RF7/RF8: cálculo puro de espacios y handlers con store
// falso. Sin Docker. El reloj se inyecta con nowFn para hacerlos deterministas.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"medicitas/api/internal/availabilitycache"

	"github.com/gin-gonic/gin"
)

// --- Helpers de tiempo ---

func mustLoc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Costa_Rica")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// fechaLocal construye un instante local (Costa Rica, UTC-6).
func fechaLocal(t *testing.T, y int, m time.Month, d, h, min int) time.Time {
	t.Helper()
	return time.Date(y, m, d, h, min, 0, 0, mustLoc(t))
}

// isoDow convierte time.Weekday (domingo=0) al ISO (lunes=1 … domingo=7).
func isoDow(w time.Weekday) int {
	if w == 0 {
		return 7
	}
	return int(w)
}

func horasDe(esp []Espacio) []string {
	out := make([]string, 0, len(esp))
	for _, e := range esp {
		out = append(out, e.HoraLocal)
	}
	return out
}

func contiene(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// --- Cálculo puro (RF7) ---

func TestEspaciosDiaVacio(t *testing.T) {
	loc := mustLoc(t)
	fecha := fechaLocal(t, 2030, 3, 11, 12, 0) // lunes
	datos := DatosAgenda{Horarios: []HorarioAgenda{{
		DiaSemana: isoDow(fecha.Weekday()), MinutoInicio: 8 * 60, MinutoFin: 12 * 60,
		VigenteDesde: "2030-01-01",
	}}}
	esp := espaciosDelDia(datos, fecha, loc, 30, 30, fechaLocal(t, 2030, 3, 4, 0, 0))
	want := []string{"08:00", "08:30", "09:00", "09:30", "10:00", "10:30", "11:00", "11:30"}
	if len(esp) != len(want) {
		t.Fatalf("espacios = %v, want %v", horasDe(esp), want)
	}
	for i := range want {
		if esp[i].HoraLocal != want[i] {
			t.Fatalf("espacios = %v, want %v", horasDe(esp), want)
		}
	}
	// Duración exacta del servicio (I4): 30 minutos.
	if esp[0].Fin.Sub(esp[0].Inicio) != 30*time.Minute {
		t.Fatalf("duración = %v", esp[0].Fin.Sub(esp[0].Inicio))
	}
}

func TestEspaciosCitaOcupa(t *testing.T) {
	loc := mustLoc(t)
	fecha := fechaLocal(t, 2030, 3, 11, 12, 0)
	datos := DatosAgenda{
		Horarios: []HorarioAgenda{{
			DiaSemana: isoDow(fecha.Weekday()), MinutoInicio: 8 * 60, MinutoFin: 12 * 60,
			VigenteDesde: "2030-01-01",
		}},
		Citas: []Rango{{ // cita de 30 min a las 08:30
			Inicio: fechaLocal(t, 2030, 3, 11, 8, 30),
			Fin:    fechaLocal(t, 2030, 3, 11, 9, 0),
		}},
	}
	esp := horasDe(espaciosDelDia(datos, fecha, loc, 30, 30, fechaLocal(t, 2030, 3, 4, 0, 0)))
	if contiene(esp, "08:30") {
		t.Fatalf("el espacio ocupado aparece: %v", esp)
	}
	if !contiene(esp, "08:00") || !contiene(esp, "09:00") || len(esp) != 7 {
		t.Fatalf("espacios = %v (esperaba 7, sin 08:30)", esp)
	}
}

func TestEspaciosBloqueoParcial(t *testing.T) {
	loc := mustLoc(t)
	fecha := fechaLocal(t, 2030, 3, 11, 12, 0)
	datos := DatosAgenda{
		Horarios: []HorarioAgenda{{
			DiaSemana: isoDow(fecha.Weekday()), MinutoInicio: 8 * 60, MinutoFin: 12 * 60,
			VigenteDesde: "2030-01-01",
		}},
		Bloqueos: []Rango{{
			Inicio: fechaLocal(t, 2030, 3, 11, 8, 0),
			Fin:    fechaLocal(t, 2030, 3, 11, 10, 0),
		}},
	}
	esp := horasDe(espaciosDelDia(datos, fecha, loc, 30, 30, fechaLocal(t, 2030, 3, 4, 0, 0)))
	want := []string{"10:00", "10:30", "11:00", "11:30"}
	if len(esp) != len(want) {
		t.Fatalf("espacios = %v, want %v", esp, want)
	}
}

func TestEspaciosBloqueoMultidia(t *testing.T) {
	loc := mustLoc(t)
	fecha := fechaLocal(t, 2030, 3, 12, 12, 0) // martes
	datos := DatosAgenda{
		Horarios: []HorarioAgenda{{
			DiaSemana: isoDow(fecha.Weekday()), MinutoInicio: 8 * 60, MinutoFin: 12 * 60,
			VigenteDesde: "2030-01-01",
		}},
		// Vacaciones que cubren el día entero (multidía: RF6).
		Bloqueos: []Rango{{
			Inicio: fechaLocal(t, 2030, 3, 11, 8, 0),
			Fin:    fechaLocal(t, 2030, 3, 13, 18, 0),
		}},
	}
	esp := espaciosDelDia(datos, fecha, loc, 30, 30, fechaLocal(t, 2030, 3, 4, 0, 0))
	if len(esp) != 0 {
		t.Fatalf("espacios = %v, want ninguno", horasDe(esp))
	}
}

func TestEspaciosVariosBloquesPorDia(t *testing.T) {
	loc := mustLoc(t)
	fecha := fechaLocal(t, 2030, 3, 11, 12, 0)
	dow := isoDow(fecha.Weekday())
	datos := DatosAgenda{Horarios: []HorarioAgenda{
		{DiaSemana: dow, MinutoInicio: 8 * 60, MinutoFin: 12 * 60, VigenteDesde: "2030-01-01"},
		{DiaSemana: dow, MinutoInicio: 13 * 60, MinutoFin: 17 * 60, VigenteDesde: "2030-01-01"},
	}}
	esp := horasDe(espaciosDelDia(datos, fecha, loc, 30, 30, fechaLocal(t, 2030, 3, 4, 0, 0)))
	if contiene(esp, "12:00") || contiene(esp, "12:30") {
		t.Fatalf("hay espacios en la jornada partida: %v", esp)
	}
	if len(esp) != 16 || !contiene(esp, "13:00") || !contiene(esp, "16:30") {
		t.Fatalf("espacios = %v (esperaba 16 en dos bloques)", esp)
	}
}

func TestEspaciosDuracionNoCabeEnHueco(t *testing.T) {
	loc := mustLoc(t)
	fecha := fechaLocal(t, 2030, 3, 11, 12, 0)
	datos := DatosAgenda{
		Horarios: []HorarioAgenda{{
			DiaSemana: isoDow(fecha.Weekday()), MinutoInicio: 8 * 60, MinutoFin: 10 * 60,
			VigenteDesde: "2030-01-01",
		}},
		// Huecos de 30 min: [08:00,08:30) y [09:30,10:00).
		Citas: []Rango{{Inicio: fechaLocal(t, 2030, 3, 11, 8, 30), Fin: fechaLocal(t, 2030, 3, 11, 9, 30)}},
	}
	// Servicio de 45 min: no cabe en ningún hueco.
	esp := espaciosDelDia(datos, fecha, loc, 45, 30, fechaLocal(t, 2030, 3, 4, 0, 0))
	if len(esp) != 0 {
		t.Fatalf("espacios = %v, want ninguno con duración 45", horasDe(esp))
	}
	// Servicio de 30 min: caben exactamente los dos huecos.
	esp = espaciosDelDia(datos, fecha, loc, 30, 30, fechaLocal(t, 2030, 3, 4, 0, 0))
	if len(esp) != 2 || esp[0].HoraLocal != "08:00" || esp[1].HoraLocal != "09:30" {
		t.Fatalf("espacios = %v, want [08:00 09:30]", horasDe(esp))
	}
}

func TestEspaciosAlineacionRejilla(t *testing.T) {
	loc := mustLoc(t)
	fecha := fechaLocal(t, 2030, 3, 11, 12, 0)
	datos := DatosAgenda{
		Horarios: []HorarioAgenda{{
			DiaSemana: isoDow(fecha.Weekday()), MinutoInicio: 8*60 + 47, MinutoFin: 12 * 60,
			VigenteDesde: "2030-01-01",
		}},
	}
	esp := horasDe(espaciosDelDia(datos, fecha, loc, 30, 30, fechaLocal(t, 2030, 3, 4, 0, 0)))
	// La rejilla es múltiplo de 30 min desde la medianoche: 08:47 → 09:00.
	if esp[0] != "09:00" {
		t.Fatalf("primer espacio = %v, want 09:00", esp[0])
	}
}

func TestEspaciosFiltraPasados(t *testing.T) {
	loc := mustLoc(t)
	fecha := fechaLocal(t, 2030, 3, 11, 12, 0)
	datos := DatosAgenda{Horarios: []HorarioAgenda{{
		DiaSemana: isoDow(fecha.Weekday()), MinutoInicio: 8 * 60, MinutoFin: 12 * 60,
		VigenteDesde: "2030-01-01",
	}}}
	// Ahora = 09:00 en punto: el espacio de las 09:00 ya está comenzando.
	esp := horasDe(espaciosDelDia(datos, fecha, loc, 30, 30, fechaLocal(t, 2030, 3, 11, 9, 0)))
	if contiene(esp, "08:00") || contiene(esp, "08:30") || contiene(esp, "09:00") {
		t.Fatalf("espacios pasados presentes: %v", esp)
	}
	if esp[0] != "09:30" {
		t.Fatalf("primer espacio = %v, want 09:30", esp[0])
	}
}

func TestEspaciosHorarioNoVigenteODiaDistinto(t *testing.T) {
	loc := mustLoc(t)
	fecha := fechaLocal(t, 2030, 3, 11, 12, 0) // lunes
	ahora := fechaLocal(t, 2030, 3, 4, 0, 0)
	// Vigencia cerrada antes de la fecha.
	datos := DatosAgenda{Horarios: []HorarioAgenda{{
		DiaSemana: isoDow(fecha.Weekday()), MinutoInicio: 8 * 60, MinutoFin: 12 * 60,
		VigenteDesde: "2030-01-01", VigenteHasta: "2030-02-01",
	}}}
	if n := len(espaciosDelDia(datos, fecha, loc, 30, 30, ahora)); n != 0 {
		t.Fatalf("horario no vigente dio %d espacios", n)
	}
	// Día de la semana distinto.
	datos = DatosAgenda{Horarios: []HorarioAgenda{{
		DiaSemana: isoDow(fecha.Weekday())%7 + 1, MinutoInicio: 8 * 60, MinutoFin: 12 * 60,
		VigenteDesde: "2030-01-01",
	}}}
	if n := len(espaciosDelDia(datos, fecha, loc, 30, 30, ahora)); n != 0 {
		t.Fatalf("día de semana ajeno dio %d espacios", n)
	}
}

func TestEspaciosCitasSolapadasSeFusionan(t *testing.T) {
	loc := mustLoc(t)
	fecha := fechaLocal(t, 2030, 3, 11, 12, 0)
	datos := DatosAgenda{
		Horarios: []HorarioAgenda{{
			DiaSemana: isoDow(fecha.Weekday()), MinutoInicio: 8 * 60, MinutoFin: 12 * 60,
			VigenteDesde: "2030-01-01",
		}},
		Citas: []Rango{
			{Inicio: fechaLocal(t, 2030, 3, 11, 8, 30), Fin: fechaLocal(t, 2030, 3, 11, 9, 15)},
			{Inicio: fechaLocal(t, 2030, 3, 11, 9, 0), Fin: fechaLocal(t, 2030, 3, 11, 9, 45)},
		},
	}
	esp := horasDe(espaciosDelDia(datos, fecha, loc, 30, 30, fechaLocal(t, 2030, 3, 4, 0, 0)))
	// Hueco [08:00,08:30) → 08:00; hueco [09:45,12:00) → rejilla desde 10:00.
	want := []string{"08:00", "10:00", "10:30", "11:00", "11:30"}
	if len(esp) != len(want) {
		t.Fatalf("espacios = %v, want %v", esp, want)
	}
	for i := range want {
		if esp[i] != want[i] {
			t.Fatalf("espacios = %v, want %v", esp, want)
		}
	}
}

// --- Store falso ---

type fakeDispoStore struct {
	zona       string
	zonaErr    error
	duracion   int
	activo     bool
	svcErr     error
	prof       ProfesionalDispo
	profErr    error
	hab        bool
	habErr     error
	profs      []ProfesionalDispo
	profsErr   error
	datos      map[string]DatosAgenda
	datosErr   error
	gotClinica string
}

func (f *fakeDispoStore) ZonaHoraria(_ context.Context, _ string) (string, error) {
	if f.zonaErr != nil {
		return "", f.zonaErr
	}
	if f.zona == "" {
		return "America/Costa_Rica", nil
	}
	return f.zona, nil
}

func (f *fakeDispoStore) ServicioDispo(_ context.Context, _, _ string) (int, bool, error) {
	if f.svcErr != nil {
		return 0, false, f.svcErr
	}
	return f.duracion, f.activo, nil
}

func (f *fakeDispoStore) ProfesionalDispo(_ context.Context, _, _ string) (ProfesionalDispo, error) {
	if f.profErr != nil {
		return ProfesionalDispo{}, f.profErr
	}
	return f.prof, nil
}

func (f *fakeDispoStore) ProfesionalHabilitado(_ context.Context, _, _, _ string) (bool, error) {
	return f.hab, f.habErr
}

func (f *fakeDispoStore) ProfesionalesDelServicio(_ context.Context, clinicaIdent, _ string) ([]ProfesionalDispo, error) {
	f.gotClinica = clinicaIdent
	if f.profsErr != nil {
		return nil, f.profsErr
	}
	return f.profs, nil
}

func (f *fakeDispoStore) DatosAgenda(_ context.Context, clinicaIdent, profesionalID string, _, _ time.Time) (DatosAgenda, error) {
	f.gotClinica = clinicaIdent
	if f.datosErr != nil {
		return DatosAgenda{}, f.datosErr
	}
	if f.datos != nil {
		return f.datos[profesionalID], nil
	}
	return DatosAgenda{}, nil
}

// setupDispoTestEnv arma el router con el store falso de disponibilidad.
func setupDispoTestEnv(t *testing.T, srvURL string, dispo DisponibilidadStore, users map[string]DomainUser) *gin.Engine {
	t.Helper()
	r := setupCatalogoTestEnv(t, srvURL, nil, nil, nil, &fakeIdentityAdmin{}, users)
	prevStore := disponibilidadStore
	prevLoader := loaderDisponibilidad
	prevCache := availabilityCache
	disponibilidadStore = dispo
	loaderDisponibilidad = newDisponibilidadLoader(dispo)
	availabilityCache = nil // las pruebas unitarias de RF7 no usan Redis
	t.Cleanup(func() {
		disponibilidadStore = prevStore
		loaderDisponibilidad = prevLoader
		availabilityCache = prevCache
	})
	return r
}

// --- Handlers RF7 ---

func TestDisponibilidadProfesionalOK(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	// Reloj congelado: lunes 2030-03-11 07:00 local.
	prevNow := nowFn
	nowFn = func() time.Time { return fechaLocal(t, 2030, 3, 11, 7, 0) }
	t.Cleanup(func() { nowFn = prevNow })

	fecha := fechaLocal(t, 2030, 3, 11, 12, 0)
	dispo := &fakeDispoStore{
		duracion: 30,
		activo:   true,
		prof:     ProfesionalDispo{ID: "p1", IdpSub: "sub-1", Especialidad: "Cardiología", Activo: true},
		hab:      true,
		datos: map[string]DatosAgenda{
			"p1": {Horarios: []HorarioAgenda{{
				DiaSemana: isoDow(fecha.Weekday()), MinutoInicio: 8 * 60, MinutoFin: 12 * 60,
				VigenteDesde: "2030-01-01",
			}}},
		},
	}
	r := setupDispoTestEnv(t, srv.URL, dispo, admClinicaUser())

	code, w := doGet(t, r,
		"/profesionales/p1/disponibilidad?fecha=2030-03-11&servicioId=s1&clinicaId=clinica-sur",
		mintAdm(t, tk))
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, "08:00:00-06:00") || !contains(body, "11:30:00-06:00") {
		t.Fatalf("espacios ausentes: %s", body)
	}
	// I5: la clínica viene del token, ?clinicaId= se ignora.
	if dispo.gotClinica != "clinica-norte" {
		t.Fatalf("I5: clínica = %s", dispo.gotClinica)
	}
	if !contains(body, `"duracionMinutos":30`) || !contains(body, `"zonaHoraria":"America/Costa_Rica"`) {
		t.Fatalf("metadatos ausentes: %s", body)
	}
}

// countingLoaderEnvuelve un LoaderFunc contando invocaciones.
type countingLoaderEnvuelve struct {
	inner availabilitycache.LoaderFunc
	calls int
}

func (c *countingLoaderEnvuelve) Load(ctx context.Context, req availabilitycache.Request) (availabilitycache.Snapshot, error) {
	c.calls++
	return c.inner(ctx, req)
}

func TestDisponibilidadProfesionalUsaCache(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	prevNow := nowFn
	nowFn = func() time.Time { return fechaLocal(t, 2030, 3, 11, 7, 0) }
	t.Cleanup(func() { nowFn = prevNow })

	dispo := &fakeDispoStore{
		duracion: 30,
		activo:   true,
		prof:     ProfesionalDispo{ID: "p1", IdpSub: "sub-1", Especialidad: "Cardiología", Activo: true},
		hab:      true,
		datos: map[string]DatosAgenda{
			"p1": {Horarios: []HorarioAgenda{{
				DiaSemana: isoDow(time.Monday), MinutoInicio: 8 * 60, MinutoFin: 12 * 60,
				VigenteDesde: "2030-01-01",
			}}},
		},
	}
	r := setupDispoTestEnv(t, srv.URL, dispo, admClinicaUser())

	// Cache real respaldada por un store en memoria; el resolver no-op permite
	// que los HITs se sirvan sin PostgreSQL.
	store := newRecordingCacheStore()
	cache, err := availabilitycache.NewCache(
		store,
		availabilitycache.DefaultConfig(),
		availabilitycache.WithVersionResolver(nopAgendaVersionResolver{}),
	)
	if err != nil {
		t.Fatalf("NewCache() error = %v", err)
	}

	prevCache := availabilityCache
	availabilityCache = cache
	prevLoader := loaderDisponibilidad
	counter := &countingLoaderEnvuelve{inner: newDisponibilidadLoader(dispo).Load}
	loaderDisponibilidad = counter
	t.Cleanup(func() {
		availabilityCache = prevCache
		loaderDisponibilidad = prevLoader
	})

	query := "/profesionales/p1/disponibilidad?fecha=2030-03-11&servicioId=s1"

	// Primera consulta: miss, invoca al loader.
	if code, _ := doGet(t, r, query, mintAdm(t, tk)); code != http.StatusOK {
		t.Fatalf("first status=%d, want 200", code)
	}
	if counter.calls != 1 {
		t.Fatalf("loader calls after first = %d, want 1", counter.calls)
	}

	// Segunda consulta: hit, no vuelve a invocar al loader.
	if code, _ := doGet(t, r, query, mintAdm(t, tk)); code != http.StatusOK {
		t.Fatalf("second status=%d, want 200", code)
	}
	if counter.calls != 1 {
		t.Fatalf("loader calls after second = %d, want 1", counter.calls)
	}
}

func TestDisponibilidadParametrosInvalidos400(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	dispo := &fakeDispoStore{duracion: 30, activo: true, hab: true}
	r := setupDispoTestEnv(t, srv.URL, dispo, admClinicaUser())

	for nombre, query := range map[string]string{
		"sin servicioId":   "/profesionales/p1/disponibilidad?fecha=2030-03-11",
		"sin fecha":        "/profesionales/p1/disponibilidad?servicioId=s1",
		"fecha inválida":   "/profesionales/p1/disponibilidad?fecha=11-03-2030&servicioId=s1",
		"paso inválido":    "/profesionales/p1/disponibilidad?fecha=2030-03-11&servicioId=s1&pasoMinutos=3",
		"paso no numérico": "/profesionales/p1/disponibilidad?fecha=2030-03-11&servicioId=s1&pasoMinutos=abc",
	} {
		t.Run(nombre, func(t *testing.T) {
			code, _ := doGet(t, r, query, mintAdm(t, tk))
			if code != http.StatusBadRequest {
				t.Fatalf("status=%d", code)
			}
		})
	}
}

func TestDisponibilidadServicioFuera404Inactivo400(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	dispo := &fakeDispoStore{duracion: 30, svcErr: errServicioDispoFuera}
	r := setupDispoTestEnv(t, srv.URL, dispo, admClinicaUser())
	code, _ := doGet(t, r, "/profesionales/p1/disponibilidad?fecha=2030-03-11&servicioId=ajeno", mintAdm(t, tk))
	if code != http.StatusNotFound {
		t.Fatalf("servicio ajeno = %d, want 404", code)
	}

	dispo = &fakeDispoStore{duracion: 30, activo: false}
	r = setupDispoTestEnv(t, srv.URL, dispo, admClinicaUser())
	code, _ = doGet(t, r, "/profesionales/p1/disponibilidad?fecha=2030-03-11&servicioId=s1", mintAdm(t, tk))
	if code != http.StatusBadRequest {
		t.Fatalf("servicio inactivo = %d, want 400", code)
	}
}

func TestDisponibilidadProfesionalEstados(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()
	query := "/profesionales/p1/disponibilidad?fecha=2030-03-11&servicioId=s1"

	dispo := &fakeDispoStore{duracion: 30, activo: true, profErr: errProfesionalDispoFuera}
	r := setupDispoTestEnv(t, srv.URL, dispo, admClinicaUser())
	if code, _ := doGet(t, r, query, mintAdm(t, tk)); code != http.StatusNotFound {
		t.Fatalf("profesional ajeno = %d, want 404", code)
	}

	dispo = &fakeDispoStore{duracion: 30, activo: true,
		prof: ProfesionalDispo{ID: "p1", Activo: false}}
	r = setupDispoTestEnv(t, srv.URL, dispo, admClinicaUser())
	if code, _ := doGet(t, r, query, mintAdm(t, tk)); code != http.StatusBadRequest {
		t.Fatalf("profesional inactivo = %d, want 400", code)
	}

	dispo = &fakeDispoStore{duracion: 30, activo: true,
		prof: ProfesionalDispo{ID: "p1", Activo: true}, hab: false}
	r = setupDispoTestEnv(t, srv.URL, dispo, admClinicaUser())
	if code, _ := doGet(t, r, query, mintAdm(t, tk)); code != http.StatusBadRequest {
		t.Fatalf("profesional sin el servicio = %d, want 400", code)
	}
}

func TestDisponibilidadMatrizPlataforma403(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	dispo := &fakeDispoStore{duracion: 30, activo: true, hab: true}
	r := setupDispoTestEnv(t, srv.URL, dispo, admClinicaUser())

	// Matriz RF17: adm-plataforma no consulta disponibilidad.
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleAdmPlataforma}, testKid, "")
	code, _ := doGet(t, r, "/profesionales/p1/disponibilidad?fecha=2030-03-11&servicioId=s1", tok)
	if code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", code)
	}
	code, _ = doGet(t, r, "/servicios/s1/disponibilidad?desde=2030-03-11&hasta=2030-03-12", tok)
	if code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", code)
	}
}

// --- Handlers RF8 ---

func TestDisponibilidadServicioOK(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	prevNow := nowFn
	nowFn = func() time.Time { return fechaLocal(t, 2030, 3, 11, 7, 0) }
	t.Cleanup(func() { nowFn = prevNow })

	fecha := fechaLocal(t, 2030, 3, 11, 12, 0)
	dispo := &fakeDispoStore{
		duracion: 30,
		activo:   true,
		profs: []ProfesionalDispo{
			{ID: "p1", IdpSub: "sub-1", Especialidad: "Cardiología", Activo: true},
			{ID: "p2", IdpSub: "sub-2", Especialidad: "Cardiología", Activo: true},
		},
		datos: map[string]DatosAgenda{
			"p1": {Horarios: []HorarioAgenda{{
				DiaSemana: isoDow(fecha.Weekday()), MinutoInicio: 8 * 60, MinutoFin: 12 * 60,
				VigenteDesde: "2030-01-01",
			}}},
			"p2": {}, // sin horario: sin espacios
		},
	}
	r := setupDispoTestEnv(t, srv.URL, dispo, admClinicaUser())

	code, w := doGet(t, r,
		"/servicios/s1/disponibilidad?desde=2030-03-11&hasta=2030-03-12&clinicaId=clinica-sur",
		mintAdm(t, tk))
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	body := w.Body.String()
	// Solo la fecha con espacios (2030-03-12 no tiene horario de ese día).
	if !contains(body, `"fecha":"2030-03-11"`) || contains(body, `"fecha":"2030-03-12"`) {
		t.Fatalf("fechas = %s", body)
	}
	if !contains(body, `"profesionalId":"p1"`) || contains(body, `"profesionalId":"p2"`) {
		t.Fatalf("profesionales = %s", body)
	}
	if dispo.gotClinica != "clinica-norte" {
		t.Fatalf("I5: clínica = %s", dispo.gotClinica)
	}
}

func TestDisponibilidadServicioRangoInvalido400(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	dispo := &fakeDispoStore{duracion: 30, activo: true}
	r := setupDispoTestEnv(t, srv.URL, dispo, admClinicaUser())

	for nombre, query := range map[string]string{
		"desde > hasta":  "/servicios/s1/disponibilidad?desde=2030-03-12&hasta=2030-03-11",
		"rango excesivo": "/servicios/s1/disponibilidad?desde=2030-03-01&hasta=2030-06-01",
		"sin fechas":     "/servicios/s1/disponibilidad",
	} {
		t.Run(nombre, func(t *testing.T) {
			code, _ := doGet(t, r, query, mintAdm(t, tk))
			if code != http.StatusBadRequest {
				t.Fatalf("status=%d", code)
			}
		})
	}
}

func TestDisponibilidadServicioFuera404(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	dispo := &fakeDispoStore{duracion: 30, svcErr: errServicioDispoFuera}
	r := setupDispoTestEnv(t, srv.URL, dispo, admClinicaUser())
	code, _ := doGet(t, r, "/servicios/ajeno/disponibilidad?desde=2030-03-11&hasta=2030-03-12", mintAdm(t, tk))
	if code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", code)
	}
}

func TestDisponibilidadStoreCaido503(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	r := setupDispoTestEnv(t, srv.URL, nil, admClinicaUser())
	code, _ := doGet(t, r, "/profesionales/p1/disponibilidad?fecha=2030-03-11&servicioId=s1", mintAdm(t, tk))
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", code)
	}
	if code, _ := doGet(t, r, "/servicios/s1/disponibilidad?desde=2030-03-11&hasta=2030-03-12", mintAdm(t, tk)); code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", code)
	}
}
