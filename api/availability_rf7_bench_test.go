//go:build integration

package main

// Benchmark de RF7: PostgreSQL directo vs HIT válido de Redis con validación
// de AgendaVersion.
//
// Requiere PostgreSQL y Redis reales. Usa los mismos fixtures y la misma
// solicitud para ambas ramas; la única diferencia es si el cálculo se ejecuta
// directamente sobre PostgreSQL o se sirve desde Redis validando la versión.
//
// Variables de entorno:
//   DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, DB_NAME
//   REDIS_ADDR, REDIS_PASSWORD, REDIS_DB

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"medicitas/api/internal/availabilitycache"
	"medicitas/api/internal/availabilityversion"
)

// benchRF7Env prepara una clínica, profesional, servicio, paciente y horario
// una sola vez para todos los sub-benchmarks.
type benchRF7Env struct {
	db            *sql.DB
	clinicaID     string
	clinicaIdent  string
	profesionalID string
	servicioID    string
	fecha         string
	req           availabilitycache.Request
	loader        interface {
		Load(context.Context, availabilitycache.Request) (availabilitycache.Snapshot, error)
	}
	resolver availabilitycache.AgendaVersionResolver
	cache    *availabilitycache.Cache
}

func setupBenchRF7Env(b *testing.B) *benchRF7Env {
	b.Helper()

	db := agendaVersionDB(b)
	b.Cleanup(func() { _ = db.Close() })

	clinicaID, profesionalID, servicioID, _ := fixturesAgendaVersion(b, db)
	clinicaIdent := identificadorDeClinica(b, db, clinicaID)
	citaBase, _ := fixtureHorarioYPaciente(b, db, clinicaID, profesionalID)

	prevNow := nowFn
	nowFn = func() time.Time { return citaBase.Add(-24 * time.Hour) }
	b.Cleanup(func() { nowFn = prevNow })

	store := newIntegrationRedisStoreForVersion(b)
	prefix := fmt.Sprintf("medicitas-bench-%d", time.Now().UnixNano())
	cleanupPrefixFromStore(b, store, prefix)

	cfg := availabilitycache.DefaultConfig()
	cfg.Prefix = prefix
	cfg.OpTimeout = 500 * time.Millisecond

	resolver := availabilityversion.NewPostgresResolver(db)
	cache, err := availabilitycache.NewCache(store, cfg, availabilitycache.WithVersionResolver(resolver))
	if err != nil {
		b.Fatalf("NewCache() error = %v", err)
	}

	dispoStore := newPGDisponibilidadStore(db)
	loader := newDisponibilidadLoader(dispoStore)

	req := availabilitycache.Request{
		ClinicaID:     clinicaIdent,
		ProfesionalID: profesionalID,
		ServicioID:    servicioID,
		Fecha:         citaBase.Format("2006-01-02"),
		PasoMinutos:   30,
	}

	// Precalentar Redis: un MISS seguido de un HIT para asegurar que el
	// benchmark de Redis mida lecturas de caché, no el cálculo inicial.
	ctx := context.Background()
	if _, err := cache.GetOrLoad(ctx, req, loader.Load); err != nil {
		b.Fatalf("warm-up MISS error = %v", err)
	}
	res, err := cache.GetOrLoad(ctx, req, loader.Load)
	if err != nil {
		b.Fatalf("warm-up HIT error = %v", err)
	}
	if !res.Hit {
		b.Fatal("warm-up no produjo HIT")
	}

	return &benchRF7Env{
		db:            db,
		clinicaID:     clinicaID,
		clinicaIdent:  clinicaIdent,
		profesionalID: profesionalID,
		servicioID:    servicioID,
		fecha:         req.Fecha,
		req:           req,
		loader:        loader,
		resolver:      resolver,
		cache:         cache,
	}
}

// BenchmarkRF7_DirectPG mide el cálculo de RF7 leyendo directamente de
// PostgreSQL, sin caché. Incluye la consulta de AgendaVersion para comparar
// contra la rama de Redis, que también la realiza al validar un HIT.
func BenchmarkRF7_DirectPG(b *testing.B) {
	env := setupBenchRF7Env(b)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := env.loader.Load(ctx, env.req); err != nil {
			b.Fatalf("Load error = %v", err)
		}
		if _, err := env.resolver.Resolve(ctx, env.clinicaIdent, env.profesionalID); err != nil {
			b.Fatalf("Resolve error = %v", err)
		}
	}
}

// BenchmarkRF7_RedisHit mide RF7 cuando el snapshot ya está en Redis y la
// AgendaVersion en PostgreSQL coincide (HIT válido). El tiempo incluye la
// lectura de Redis y la validación de versión.
func BenchmarkRF7_RedisHit(b *testing.B) {
	env := setupBenchRF7Env(b)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := env.cache.GetOrLoad(ctx, env.req, env.loader.Load)
		if err != nil {
			b.Fatalf("GetOrLoad error = %v", err)
		}
		if !res.Hit {
			b.Fatalf("esperaba HIT, obtuvo MISS en iteración %d", i)
		}
	}
}
