package availabilitycache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeStore permite probar la política de caché sin Redis.
//
// No intenta imitar a Redis: solo implementa el contrato mínimo de Store y
// permite inyectar fallos de lectura, escritura y SETNX de forma determinista.
type fakeStore struct {
	mu sync.Mutex

	values map[string]string

	getErr   error
	setErr   error
	setNXErr error

	getCalls  int
	setCalls  int
	setNXWins int
}

func newFakeStore() *fakeStore {
	return &fakeStore{values: make(map[string]string)}
}

func (f *fakeStore) Get(_ context.Context, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.getCalls++
	if f.getErr != nil {
		return "", f.getErr
	}
	value, ok := f.values[key]
	if !ok {
		return "", ErrMiss
	}
	return value, nil
}

func (f *fakeStore) Set(_ context.Context, key, value string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.setCalls++
	if f.setErr != nil {
		return f.setErr
	}
	f.values[key] = value
	return nil
}

func (f *fakeStore) SetNX(_ context.Context, key, value string, _ time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.setNXErr != nil {
		return false, f.setNXErr
	}
	if _, ok := f.values[key]; ok {
		return false, nil
	}
	f.values[key] = value
	f.setNXWins++
	return true, nil
}

func (f *fakeStore) failGets(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getErr = err
}

func (f *fakeStore) failSets(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setErr = err
}

func (f *fakeStore) failSetNX(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setNXErr = err
}

func (f *fakeStore) put(key, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[key] = value
}

func (f *fakeStore) remove(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.values, key)
}

func (f *fakeStore) mustValue(t *testing.T, key string) string {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	value, ok := f.values[key]
	if !ok {
		t.Fatalf("key %q not found in fake store", key)
	}
	return value
}

func (f *fakeStore) size() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.values)
}

func (f *fakeStore) calls() (getCalls, setCalls, setNXWins int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getCalls, f.setCalls, f.setNXWins
}

func newTestCache(t *testing.T) (*Cache, *fakeStore) {
	t.Helper()

	store := newFakeStore()
	cache, err := NewCache(store, DefaultConfig())
	if err != nil {
		t.Fatalf("NewCache() error = %v", err)
	}
	return cache, store
}

func testRequest() Request {
	return Request{
		ClinicaID:     "clinica-a",
		ProfesionalID: "prof-1",
		ServicioID:    "serv-1",
		Fecha:         "2026-11-10",
	}
}

func testSnapshot(req Request, hour int) Snapshot {
	start := time.Date(2026, 11, 10, hour, 0, 0, 0, time.UTC)
	return Snapshot{
		SchemaVersion:   SnapshotSchemaVersion,
		ClinicaID:       req.ClinicaID,
		ProfesionalID:   req.ProfesionalID,
		ServicioID:      req.ServicioID,
		Fecha:           req.Fecha,
		ZonaHoraria:     "America/Argentina/Buenos_Aires",
		DuracionMinutos: 30,
		Espacios: []Slot{{
			Inicio: start,
			Fin:    start.Add(30 * time.Minute),
		}},
		GeneradoEn: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
	}
}

func countingLoader(snapshot Snapshot) (LoaderFunc, *atomic.Int32) {
	var calls atomic.Int32
	load := func(context.Context, Request) (Snapshot, error) {
		calls.Add(1)
		return snapshot, nil
	}
	return load, &calls
}

func assertSlotHour(t *testing.T, snapshot Snapshot, hour int) {
	t.Helper()

	if len(snapshot.Espacios) != 1 {
		t.Fatalf("got %d slots, want 1", len(snapshot.Espacios))
	}
	want := time.Date(2026, 11, 10, hour, 0, 0, 0, time.UTC)
	if !snapshot.Espacios[0].Inicio.Equal(want) {
		t.Fatalf("slot starts at %v, want %v", snapshot.Espacios[0].Inicio, want)
	}
}

func dataKeyFor(t *testing.T, cache *Cache, store *fakeStore, req Request) string {
	t.Helper()

	epochKey, err := cache.keys.epochKey(req.ClinicaID, req.ProfesionalID)
	if err != nil {
		t.Fatalf("epochKey() error = %v", err)
	}
	epoch := store.mustValue(t, epochKey)

	key, err := cache.keys.dataKey(req, epoch)
	if err != nil {
		t.Fatalf("dataKey() error = %v", err)
	}
	return key
}

