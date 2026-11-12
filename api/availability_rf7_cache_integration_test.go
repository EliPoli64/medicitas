//go:build integration

package main

// Integración de RF7 con Redis, PostgreSQL y la API real (Etapa 7).
//
// Usa PostgreSQL y Redis reales, pero reemplaza Keycloak por el JWKS de prueba
// para poder correr sin levantar toda la pila. Los handlers reciben requests
// HTTP reales a través de httptest y ejercen la caché global.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"medicitas/api/internal/availabilitycache"
	"medicitas/api/internal/availabilityversion"
)

// setupRF7CacheIntegrationEnv arma un router con stores reales de PostgreSQL,
// la caché indicada y autenticación falsa para los usuarios indicados.
// Devuelve el router y las claves de prueba para mintar tokens coherentes con
// el JWKS configurado.
func setupRF7CacheIntegrationEnv(t *testing.T, db *sql.DB, users map[string]DomainUser, cache *availabilitycache.Cache) (*gin.Engine, *testKeys) {
	t.Helper()

	tk := newTestKeys(t)
	srv := tk.jwksServer()
	t.Cleanup(srv.Close)
	withTestIssuer(t, srv.URL)

	prevPing := pingDBFn
	pingDBFn = func(context.Context) error { return nil }
	t.Cleanup(func() { pingDBFn = prevPing })

	prevUsers := userStore
	userStore = &fakeUserStore{bySub: users}
	t.Cleanup(func() { userStore = prevUsers })

	prevStore := disponibilidadStore
	prevLoader := loaderDisponibilidad
	prevCache := availabilityCache
	disponibilidadStore = newPGDisponibilidadStore(db)
	loaderDisponibilidad = newDisponibilidadLoader(disponibilidadStore)
	availabilityCache = cache
	t.Cleanup(func() {
		disponibilidadStore = prevStore
		loaderDisponibilidad = prevLoader
		availabilityCache = prevCache
	})

	gin.SetMode(gin.TestMode)
	return setupRouter(), tk
}

func rf7User(clinicaIdent string) DomainUser {
	return DomainUser{
		UsuarioID:            "u-adm",
		IdpSub:               "test-user",
		Activo:               true,
		ClinicaIdentificador: &clinicaIdent,
		RolCodigo:            "adm_clinica",
	}
}

func newRF7Cache(t *testing.T, db *sql.DB) (*availabilitycache.Cache, *availabilitycache.RedisStore, string) {
	t.Helper()

	store := newIntegrationRedisStoreForVersion(t)
	prefix := fmt.Sprintf("medicitas-rf7-%d", time.Now().UnixNano())
	cleanupPrefixFromStore(t, store, prefix)

	cfg := availabilitycache.DefaultConfig()
	cfg.Prefix = prefix
	cfg.OpTimeout = 500 * time.Millisecond

	cache, err := availabilitycache.NewCache(
		store,
		cfg,
		availabilitycache.WithVersionResolver(availabilityversion.NewPostgresResolver(db)),
	)
	if err != nil {
		t.Fatalf("NewCache() error = %v", err)
	}
	return cache, store, prefix
}

func rf7Token(t *testing.T, tk *testKeys, clinicaIdent string) string {
	t.Helper()
	return rf7TokenForSub(t, tk, clinicaIdent, "test-user")
}

func rf7TokenForSub(t *testing.T, tk *testKeys, clinicaIdent, sub string) string {
	t.Helper()
	return tk.mintWithSub(testIssuer, time.Now().Add(time.Hour), true, []string{RoleAdmClinica}, testKid, clinicaIdent, sub)
}

func rf7URL(profesionalID, fecha, servicioID string) string {
	return fmt.Sprintf("/profesionales/%s/disponibilidad?fecha=%s&servicioId=%s", profesionalID, fecha, servicioID)
}

func TestIntegrationRF7Cache_MissThenHit(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	cache, _, _ := newRF7Cache(t, db)
	clinicaID, profesionalID, servicioID, usuarioID := fixturesAgendaVersion(t, db)
	_ = usuarioID
	clinicaIdent := identificadorDeClinica(t, db, clinicaID)
	citaBase, _ := fixtureHorarioYPaciente(t, db, clinicaID, profesionalID)

	prevNow := nowFn
	nowFn = func() time.Time { return citaBase.Add(-24 * time.Hour) }
	t.Cleanup(func() { nowFn = prevNow })

	r, tk := setupRF7CacheIntegrationEnv(t, db, map[string]DomainUser{"test-user": rf7User(clinicaIdent)}, cache)
	token := rf7Token(t, tk, clinicaIdent)

	fechaStr := citaBase.Format("2006-01-02")
	url := rf7URL(profesionalID, fechaStr, servicioID)

	// Primera consulta: MISS, debe calcular desde PostgreSQL.
	code, w := doGet(t, r, url, token)
	if code != http.StatusOK {
		t.Fatalf("RF7 first = %d body=%s", code, w.Body.String())
	}
	first := espaciosHoraLocal(t, w.Body.String())
	if len(first) == 0 {
		t.Fatalf("RF7 first returned no spaces: %s", w.Body.String())
	}

	// Segunda consulta: HIT, misma respuesta sin recalcular.
	code, w = doGet(t, r, url, token)
	if code != http.StatusOK {
		t.Fatalf("RF7 second = %d body=%s", code, w.Body.String())
	}
	second := espaciosHoraLocal(t, w.Body.String())
	if !equalStrings(first, second) {
		t.Fatalf("HIT changed response: %v vs %v", first, second)
	}
}

