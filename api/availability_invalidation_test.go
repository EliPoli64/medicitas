package main

// Pruebas de la invalidación de disponibilidad posterior a RF5/RF6.
//
// Usan la Cache real con un Store en memoria. Así se ejercita la rotación de
// epoch, la construcción de la clave y el manejo de errores sin depender de
// Redis ni de PostgreSQL.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"medicitas/api/internal/availabilitycache"
)

// recordingCacheStore implementa availabilitycache.Store en memoria.
type recordingCacheStore struct {
	mu    sync.Mutex
	value map[string]string

	setErr               error
	requireActiveContext bool

	setCalls   int
	lastSetKey string
}

func newRecordingCacheStore() *recordingCacheStore {
	return &recordingCacheStore{value: make(map[string]string)}
}

func (s *recordingCacheStore) Get(ctx context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.requireActiveContext {
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	v, ok := s.value[key]
	if !ok {
		return "", availabilitycache.ErrMiss
	}
	return v, nil
}

func (s *recordingCacheStore) Set(ctx context.Context, key, value string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.setCalls++
	s.lastSetKey = key
	if s.requireActiveContext {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if s.setErr != nil {
		return s.setErr
	}
	s.value[key] = value
	return nil
}

func (s *recordingCacheStore) SetNX(ctx context.Context, key, value string, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.requireActiveContext {
		if err := ctx.Err(); err != nil {
			return false, err
		}
	}
	if _, ok := s.value[key]; ok {
		return false, nil
	}
	s.value[key] = value
	return true, nil
}

func (s *recordingCacheStore) seed(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value[key] = value
}

func (s *recordingCacheStore) read(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.value[key]
	return v, ok
}

func (s *recordingCacheStore) setCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setCalls
}

// testEpochKey replica el layout documentado de la clave de epoch.
func testEpochKey(clinica, profesionalID string) string {
	return availabilitycache.DefaultPrefix + ":avail:" + availabilitycache.DefaultFormatVersion +
		":epoch:" + clinica + ":" + profesionalID
}

func seedEpoch(store *recordingCacheStore, clinica, profesionalID, token string) {
	store.seed(testEpochKey(clinica, profesionalID), token)
}

func assertEpochRotated(t *testing.T, store *recordingCacheStore, clinica, profesionalID, before string) {
	t.Helper()

	after, ok := store.read(testEpochKey(clinica, profesionalID))
	if !ok {
		t.Fatalf("no existe la clave de epoch %s/%s", clinica, profesionalID)
	}
	if after == before {
		t.Fatalf("el epoch de %s/%s no cambió", clinica, profesionalID)
	}
	if len(after) != 32 {
		t.Fatalf("epoch de %s/%s = %q, want 32 caracteres hex", clinica, profesionalID, after)
	}
}

func assertEpochUnchanged(t *testing.T, store *recordingCacheStore, clinica, profesionalID, want string) {
	t.Helper()

	got, ok := store.read(testEpochKey(clinica, profesionalID))
	if !ok {
		t.Fatalf("no existe la clave de epoch %s/%s", clinica, profesionalID)
	}
	if got != want {
		t.Fatalf("el epoch de %s/%s cambió a %q, want %q", clinica, profesionalID, got, want)
	}
}

func installAvailabilityCache(t *testing.T, store availabilitycache.Store) *availabilitycache.Cache {
	t.Helper()

	cache, err := availabilitycache.NewCache(store, availabilitycache.DefaultConfig())
	if err != nil {
		t.Fatalf("NewCache() error = %v", err)
	}

	prev := availabilityCache
	availabilityCache = cache
	t.Cleanup(func() { availabilityCache = prev })
	return cache
}

func TestInvalidarDisponibilidadSinCacheEsNoOp(t *testing.T) {
	prev := availabilityCache
	availabilityCache = nil
	t.Cleanup(func() { availabilityCache = prev })

	if err := invalidarDisponibilidadDeAgenda(context.Background(), "clinica-norte", "p1"); err != nil {
		t.Fatalf("error = %v, want nil sin caché", err)
	}
}

func TestInvalidarDisponibilidadRotaSoloLaAgendaObjetivo(t *testing.T) {
	store := newRecordingCacheStore()
	installAvailabilityCache(t, store)

	const (
		clinicaNorte = "clinica-norte"
		clinicaSur   = "clinica-sur"
		profUno      = "p1"
		profDos      = "p2"
	)
	old := strings.Repeat("a", 32)
	seedEpoch(store, clinicaNorte, profUno, old)
	seedEpoch(store, clinicaNorte, profDos, old)
	seedEpoch(store, clinicaSur, profUno, old)

	if err := invalidarDisponibilidadDeAgenda(context.Background(), clinicaNorte, profUno); err != nil {
		t.Fatalf("invalidarDisponibilidadDeAgenda() error = %v", err)
	}

	assertEpochRotated(t, store, clinicaNorte, profUno, old)
	assertEpochUnchanged(t, store, clinicaNorte, profDos, old)
	assertEpochUnchanged(t, store, clinicaSur, profUno, old)

	if got := store.setCount(); got != 1 {
		t.Fatalf("Set() calls = %d, want 1", got)
	}
	if got := store.lastSetKey; got != testEpochKey(clinicaNorte, profUno) {
		t.Fatalf("última clave invalidada = %q", got)
	}
}

func TestInvalidarDisponibilidadErrorEsExplicito(t *testing.T) {
	store := newRecordingCacheStore()
	installAvailabilityCache(t, store)

	old := strings.Repeat("b", 32)
	seedEpoch(store, "clinica-norte", "p1", old)
	store.setErr = errors.New("redis caído")

	err := invalidarDisponibilidadDeAgenda(context.Background(), "clinica-norte", "p1")
	if !errors.Is(err, availabilitycache.ErrCacheInvalidation) {
		t.Fatalf("error = %v, want ErrCacheInvalidation", err)
	}
	if !strings.Contains(err.Error(), "redis caído") {
		t.Fatalf("error = %v, want it to preserve the cause", err)
	}
	// Un SET fallido no debe publicar un epoch nuevo.
	assertEpochUnchanged(t, store, "clinica-norte", "p1", old)
}

func TestInvalidarDisponibilidadIgnoraCancelacionDelCliente(t *testing.T) {
	store := newRecordingCacheStore()
	store.requireActiveContext = true
	installAvailabilityCache(t, store)

	old := strings.Repeat("c", 32)
	seedEpoch(store, "clinica-norte", "p1", old)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := invalidarDisponibilidadDeAgenda(ctx, "clinica-norte", "p1"); err != nil {
		t.Fatalf("error = %v; la invalidación debe desacoplarse de la cancelación", err)
	}
	assertEpochRotated(t, store, "clinica-norte", "p1", old)
}