func TestNewCache_Validation(t *testing.T) {
	if _, err := NewCache(nil, DefaultConfig()); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewCache(nil) error = %v, want ErrInvalidConfig", err)
	}

	cfg := DefaultConfig()
	cfg.DataTTL = 0
	if _, err := NewCache(newFakeStore(), cfg); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewCache(invalid config) error = %v, want ErrInvalidConfig", err)
	}
}

func TestCache_RejectsInvalidRequest(t *testing.T) {
	cache, store := newTestCache(t)

	req := testRequest()
	req.Fecha = "10/11/2026"
	load, calls := countingLoader(testSnapshot(req, 9))

	if _, err := cache.GetOrLoad(context.Background(), req, load); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("GetOrLoad() error = %v, want ErrInvalidRequest", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("loader called %d times, want 0", calls.Load())
	}
	if store.size() != 0 {
		t.Fatalf("store has %d keys, want 0", store.size())
	}
}

func TestCache_RejectsNilLoader(t *testing.T) {
	cache, _ := newTestCache(t)

	_, err := cache.GetOrLoad(context.Background(), testRequest(), nil)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("GetOrLoad(nil loader) error = %v, want ErrInvalidRequest", err)
	}
}

func TestCache_FirstLoadThenHit(t *testing.T) {
	cache, _ := newTestCache(t)
	req := testRequest()
	load, calls := countingLoader(testSnapshot(req, 9))

	first, err := cache.GetOrLoad(context.Background(), req, load)
	if err != nil {
		t.Fatalf("GetOrLoad(first) error = %v", err)
	}
	if first.Hit {
		t.Fatal("first GetOrLoad() reported a hit, want a miss")
	}
	if first.CacheErr != nil {
		t.Fatalf("first GetOrLoad() CacheErr = %v, want nil", first.CacheErr)
	}
	assertSlotHour(t, first.Snapshot, 9)
	if calls.Load() != 1 {
		t.Fatalf("loader calls = %d, want 1", calls.Load())
	}

	second, err := cache.GetOrLoad(context.Background(), req, load)
	if err != nil {
		t.Fatalf("GetOrLoad(second) error = %v", err)
	}
	if !second.Hit {
		t.Fatal("second GetOrLoad() did not hit the cache")
	}
	if second.CacheErr != nil {
		t.Fatalf("second GetOrLoad() CacheErr = %v, want nil", second.CacheErr)
	}
	assertSlotHour(t, second.Snapshot, 9)
	if calls.Load() != 1 {
		t.Fatalf("loader calls = %d after a hit, want 1", calls.Load())
	}
}

func TestCache_InvalidateForcesReload(t *testing.T) {
	cache, store := newTestCache(t)
	req := testRequest()
	load, calls := countingLoader(testSnapshot(req, 9))

	if _, err := cache.GetOrLoad(context.Background(), req, load); err != nil {
		t.Fatalf("GetOrLoad(first) error = %v", err)
	}
	if _, err := cache.GetOrLoad(context.Background(), req, load); err != nil {
		t.Fatalf("GetOrLoad(second) error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("loader calls = %d, want 1", calls.Load())
	}

	epochKey, err := cache.keys.epochKey(req.ClinicaID, req.ProfesionalID)
	if err != nil {
		t.Fatalf("epochKey() error = %v", err)
	}
	before := store.mustValue(t, epochKey)

	if err := cache.InvalidateAgenda(context.Background(), req.ClinicaID, req.ProfesionalID); err != nil {
		t.Fatalf("InvalidateAgenda() error = %v", err)
	}

	after := store.mustValue(t, epochKey)
	if after == before {
		t.Fatal("epoch token did not change after invalidation")
	}
	if !epochPattern.MatchString(after) {
		t.Fatalf("new epoch %q does not match the expected format", after)
	}

	third, err := cache.GetOrLoad(context.Background(), req, load)
	if err != nil {
		t.Fatalf("GetOrLoad(third) error = %v", err)
	}
	if third.Hit {
		t.Fatal("GetOrLoad() hit an entry from the old epoch")
	}
	if calls.Load() != 2 {
		t.Fatalf("loader calls = %d, want 2", calls.Load())
	}

	fourth, err := cache.GetOrLoad(context.Background(), req, load)
	if err != nil {
		t.Fatalf("GetOrLoad(fourth) error = %v", err)
	}
	if !fourth.Hit {
		t.Fatal("GetOrLoad() missed after the entry was recalculated")
	}
	if calls.Load() != 2 {
		t.Fatalf("loader calls = %d after a hit, want 2", calls.Load())
	}
}

func TestCache_InvalidateRejectsInvalidIDs(t *testing.T) {
	cache, _ := newTestCache(t)

	err := cache.InvalidateAgenda(context.Background(), "", "prof-1")
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("InvalidateAgenda() error = %v, want ErrInvalidRequest", err)
	}
}