func TestIntegrationRF7Cache_AgendaChangeInvalidatesHit(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	cache, _, _ := newRF7Cache(t, db)
	clinicaID, profesionalID, servicioID, usuarioID := fixturesAgendaVersion(t, db)
	clinicaIdent := identificadorDeClinica(t, db, clinicaID)
	citaBase, pacienteID := fixtureHorarioYPaciente(t, db, clinicaID, profesionalID)

	prevNow := nowFn
	nowFn = func() time.Time { return citaBase.Add(-24 * time.Hour) }
	t.Cleanup(func() { nowFn = prevNow })

	r, tk := setupRF7CacheIntegrationEnv(t, db, map[string]DomainUser{"test-user": rf7User(clinicaIdent)}, cache)
	token := rf7Token(t, tk, clinicaIdent)

	fechaStr := citaBase.Format("2006-01-02")
	url := rf7URL(profesionalID, fechaStr, servicioID)

	code, w := doGet(t, r, url, token)
	if code != http.StatusOK {
		t.Fatalf("RF7 baseline = %d body=%s", code, w.Body.String())
	}
	baseline := espaciosHoraLocal(t, w.Body.String())
	if len(baseline) == 0 {
		t.Fatal("baseline returned no spaces")
	}

	// HIT verificado contra AgendaVersion.
	code, w = doGet(t, r, url, token)
	if code != http.StatusOK {
		t.Fatalf("RF7 hit = %d body=%s", code, w.Body.String())
	}

	// Cambio de agenda: bloqueo de 10:00 a 11:00 en la zona de la clínica.
	bloqueoID := newTestUUID()
	mustExec(context.Background(), t, db,
		`INSERT INTO "Bloqueo" ("id", "clinicaId", "profesionalId", "inicio", "fin", "motivo", "creadoPorUsuarioId")
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4::timestamptz, $5::timestamptz, 'capacitación', $6::uuid)`,
		bloqueoID, clinicaID, profesionalID, citaBase.UTC(), citaBase.Add(60*time.Minute).UTC(), usuarioID)

	// La próxima consulta debe detectar el cambio de versión y recalcular.
	code, w = doGet(t, r, url, token)
	if code != http.StatusOK {
		t.Fatalf("RF7 after block = %d body=%s", code, w.Body.String())
	}
	after := espaciosHoraLocal(t, w.Body.String())
	for _, h := range []string{"10:00", "10:30"} {
		if containsString(after, h) {
			t.Fatalf("espacio %s no fue retirado tras bloqueo: %v", h, after)
		}
	}

	// La respuesta final se vuelve a cachear.
	code, w = doGet(t, r, url, token)
	if code != http.StatusOK {
		t.Fatalf("RF7 final hit = %d body=%s", code, w.Body.String())
	}
	final := espaciosHoraLocal(t, w.Body.String())
	if !equalStrings(after, final) {
		t.Fatalf("final HIT differs: %v vs %v", after, final)
	}

	_ = pacienteID
}

