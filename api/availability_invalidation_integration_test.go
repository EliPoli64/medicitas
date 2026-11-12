//go:build integration

package main

// Pruebas de integración de la invalidación contra Redis real.
//
// Usan la API (handlers RF5/RF6) con stores falsos y una Cache respaldada por
// Redis. No requieren PostgreSQL: el objetivo es comprobar que el handler
// publica un epoch nuevo y que la entrada cacheada del epoch anterior deja de
// servirse.
//
// Variables de entorno, iguales al resto de las pruebas de Redis:
//
//	REDIS_ADDR      host:puerto (por defecto localhost:6379)
//	REDIS_PASSWORD  password opcional
//	REDIS_DB        base lógica (por defecto 15)
//
// Cada prueba usa un prefijo único y limpia solo sus claves con SCAN+DEL; no
// se usa FLUSHDB.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"medicitas/api/internal/availabilitycache"
)

func newIntegrationInvalidationCache(t *testing.T) (*availabilitycache.Cache, *redis.Client, string) {
	t.Helper()

	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	password := os.Getenv("REDIS_PASSWORD")
	db := 15
	if raw := os.Getenv("REDIS_DB"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("REDIS_DB = %q no es un número", raw)
		}
		db = parsed
	}

	client := redis.NewClient(&redis.Options{
		Addr:        addr,
		Password:    password,
		DB:          db,
		DialTimeout: 2 * time.Second,
	})
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		t.Fatalf("Redis no disponible en %s: %v", addr, err)
	}

	prefix := fmt.Sprintf("medicitas-it-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupIntegrationPrefix(t, client, prefix)
		_ = client.Close()
	})

	store, err := availabilitycache.NewRedisStore(availabilitycache.RedisConfig{
		Addr:         addr,
		Password:     password,
		DB:           db,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewRedisStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := availabilitycache.DefaultConfig()
	cfg.Prefix = prefix
	cache, err := availabilitycache.NewCache(store, cfg)
	if err != nil {
		t.Fatalf("NewCache() error = %v", err)
	}

	prev := availabilityCache
	availabilityCache = cache
	t.Cleanup(func() { availabilityCache = prev })

	return cache, client, prefix
}

func cleanupIntegrationPrefix(t *testing.T, client *redis.Client, prefix string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var cursor uint64
	for {
		keys, next, err := client.Scan(ctx, cursor, prefix+":*", 100).Result()
		if err != nil {
			t.Logf("cleanup: SCAN %q: %v", prefix, err)
			return
		}
		if len(keys) > 0 {
			if err := client.Del(ctx, keys...).Err(); err != nil {
				t.Logf("cleanup: DEL %v: %v", keys, err)
			}
		}
		cursor = next
		if cursor == 0 {
			return
		}
	}
}

func integrationLoader() availabilitycache.LoaderFunc {
	return func(_ context.Context, req availabilitycache.Request) (availabilitycache.Snapshot, error) {
		return availabilitycache.Snapshot{
			SchemaVersion:   availabilitycache.SnapshotSchemaVersion,
			ClinicaID:       req.ClinicaID,
			ProfesionalID:   req.ProfesionalID,
			ServicioID:      req.ServicioID,
			Fecha:           req.Fecha,
			ZonaHoraria:     "America/Costa_Rica",
			DuracionMinutos: 30,
			PasoMinutos:     req.PasoMinutos,
			Espacios: []availabilitycache.Slot{{
				Inicio: time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC),
				Fin:    time.Date(2026, 11, 20, 14, 30, 0, 0, time.UTC),
			}},
			GeneradoEn: time.Now().UTC(),
		}, nil
	}
}

func integrationEpochKey(prefix string, req availabilitycache.Request) string {
	return prefix + ":avail:" + availabilitycache.DefaultFormatVersion +
		":epoch:" + req.ClinicaID + ":" + req.ProfesionalID
}