func TestCache_InvalidationIsScopedToAgenda(t *testing.T) {
	cache, _ := newTestCache(t)

	base := testRequest()
	requests := []Request{base, base, base}
	requests[1].ProfesionalID = "prof-2"
	requests[2].ClinicaID = "clinica-b"

	loads := make([]LoaderFunc, len(requests))
	calls := make([]*atomic.Int32, len(requests))
	for i, req := range requests {
		loads[i], calls[i] = countingLoader(testSnapshot(req, 9))
		for k := 0; k < 2; k++ {
			if _, err := cache.GetOrLoad(context.Background(), req, loads[i]); err != nil {
				t.Fatalf("GetOrLoad() error = %v", err)
			}
		}
		if calls[i].Load() != 1 {
			t.Fatalf("loader calls for agenda %d = %d, want 1", i, calls[i].Load())
		}
	}

	if err := cache.InvalidateAgenda(context.Background(), base.ClinicaID, base.ProfesionalID); err != nil {
		t.Fatalf("InvalidateAgenda() error = %v", err)
	}

	for i, req := range requests {
		result, err := cache.GetOrLoad(context.Background(), req, loads[i])
		if err != nil {
			t.Fatalf("GetOrLoad() error = %v", err)
		}

		if i == 0 {
			if result.Hit {
				t.Fatal("the invalidated agenda served a stale entry")
			}
			if calls[i].Load() != 2 {
				t.Fatalf("loader calls for the invalidated agenda = %d, want 2", calls[i].Load())
			}
			continue
		}

		if !result.Hit {
			t.Fatalf("agenda %d was invalidated by another agenda", i)
		}
		if calls[i].Load() != 1 {
			t.Fatalf("loader calls for agenda %d = %d, want 1", i, calls[i].Load())
		}
	}
}

// TestCache_ReadStartedBeforeInvalidation verifica dos cosas:
//
//   - una lectura en curso puede devolver datos previos a la invalidación,
//   - pero su escritura queda bajo el epoch viejo y no contamina el nuevo.
func TestCache_ReadStartedBeforeInvalidation(t *testing.T) {
	cache, _ := newTestCache(t)
	req := testRequest()

	oldSnapshot := testSnapshot(req, 9)
	newSnapshot := testSnapshot(req, 10)

	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	load := func(context.Context, Request) (Snapshot, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			return oldSnapshot, nil
		}
		return newSnapshot, nil
	}

	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := cache.GetOrLoad(context.Background(), req, load)
		done <- outcome{result: result, err: err}
	}()

	<-entered

	if err := cache.InvalidateAgenda(context.Background(), req.ClinicaID, req.ProfesionalID); err != nil {
		t.Fatalf("InvalidateAgenda() error = %v", err)
	}
	close(release)

	first := <-done
	if first.err != nil {
		t.Fatalf("in-flight GetOrLoad() error = %v", first.err)
	}
	if first.result.Hit {
		t.Fatal("in-flight GetOrLoad() reported a hit, want a miss")
	}
	assertSlotHour(t, first.result.Snapshot, 9)

	second, err := cache.GetOrLoad(context.Background(), req, load)
	if err != nil {
		t.Fatalf("GetOrLoad(after invalidation) error = %v", err)
	}
	if second.Hit {
		t.Fatal("the write from the old epoch was served after invalidation")
	}
	assertSlotHour(t, second.Snapshot, 10)

	third, err := cache.GetOrLoad(context.Background(), req, load)
	if err != nil {
		t.Fatalf("GetOrLoad(second read after invalidation) error = %v", err)
	}
	if !third.Hit {
		t.Fatal("GetOrLoad() missed after recalculating under the new epoch")
	}
	assertSlotHour(t, third.Snapshot, 10)
}