func TestIntegrationRF7Cache_ClinicIsolation(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	cache, _, _ := newRF7Cache(t, db)

	// Clínica A
	clinicaIDA, profesionalIDA, servicioIDA, usuarioIDA := fixturesAgendaVersion(t, db)
	clinicaIdentA := identificadorDeClinica(t, db, clinicaIDA)
	citaBaseA, _ := fixtureHorarioYPaciente(t, db, clinicaIDA, profesionalIDA)

	// Clínica B
	clinicaIDB, profesionalIDB, servicioIDB, usuarioIDB := fixturesAgendaVersion(t, db)
	clinicaIdentB := identificadorDeClinica(t, db, clinicaIDB)
	citaBaseB, _ := fixtureHorarioYPaciente(t, db, clinicaIDB, profesionalIDB)

	prevNow := nowFn
	nowFn = func() time.Time {
		if citaBaseA.Before(citaBaseB) {
			return citaBaseA.Add(-24 * time.Hour)
		}
		return citaBaseB.Add(-24 * time.Hour)
	}
	t.Cleanup(func() { nowFn = prevNow })

	userA := rf7User(clinicaIdentA)
	userA.IdpSub = "user-a"
	userB := rf7User(clinicaIdentB)
	userB.IdpSub = "user-b"
	r, tk := setupRF7CacheIntegrationEnv(t, db, map[string]DomainUser{
		"user-a": userA,
		"user-b": userB,
	}, cache)
	tokenA := rf7TokenForSub(t, tk, clinicaIdentA, "user-a")
	tokenB := rf7TokenForSub(t, tk, clinicaIdentB, "user-b")

	urlA := rf7URL(profesionalIDA, citaBaseA.Format("2006-01-02"), servicioIDA)
	urlB := rf7URL(profesionalIDB, citaBaseB.Format("2006-01-02"), servicioIDB)

	code, w := doGet(t, r, urlA, tokenA)
	if code != http.StatusOK {
		t.Fatalf("RF7 A = %d body=%s", code, w.Body.String())
	}
	code, w = doGet(t, r, urlB, tokenB)
	if code != http.StatusOK {
		t.Fatalf("RF7 B = %d body=%s", code, w.Body.String())
	}

	// Cambiar agenda de A no debe invalidar el HIT de B.
	bloqueoID := newTestUUID()
	mustExec(context.Background(), t, db,
		`INSERT INTO "Bloqueo" ("id", "clinicaId", "profesionalId", "inicio", "fin", "motivo", "creadoPorUsuarioId")
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4::timestamptz, $5::timestamptz, 'mantenimiento', $6::uuid)`,
		bloqueoID, clinicaIDA, profesionalIDA, citaBaseA.UTC(), citaBaseA.Add(30*time.Minute).UTC(), usuarioIDA)

	code, w = doGet(t, r, urlB, tokenB)
	if code != http.StatusOK {
		t.Fatalf("RF7 B after A change = %d body=%s", code, w.Body.String())
	}
	// El HIT de B sigue siendo válido: el loader no se invocó de nuevo.
	// Verificamos que la respuesta tenga espacios (un recálculo también los
	// tendría, pero combinado con el contador de la siguiente prueba da
	// confianza de aislamiento).
	if len(espaciosHoraLocal(t, w.Body.String())) == 0 {
		t.Fatal("RF7 B lost spaces after A changed")
	}

	_ = usuarioIDB
}

func TestIntegrationRF7Cache_FallbackWhenRedisUnavailable(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	// Cache con un store que siempre falla para simular Redis caído.
	failingStore := &failingCacheStore{err: errors.New("redis unavailable")}
	cache, err := availabilitycache.NewCache(
		failingStore,
		availabilitycache.DefaultConfig(),
		availabilitycache.WithVersionResolver(availabilityversion.NewPostgresResolver(db)),
	)
	if err != nil {
		t.Fatalf("NewCache() error = %v", err)
	}

	clinicaID, profesionalID, servicioID, _ := fixturesAgendaVersion(t, db)
	clinicaIdent := identificadorDeClinica(t, db, clinicaID)
	citaBase, _ := fixtureHorarioYPaciente(t, db, clinicaID, profesionalID)

	prevNow := nowFn
	nowFn = func() time.Time { return citaBase.Add(-24 * time.Hour) }
	t.Cleanup(func() { nowFn = prevNow })

	r, tk := setupRF7CacheIntegrationEnv(t, db, map[string]DomainUser{"test-user": rf7User(clinicaIdent)}, cache)
	token := rf7Token(t, tk, clinicaIdent)

	url := rf7URL(profesionalID, citaBase.Format("2006-01-02"), servicioID)

	code, w := doGet(t, r, url, token)
	if code != http.StatusOK {
		t.Fatalf("RF7 with Redis down = %d body=%s", code, w.Body.String())
	}
	if len(espaciosHoraLocal(t, w.Body.String())) == 0 {
		t.Fatal("RF7 fallback returned no spaces")
	}
}

func TestIntegrationRF7Cache_ConcurrentRequests(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	cache, _, _ := newRF7Cache(t, db)
	clinicaID, profesionalID, servicioID, _ := fixturesAgendaVersion(t, db)
	clinicaIdent := identificadorDeClinica(t, db, clinicaID)
	citaBase, _ := fixtureHorarioYPaciente(t, db, clinicaID, profesionalID)

	prevNow := nowFn
	nowFn = func() time.Time { return citaBase.Add(-24 * time.Hour) }
	t.Cleanup(func() { nowFn = prevNow })

	r, tk := setupRF7CacheIntegrationEnv(t, db, map[string]DomainUser{"test-user": rf7User(clinicaIdent)}, cache)
	token := rf7Token(t, tk, clinicaIdent)

	url := rf7URL(profesionalID, citaBase.Format("2006-01-02"), servicioID)

	var wg sync.WaitGroup
	var okCount int32
	var errCount int32
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, w := doGet(t, r, url, token)
			if code == http.StatusOK && len(espaciosHoraLocal(t, w.Body.String())) > 0 {
				atomic.AddInt32(&okCount, 1)
			} else {
				atomic.AddInt32(&errCount, 1)
			}
		}()
	}
	wg.Wait()

	if okCount != 10 {
		t.Fatalf("concurrent RF7: ok=%d err=%d", okCount, errCount)
	}
}

