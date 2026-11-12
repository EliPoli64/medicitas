//go:build integration

package main

// Pruebas de integración de availabilitycache contra PostgreSQL y Redis reales.
//
// Verifican que GetOrLoad valide los snapshots de Redis contra la versión
// autoritativa "AgendaVersion" de PostgreSQL.
//
// Variables de entorno:
//   DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, DB_NAME
//   REDIS_ADDR, REDIS_PASSWORD, REDIS_DB

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"medicitas/api/internal/availabilitycache"
	"medicitas/api/internal/availabilityversion"
)

func newIntegrationRedisStoreForVersion(t *testing.T) *availabilitycache.RedisStore {
	t.Helper()

	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("REDIS_ADDR = %q no es host:puerto: %v", addr, err)
	}

	db := 15
	if raw := os.Getenv("REDIS_DB"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("REDIS_DB = %q no es un número", raw)
		}
		db = parsed
	}

	store, err := availabilitycache.NewRedisStore(availabilitycache.RedisConfig{
		Addr:         net.JoinHostPort(host, port),
		Password:     os.Getenv("REDIS_PASSWORD"),
		DB:           db,
		DialTimeout:  1 * time.Second,
		ReadTimeout:  1 * time.Second,
		WriteTimeout: 1 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRedisStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func cleanupPrefixFromStore(t *testing.T, store *availabilitycache.RedisStore, prefix string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := store.Cleanup(ctx, prefix+":*"); err != nil {
		t.Fatalf("Cleanup(%q) error = %v", prefix+":*", err)
	}
}

func identificadorDeClinica(t *testing.T, db *sql.DB, clinicaID string) string {
	t.Helper()

	var ident string
	if err := db.QueryRow(
		`SELECT "identificador" FROM "Clinica" WHERE "id" = $1::uuid`,
		clinicaID,
	).Scan(&ident); err != nil {
		t.Fatalf("obtener identificador de clínica %s: %v", clinicaID, err)
	}
	return ident
}

func TestAvailabilityCacheVersionIntegration_HitThenInvalidatedByAgendaChange(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	store := newIntegrationRedisStoreForVersion(t)
	prefix := fmt.Sprintf("medicitas-it-%d", time.Now().UnixNano())
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

	clinicaID, profesionalID, servicioID, usuarioID := fixturesAgendaVersion(t, db)
	clinicaIdent := identificadorDeClinica(t, db, clinicaID)
	citaBase, pacienteID := fixtureHorarioYPaciente(t, db, clinicaID, profesionalID)
	_ = pacienteID

	// Congelar el reloj para que los espacios del próximo lunes no queden
	// filtrados como pasados.
	prevNow := nowFn
	nowFn = func() time.Time { return citaBase.Add(-24 * time.Hour) }
	t.Cleanup(func() { nowFn = prevNow })

	// Request.ClinicaID usa el identificador de clínica, igual que el loader.
	// El resolver de versiones se encarga de traducirlo a UUID.
	req := availabilitycache.Request{
		ClinicaID:     clinicaIdent,
		ProfesionalID: profesionalID,
		ServicioID:    servicioID,
		Fecha:         citaBase.Format("2006-01-02"),
		PasoMinutos:   30,
	}

	loader := newDisponibilidadLoader(newPGDisponibilidadStore(db)).Load
	ctx := context.Background()

	// Primera consulta: miss, calcula desde PostgreSQL.
	first, err := cache.GetOrLoad(ctx, req, loader)
	if err != nil {
		t.Fatalf("GetOrLoad(first) error = %v", err)
	}
	if first.Hit {
		t.Fatal("first GetOrLoad() reported a hit, want a miss")
	}
	v0 := versionDeAgenda(t, db, clinicaID, profesionalID)
	if first.Snapshot.AgendaVersion != v0 {
		t.Fatalf("first snapshot agenda version = %d, want %d", first.Snapshot.AgendaVersion, v0)
	}

	// Segunda consulta: hit, el snapshot coincide con la versión de PostgreSQL.
	second, err := cache.GetOrLoad(ctx, req, loader)
	if err != nil {
		t.Fatalf("GetOrLoad(second) error = %v", err)
	}
	if !second.Hit {
		t.Fatal("second GetOrLoad() did not hit the cache")
	}

	// Cambiar la agenda (alta de un bloqueo) aumenta la versión autoritativa.
	bloqueoID := newTestUUID()
	mustExec(ctx, t, db,
		`INSERT INTO "Bloqueo" ("id", "clinicaId", "profesionalId", "inicio", "fin", "motivo", "creadoPorUsuarioId")
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4::timestamptz, $5::timestamptz, 'mantenimiento', $6::uuid)`,
		bloqueoID, clinicaID, profesionalID, citaBase.UTC(), citaBase.Add(30*time.Minute).UTC(), usuarioID)

	v1 := versionDeAgenda(t, db, clinicaID, profesionalID)
	if v1 != v0+1 {
		t.Fatalf("agenda version after block = %d, want %d", v1, v0+1)
	}

	// Tercera consulta: el snapshot en Redis está desactualizado; debe
	// descartarse y recalcularse.
	third, err := cache.GetOrLoad(ctx, req, loader)
	if err != nil {
		t.Fatalf("GetOrLoad(third) error = %v", err)
	}
	if third.Hit {
		t.Fatal("third GetOrLoad() served a stale snapshot after agenda change")
	}
	if third.Snapshot.AgendaVersion != v1 {
		t.Fatalf("third snapshot agenda version = %d, want %d", third.Snapshot.AgendaVersion, v1)
	}

	// Cuarta consulta: el snapshot recién calculado ya puede servirse.
	fourth, err := cache.GetOrLoad(ctx, req, loader)
	if err != nil {
		t.Fatalf("GetOrLoad(fourth) error = %v", err)
	}
	if !fourth.Hit {
		t.Fatal("fourth GetOrLoad() did not hit the cache after recalculation")
	}
}

func TestAvailabilityCacheVersionIntegration_FallbackWhenResolverFails(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	store := newIntegrationRedisStoreForVersion(t)
	prefix := fmt.Sprintf("medicitas-it-%d", time.Now().UnixNano())
	cleanupPrefixFromStore(t, store, prefix)

	cfg := availabilitycache.DefaultConfig()
	cfg.Prefix = prefix
	cfg.OpTimeout = 500 * time.Millisecond

	// Resolver que siempre falla para simular PostgreSQL no disponible.
	failingResolver := availabilityversion.NewPostgresResolver(nil)

	cache, err := availabilitycache.NewCache(store, cfg, availabilitycache.WithVersionResolver(failingResolver))
	if err != nil {
		t.Fatalf("NewCache() error = %v", err)
	}

	clinicaID, profesionalID, servicioID, _ := fixturesAgendaVersion(t, db)
	clinicaIdent := identificadorDeClinica(t, db, clinicaID)
	citaBase, pacienteID := fixtureHorarioYPaciente(t, db, clinicaID, profesionalID)
	_ = pacienteID

	prevNow := nowFn
	nowFn = func() time.Time { return citaBase.Add(-24 * time.Hour) }
	t.Cleanup(func() { nowFn = prevNow })

	req := availabilitycache.Request{
		ClinicaID:     clinicaIdent,
		ProfesionalID: profesionalID,
		ServicioID:    servicioID,
		Fecha:         citaBase.Format("2006-01-02"),
		PasoMinutos:   30,
	}

	loader := newDisponibilidadLoader(newPGDisponibilidadStore(db)).Load
	result, err := cache.GetOrLoad(context.Background(), req, loader)
	if err != nil {
		t.Fatalf("GetOrLoad() error = %v", err)
	}
	if result.Hit {
		t.Fatal("GetOrLoad() reported a hit when resolver is failing")
	}
	if !errors.Is(result.CacheErr, availabilitycache.ErrCacheRead) {
		t.Fatalf("CacheErr = %v, want ErrCacheRead", result.CacheErr)
	}
	if len(result.Snapshot.Espacios) == 0 {
		t.Fatal("expected loader result to contain slots")
	}
}