func TestCache_ConcurrentEpochInitialization(t *testing.T) {
	cache, store := newTestCache(t)
	req := testRequest()
	load, calls := countingLoader(testSnapshot(req, 9))

	const goroutines = 20
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			result, err := cache.GetOrLoad(context.Background(), req, load)
			if err != nil {
				errs <- err
				return
			}
			if len(result.Snapshot.Espacios) != 1 {
				errs <- fmt.Errorf("got %d slots, want 1", len(result.Snapshot.Espacios))
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent GetOrLoad() error = %v", err)
	}

	if calls.Load() < 1 {
		t.Fatalf("loader calls = %d, want at least 1", calls.Load())
	}

	_, _, setNXWins := store.calls()
	if setNXWins != 1 {
		t.Fatalf("SETNX created the epoch %d times, want exactly 1", setNXWins)
	}

	epochKey, err := cache.keys.epochKey(req.ClinicaID, req.ProfesionalID)
	if err != nil {
		t.Fatalf("epochKey() error = %v", err)
	}
	epoch := store.mustValue(t, epochKey)
	if !epochPattern.MatchString(epoch) {
		t.Fatalf("epoch %q does not match the expected format", epoch)
	}

	// Todos los lectores deben haber usado el mismo epoch, por lo que el
	// store contiene exactamente el epoch y una entrada de datos.
	if got := store.size(); got != 2 {
		t.Fatalf("store has %d keys, want 2 (epoch plus one data key)", got)
	}

	afterCalls := calls.Load()
	hit, err := cache.GetOrLoad(context.Background(), req, load)
	if err != nil {
		t.Fatalf("GetOrLoad(after concurrency) error = %v", err)
	}
	if !hit.Hit {
		t.Fatal("GetOrLoad() missed after concurrent writers populated the cache")
	}
	if calls.Load() != afterCalls {
		t.Fatalf("loader calls = %d after a hit, want %d", calls.Load(), afterCalls)
	}
}