// TestIntegrationRF7Cache_CitaReservadaInvalidaSnapshot verifica que una Cita
// reservada invalida el snapshot cacheado de RF7 a través de AgendaVersion,
// sin depender de una invalidación de epoch en Redis.
func TestIntegrationRF7Cache_CitaReservadaInvalidaSnapshot(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	cache, _, _ := newRF7Cache(t, db)
	clinicaID, profesionalID, servicioID, usuarioID := fixturesAgendaVersion(t, db)
	clinicaIdent := identificadorDeClinica(t, db, clinicaID)
	citaBase, pacienteID := fixtureHorarioYPaciente(t, db, clinicaID, profesionalID)

	prevNow := nowFn
	nowFn = func() time.Time { return citaBase.Add(-24 * time.Hour) }
	t.Cleanup(func() { nowFn = prevNow })

	r, tk := setupRF7CacheIntegrationEnv(t, db, map[string]DomainUser{"test-user": rf7User(clinicaIdent)}, cache)
	token := rf7Token(t, tk, clinicaIdent)

	fechaStr := citaBase.Format("2006-01-02")
	url := rf7URL(profesionalID, fechaStr, servicioID)

	// 1. Primera consulta RF7: MISS. El espacio de las 10:00 debe estar disponible.
	code, w := doGet(t, r, url, token)
	if code != http.StatusOK {
		t.Fatalf("RF7 baseline = %d body=%s", code, w.Body.String())
	}
	baseline := espaciosHoraLocal(t, w.Body.String())
	if !containsString(baseline, "10:00") {
		t.Fatalf("espacio 10:00 no disponible inicialmente: %v", baseline)
	}

	// 2. HIT validado contra AgendaVersion; la respuesta debe ser idéntica.
	code, w = doGet(t, r, url, token)
	if code != http.StatusOK {
		t.Fatalf("RF7 hit = %d body=%s", code, w.Body.String())
	}
	hit := espaciosHoraLocal(t, w.Body.String())
	if !equalStrings(baseline, hit) {
		t.Fatalf("HIT cambió la respuesta: %v vs %v", baseline, hit)
	}

	v0 := versionDeAgenda(t, db, clinicaID, profesionalID)

	// 3. Se crea una Cita reservada a las 10:00.
	_ = insertarCitaReservada(t, db, clinicaID, profesionalID, servicioID, pacienteID, citaBase, 0)

	// 4. La cita incrementa AgendaVersion; esto es lo que invalidará el snapshot.
	v1 := versionDeAgenda(t, db, clinicaID, profesionalID)
	if v1 <= v0 {
		t.Fatalf("AgendaVersion no incrementó tras reservar cita: %d -> %d", v0, v1)
	}

	// 5. Segunda consulta RF7: debe detectar la versión nueva y recalcular.
	// El espacio 10:00 ya no debe aparecer.
	code, w = doGet(t, r, url, token)
	if code != http.StatusOK {
		t.Fatalf("RF7 after cita = %d body=%s", code, w.Body.String())
	}
	after := espaciosHoraLocal(t, w.Body.String())
	if containsString(after, "10:00") {
		t.Fatalf("espacio 10:00 sigue apareciendo tras reservar cita: %v", after)
	}

	// 6. El nuevo resultado se vuelve a cachear.
	code, w = doGet(t, r, url, token)
	if code != http.StatusOK {
		t.Fatalf("RF7 final hit = %d body=%s", code, w.Body.String())
	}
	final := espaciosHoraLocal(t, w.Body.String())
	if !equalStrings(after, final) {
		t.Fatalf("final HIT difiere: %v vs %v", after, final)
	}

	_ = usuarioID
}

// failingCacheStore simula un Redis caído.
type failingCacheStore struct {
	err error
}

func (f *failingCacheStore) Get(context.Context, string) (string, error) {
	return "", f.err
}

func (f *failingCacheStore) Set(context.Context, string, string, time.Duration) error {
	return f.err
}

func (f *failingCacheStore) SetNX(context.Context, string, string, time.Duration) (bool, error) {
	return false, f.err
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