func seedCachedAvailability(t *testing.T, cache *availabilitycache.Cache, client *redis.Client, prefix string, req availabilitycache.Request) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	load := integrationLoader()
	if res, err := cache.GetOrLoad(ctx, req, load); err != nil || res.Hit {
		t.Fatalf("consulta inicial: hit=%v err=%v", res.Hit, err)
	}
	if res, err := cache.GetOrLoad(ctx, req, load); err != nil || !res.Hit {
		t.Fatalf("consulta repetida: hit=%v err=%v", res.Hit, err)
	}

	before, err := client.Get(ctx, integrationEpochKey(prefix, req)).Result()
	if err != nil {
		t.Fatalf("leer epoch inicial: %v", err)
	}
	return before
}

func assertCacheInvalidated(t *testing.T, cache *availabilitycache.Cache, client *redis.Client, prefix string, req availabilitycache.Request, before string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	after, err := client.Get(ctx, integrationEpochKey(prefix, req)).Result()
	if err != nil {
		t.Fatalf("leer epoch final: %v", err)
	}
	if after == before {
		t.Fatal("el epoch no cambió después de la mutación")
	}
	t.Logf("epoch %s: %s -> %s", integrationEpochKey(prefix, req), before, after)
	t.Logf("data key anterior: %s:avail:%s:data:%s:%s:%s:%s:%d:%s",
		prefix, availabilitycache.DefaultFormatVersion, req.ClinicaID, req.ProfesionalID,
		req.ServicioID, req.Fecha, req.PasoMinutos, before)

	res, err := cache.GetOrLoad(ctx, req, integrationLoader())
	if err != nil {
		t.Fatalf("GetOrLoad() posterior error = %v", err)
	}
	if res.Hit {
		t.Fatal("se sirvió una entrada cacheada bajo el epoch anterior")
	}
}

func TestIntegrationRF5InvalidaCache(t *testing.T) {
	cache, client, prefix := newIntegrationInvalidationCache(t)
	req := availabilitycache.Request{
		ClinicaID:     "clinica-norte",
		ProfesionalID: "p1",
		ServicioID:    "s1",
		Fecha:         "2026-11-20",
		PasoMinutos:   30,
	}
	before := seedCachedAvailability(t, cache, client, prefix, req)

	tk := newTestKeys(t)
	srv := tk.jwksServer()
	t.Cleanup(srv.Close)

	sched := &fakeScheduleStore{
		owners:       map[string]string{"p1": "u-prof"},
		byProf:       map[string][]Schedule{},
		horarioOwner: map[string]string{},
	}
	r := setupAgendaTestEnv(t, srv.URL, sched, &fakeBlockStore{}, admAgendaUser())

	body := `{"diaSemana":1,"horaInicio":"09:00","horaFin":"12:00","vigenteDesde":"2026-01-01"}`
	code, w := doJSON(t, r, http.MethodPost, "/profesionales/p1/horarios",
		mintAgenda(t, tk, []string{RoleAdmClinica}), body)
	if code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}

	assertCacheInvalidated(t, cache, client, prefix, req, before)
}

func TestIntegrationRF6InvalidaCache(t *testing.T) {
	cache, client, prefix := newIntegrationInvalidationCache(t)
	req := availabilitycache.Request{
		ClinicaID:     "clinica-norte",
		ProfesionalID: "p1",
		ServicioID:    "s1",
		Fecha:         "2026-11-20",
		PasoMinutos:   30,
	}
	before := seedCachedAvailability(t, cache, client, prefix, req)

	tk := newTestKeys(t)
	srv := tk.jwksServer()
	t.Cleanup(srv.Close)

	sched := &fakeScheduleStore{
		owners:       map[string]string{"p1": "u-prof"},
		byProf:       map[string][]Schedule{},
		horarioOwner: map[string]string{},
	}
	blocks := &fakeBlockStore{
		byProf:       map[string][]Block{"p1": {{ID: "b1", ProfesionalID: "p1", Motivo: "x"}}},
		bloqueoOwner: map[string]string{"b1": "p1"},
	}
	r := setupAgendaTestEnv(t, srv.URL, sched, blocks, admAgendaUser())

	code, _ := doJSON(t, r, http.MethodDelete, "/bloqueos/b1",
		mintAgenda(t, tk, []string{RoleAdmClinica}), "")
	if code != http.StatusNoContent {
		t.Fatalf("status=%d want 204", code)
	}

	assertCacheInvalidated(t, cache, client, prefix, req, before)
}