func TestCache_EpochLossReinitializes(t *testing.T) {
	cache, store := newTestCache(t)
	req := testRequest()
	load, calls := countingLoader(testSnapshot(req, 9))

	if _, err := cache.GetOrLoad(context.Background(), req, load); err != nil {
		t.Fatalf("GetOrLoad(first) error = %v", err)
	}
	if _, err := cache.GetOrLoad(context.Background(), req, load); err != nil {
		t.Fatalf("GetOrLoad(second) error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("loader calls = %d, want 1", calls.Load())
	}

	epochKey, err := cache.keys.epochKey(req.ClinicaID, req.ProfesionalID)
	if err != nil {
		t.Fatalf("epochKey() error = %v", err)
	}
	store.remove(epochKey)

	afterLoss, err := cache.GetOrLoad(context.Background(), req, load)
	if err != nil {
		t.Fatalf("GetOrLoad(after epoch loss) error = %v", err)
	}
	if afterLoss.Hit {
		t.Fatal("GetOrLoad() served data after the epoch was lost")
	}
	if calls.Load() != 2 {
		t.Fatalf("loader calls = %d, want 2", calls.Load())
	}
	if !epochPattern.MatchString(store.mustValue(t, epochKey)) {
		t.Fatal("epoch was not reinitialized with a valid token")
	}
}

func TestCache_CorruptEpochIsReplaced(t *testing.T) {
	cache, store := newTestCache(t)
	req := testRequest()
	load, calls := countingLoader(testSnapshot(req, 9))

	epochKey, err := cache.keys.epochKey(req.ClinicaID, req.ProfesionalID)
	if err != nil {
		t.Fatalf("epochKey() error = %v", err)
	}
	store.put(epochKey, "not-a-valid-token")

	result, err := cache.GetOrLoad(context.Background(), req, load)
	if err != nil {
		t.Fatalf("GetOrLoad() error = %v", err)
	}
	if result.Hit {
		t.Fatal("GetOrLoad() served data for a corrupt epoch")
	}
	if calls.Load() != 1 {
		t.Fatalf("loader calls = %d, want 1", calls.Load())
	}
	if !epochPattern.MatchString(store.mustValue(t, epochKey)) {
		t.Fatal("corrupt epoch was not replaced")
	}
}

func TestCache_RedisReadFailureFallsBackToLoader(t *testing.T) {
	cache, store := newTestCache(t)
	req := testRequest()
	load, calls := countingLoader(testSnapshot(req, 9))

	store.failGets(errors.New("redis unavailable"))

	result, err := cache.GetOrLoad(context.Background(), req, load)
	if err != nil {
		t.Fatalf("GetOrLoad() error = %v, want a fallback result", err)
	}
	if result.Hit {
		t.Fatal("GetOrLoad() reported a hit while Redis reads were failing")
	}
	if !errors.Is(result.CacheErr, ErrCacheRead) {
		t.Fatalf("CacheErr = %v, want ErrCacheRead", result.CacheErr)
	}
	assertSlotHour(t, result.Snapshot, 9)
	if calls.Load() != 1 {
		t.Fatalf("loader calls = %d, want 1", calls.Load())
	}
}

func TestCache_RedisWriteFailureReturnsLoaderResult(t *testing.T) {
	cache, store := newTestCache(t)
	req := testRequest()
	load, calls := countingLoader(testSnapshot(req, 9))

	store.failSets(errors.New("redis write unavailable"))

	first, err := cache.GetOrLoad(context.Background(), req, load)
	if err != nil {
		t.Fatalf("GetOrLoad(first) error = %v", err)
	}
	if first.Hit {
		t.Fatal("GetOrLoad() reported a hit while Redis writes were failing")
	}
	if !errors.Is(first.CacheErr, ErrCacheWrite) {
		t.Fatalf("CacheErr = %v, want ErrCacheWrite", first.CacheErr)
	}
	assertSlotHour(t, first.Snapshot, 9)

	second, err := cache.GetOrLoad(context.Background(), req, load)
	if err != nil {
		t.Fatalf("GetOrLoad(second) error = %v", err)
	}
	if second.Hit {
		t.Fatal("GetOrLoad() served an entry that was never written")
	}
	if calls.Load() != 2 {
		t.Fatalf("loader calls = %d, want 2", calls.Load())
	}
}

func TestCache_InvalidationFailureIsExplicit(t *testing.T) {
	cache, store := newTestCache(t)
	req := testRequest()
	load, _ := countingLoader(testSnapshot(req, 9))

	if _, err := cache.GetOrLoad(context.Background(), req, load); err != nil {
		t.Fatalf("GetOrLoad() error = %v", err)
	}

	epochKey, err := cache.keys.epochKey(req.ClinicaID, req.ProfesionalID)
	if err != nil {
		t.Fatalf("epochKey() error = %v", err)
	}
	before := store.mustValue(t, epochKey)

	store.failSets(errors.New("redis write unavailable"))

	err = cache.InvalidateAgenda(context.Background(), req.ClinicaID, req.ProfesionalID)
	if !errors.Is(err, ErrCacheInvalidation) {
		t.Fatalf("InvalidateAgenda() error = %v, want ErrCacheInvalidation", err)
	}
	if after := store.mustValue(t, epochKey); after != before {
		t.Fatal("epoch changed even though invalidation reported a failure")
	}
}

func TestCache_InvalidPayloadFromAnotherClinicIsRejected(t *testing.T) {
	cache, store := newTestCache(t)
	req := testRequest()
	load, calls := countingLoader(testSnapshot(req, 9))

	if _, err := cache.GetOrLoad(context.Background(), req, load); err != nil {
		t.Fatalf("GetOrLoad(first) error = %v", err)
	}

	key := dataKeyFor(t, cache, store, req)

	wrong := testSnapshot(req, 9)
	wrong.ClinicaID = "clinica-b"
	raw, err := encodeSnapshot(wrong)
	if err != nil {
		t.Fatalf("encodeSnapshot() error = %v", err)
	}
	store.put(key, raw)

	result, err := cache.GetOrLoad(context.Background(), req, load)
	if err != nil {
		t.Fatalf("GetOrLoad(corrupt) error = %v", err)
	}
	if result.Hit {
		t.Fatal("GetOrLoad() served a snapshot from another clinic")
	}
	if !errors.Is(result.CacheErr, ErrInvalidPayload) {
		t.Fatalf("CacheErr = %v, want ErrInvalidPayload", result.CacheErr)
	}
	if calls.Load() != 2 {
		t.Fatalf("loader calls = %d, want 2", calls.Load())
	}
	assertSlotHour(t, result.Snapshot, 9)

	// El valor inválido debe haber sido reemplazado por uno correcto.
	third, err := cache.GetOrLoad(context.Background(), req, load)
	if err != nil {
		t.Fatalf("GetOrLoad(after repair) error = %v", err)
	}
	if !third.Hit {
		t.Fatal("GetOrLoad() missed after the corrupt entry was replaced")
	}
	if calls.Load() != 2 {
		t.Fatalf("loader calls = %d after a hit, want 2", calls.Load())
	}
}

func TestCache_InvalidJSONIsRejected(t *testing.T) {
	cache, store := newTestCache(t)
	req := testRequest()
	load, calls := countingLoader(testSnapshot(req, 9))

	if _, err := cache.GetOrLoad(context.Background(), req, load); err != nil {
		t.Fatalf("GetOrLoad(first) error = %v", err)
	}
	store.put(dataKeyFor(t, cache, store, req), "{not-json")

	result, err := cache.GetOrLoad(context.Background(), req, load)
	if err != nil {
		t.Fatalf("GetOrLoad() error = %v", err)
	}
	if result.Hit {
		t.Fatal("GetOrLoad() served an entry with invalid JSON")
	}
	if !errors.Is(result.CacheErr, ErrInvalidPayload) {
		t.Fatalf("CacheErr = %v, want ErrInvalidPayload", result.CacheErr)
	}
	if calls.Load() != 2 {
		t.Fatalf("loader calls = %d, want 2", calls.Load())
	}
}

func TestCache_LoaderErrorsAreFatal(t *testing.T) {
	cache, _ := newTestCache(t)
	req := testRequest()
	loadErr := errors.New("postgres unavailable")

	_, err := cache.GetOrLoad(context.Background(), req, func(context.Context, Request) (Snapshot, error) {
		return Snapshot{}, loadErr
	})
	if !errors.Is(err, loadErr) {
		t.Fatalf("GetOrLoad() error = %v, want the loader error", err)
	}
}

func TestCache_LoaderSnapshotIsValidated(t *testing.T) {
	cache, store := newTestCache(t)
	req := testRequest()

	wrong := testSnapshot(req, 9)
	wrong.ClinicaID = "clinica-b"

	_, err := cache.GetOrLoad(context.Background(), req, func(context.Context, Request) (Snapshot, error) {
		return wrong, nil
	})
	if !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("GetOrLoad() error = %v, want ErrInvalidPayload", err)
	}

	// Solo debe existir la clave del epoch, nunca una entrada de datos.
	if got := store.size(); got != 1 {
		t.Fatalf("store has %d keys, want only the epoch key", got)
	}
}

func TestCache_DoesNotMutateLoaderSnapshot(t *testing.T) {
	cache, _ := newTestCache(t)
	req := testRequest()

	original := testSnapshot(req, 9)
	loc := time.FixedZone("UTC-3", -3*60*60)
	original.Espacios[0].Inicio = original.Espacios[0].Inicio.In(loc)
	original.Espacios[0].Fin = original.Espacios[0].Fin.In(loc)
	originalStart := original.Espacios[0].Inicio

	load := func(context.Context, Request) (Snapshot, error) {
		return original, nil
	}
	if _, err := cache.GetOrLoad(context.Background(), req, load); err != nil {
		t.Fatalf("GetOrLoad() error = %v", err)
	}

	got := original.Espacios[0].Inicio
	if !got.Equal(originalStart) || got.Location() != loc {
		t.Fatalf("loader snapshot was mutated: got %v in %v", got, got.Location())
	}
}
