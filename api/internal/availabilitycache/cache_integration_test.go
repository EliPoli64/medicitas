//go:build integration

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

// newIntegrationCache crea una caché contra el Redis real y registra la
// limpieza de un prefijo exclusivo para la prueba. No usa FLUSHDB.
func newIntegrationCache(t *testing.T) (*Cache, *RedisStore, string) {
	t.Helper()

	store := newTestRedisStore(t)
	prefix := fmt.Sprintf("medicitas-it-%d", time.Now().UnixNano())
	cleanupPrefix(t, store, prefix)

	cfg := DefaultConfig()
	cfg.Prefix = prefix
	cfg.OpTimeout = 500 * time.Millisecond

	cache, err := NewCache(store, cfg, WithVersionResolver(nopResolver{}))
	if err != nil {
		t.Fatalf("NewCache() error = %v", err)
	}
	return cache, store, prefix
}

func integrationContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 15*time.Second)
}

func scanKeys(t *testing.T, store *RedisStore, match string) []string {
	t.Helper()

	ctx, cancel := integrationContext(t)
	defer cancel()

	var keys []string
	var cursor uint64
	for {
		batch, next, err := store.client.Scan(ctx, cursor, match, 100).Result()
		if err != nil {
			t.Fatalf("SCAN %q error = %v", match, err)
		}
		keys = append(keys, batch...)
		cursor = next
		if cursor == 0 {
			return keys
		}
	}
}

func TestCacheIntegration_FirstLoadThenHit(t *testing.T) {
	cache, store, _ := newIntegrationCache(t)
	ctx, cancel := integrationContext(t)
	defer cancel()

	req := testRequest()
	load, calls := countingLoader(testSnapshot(req, 9))

	first, err := cache.GetOrLoad(ctx, req, load)
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

	second, err := cache.GetOrLoad(ctx, req, load)
	if err != nil {
		t.Fatalf("GetOrLoad(second) error = %v", err)
	}
	if !second.Hit {
		t.Fatal("second GetOrLoad() did not hit the cache")
	}
	if calls.Load() != 1 {
		t.Fatalf("loader calls = %d, want 1", calls.Load())
	}
	assertSlotHour(t, second.Snapshot, 9)

	epochKey, err := cache.keys.epochKey(req.ClinicaID, req.ProfesionalID)
	if err != nil {
		t.Fatalf("epochKey() error = %v", err)
	}
	epoch, err := store.Get(ctx, epochKey)
	if err != nil {
		t.Fatalf("Get(epoch) error = %v", err)
	}
	dataKey, err := cache.keys.dataKey(req, epoch)
	if err != nil {
		t.Fatalf("dataKey() error = %v", err)
	}

	dataTTL, err := store.client.TTL(ctx, dataKey).Result()
	if err != nil {
		t.Fatalf("TTL(data) error = %v", err)
	}
	if dataTTL <= 0 || dataTTL > DefaultDataTTL {
		t.Fatalf("data TTL = %v, want (0, %v]", dataTTL, DefaultDataTTL)
	}

	epochTTL, err := store.client.TTL(ctx, epochKey).Result()
	if err != nil {
		t.Fatalf("TTL(epoch) error = %v", err)
	}
	if epochTTL <= 0 || epochTTL > DefaultEpochTTL {
		t.Fatalf("epoch TTL = %v, want (0, %v]", epochTTL, DefaultEpochTTL)
	}
}

func TestCacheIntegration_InvalidateAgendaRecalculates(t *testing.T) {
	cache, store, _ := newIntegrationCache(t)
	ctx, cancel := integrationContext(t)
	defer cancel()

	req := testRequest()
	load, calls := countingLoader(testSnapshot(req, 9))

	if _, err := cache.GetOrLoad(ctx, req, load); err != nil {
		t.Fatalf("GetOrLoad(first) error = %v", err)
	}
	if _, err := cache.GetOrLoad(ctx, req, load); err != nil {
		t.Fatalf("GetOrLoad(second) error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("loader calls = %d, want 1", calls.Load())
	}

	epochKey, err := cache.keys.epochKey(req.ClinicaID, req.ProfesionalID)
	if err != nil {
		t.Fatalf("epochKey() error = %v", err)
	}
	before, err := store.Get(ctx, epochKey)
	if err != nil {
		t.Fatalf("Get(epoch) error = %v", err)
	}

	if err := cache.InvalidateAgenda(ctx, req.ClinicaID, req.ProfesionalID); err != nil {
		t.Fatalf("InvalidateAgenda() error = %v", err)
	}

	after, err := store.Get(ctx, epochKey)
	if err != nil {
		t.Fatalf("Get(epoch after invalidation) error = %v", err)
	}
	if after == before {
		t.Fatal("epoch token did not change after invalidation")
	}

	third, err := cache.GetOrLoad(ctx, req, load)
	if err != nil {
		t.Fatalf("GetOrLoad(third) error = %v", err)
	}
	if third.Hit {
		t.Fatal("GetOrLoad() hit an entry from the old epoch")
	}
	if calls.Load() != 2 {
		t.Fatalf("loader calls = %d, want 2", calls.Load())
	}
}

func TestCacheIntegration_ConcurrentEpochInitialization(t *testing.T) {
	cache, store, prefix := newIntegrationCache(t)
	ctx, cancel := integrationContext(t)
	defer cancel()

	req := testRequest()
	load, calls := countingLoader(testSnapshot(req, 9))

	const goroutines = 10
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			result, err := cache.GetOrLoad(ctx, req, load)
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

	epochKey, err := cache.keys.epochKey(req.ClinicaID, req.ProfesionalID)
	if err != nil {
		t.Fatalf("epochKey() error = %v", err)
	}
	epoch, err := store.Get(ctx, epochKey)
	if err != nil {
		t.Fatalf("Get(epoch) error = %v", err)
	}
	if !epochPattern.MatchString(epoch) {
		t.Fatalf("epoch %q does not match the expected format", epoch)
	}

	epochKeys := scanKeys(t, store, prefix+":avail:"+cache.keys.formatVersion+":epoch:*")
	if len(epochKeys) != 1 {
		t.Fatalf("found %d epoch keys, want 1", len(epochKeys))
	}

	dataKeys := scanKeys(t, store, prefix+":avail:"+cache.keys.formatVersion+":data:*")
	if len(dataKeys) != 1 {
		t.Fatalf("found %d data keys, want 1 under the winning epoch", len(dataKeys))
	}
	dataKey, err := cache.keys.dataKey(req, epoch)
	if err != nil {
		t.Fatalf("dataKey() error = %v", err)
	}
	if dataKeys[0] != dataKey {
		t.Fatalf("data key = %q, want %q", dataKeys[0], dataKey)
	}

	afterCalls := calls.Load()
	hit, err := cache.GetOrLoad(ctx, req, load)
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

func TestCacheIntegration_EpochExpirationReinitializes(t *testing.T) {
	cache, store, _ := newIntegrationCache(t)
	ctx, cancel := integrationContext(t)
	defer cancel()

	req := testRequest()
	load, calls := countingLoader(testSnapshot(req, 9))

	if _, err := cache.GetOrLoad(ctx, req, load); err != nil {
		t.Fatalf("GetOrLoad(first) error = %v", err)
	}
	if _, err := cache.GetOrLoad(ctx, req, load); err != nil {
		t.Fatalf("GetOrLoad(second) error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("loader calls = %d, want 1", calls.Load())
	}

	epochKey, err := cache.keys.epochKey(req.ClinicaID, req.ProfesionalID)
	if err != nil {
		t.Fatalf("epochKey() error = %v", err)
	}
	if err := store.client.Del(ctx, epochKey).Err(); err != nil {
		t.Fatalf("DEL(epoch) error = %v", err)
	}

	afterLoss, err := cache.GetOrLoad(ctx, req, load)
	if err != nil {
		t.Fatalf("GetOrLoad(after expiration) error = %v", err)
	}
	if afterLoss.Hit {
		t.Fatal("GetOrLoad() served data after the epoch expired")
	}
	if calls.Load() != 2 {
		t.Fatalf("loader calls = %d, want 2", calls.Load())
	}

	epoch, err := store.Get(ctx, epochKey)
	if err != nil {
		t.Fatalf("Get(reinitialized epoch) error = %v", err)
	}
	if !epochPattern.MatchString(epoch) {
		t.Fatalf("reinitialized epoch %q does not match the expected format", epoch)
	}
}

// TestCacheIntegration_ReadStartedBeforeInvalidation reproduce el escenario en
// el que una lectura obtiene el epoch viejo, la agenda se invalida mientras el
// Loader trabaja y la lectura termina después.
func TestCacheIntegration_ReadStartedBeforeInvalidation(t *testing.T) {
	cache, store, _ := newIntegrationCache(t)
	ctx, cancel := integrationContext(t)
	defer cancel()

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
		result, err := cache.GetOrLoad(ctx, req, load)
		done <- outcome{result: result, err: err}
	}()

	<-entered

	epochKey, err := cache.keys.epochKey(req.ClinicaID, req.ProfesionalID)
	if err != nil {
		t.Fatalf("epochKey() error = %v", err)
	}
	oldEpoch, err := store.Get(ctx, epochKey)
	if err != nil {
		t.Fatalf("Get(old epoch) error = %v", err)
	}

	if err := cache.InvalidateAgenda(ctx, req.ClinicaID, req.ProfesionalID); err != nil {
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

	newEpoch, err := store.Get(ctx, epochKey)
	if err != nil {
		t.Fatalf("Get(new epoch) error = %v", err)
	}
	if newEpoch == oldEpoch {
		t.Fatal("epoch did not change after invalidation")
	}

	oldDataKey, err := cache.keys.dataKey(req, oldEpoch)
	if err != nil {
		t.Fatalf("dataKey(old epoch) error = %v", err)
	}
	if _, err := store.Get(ctx, oldDataKey); err != nil {
		t.Fatalf("in-flight read did not write under the old epoch: %v", err)
	}

	second, err := cache.GetOrLoad(ctx, req, load)
	if err != nil {
		t.Fatalf("GetOrLoad(after invalidation) error = %v", err)
	}
	if second.Hit {
		t.Fatal("the write from the old epoch was served after invalidation")
	}
	assertSlotHour(t, second.Snapshot, 10)

	third, err := cache.GetOrLoad(ctx, req, load)
	if err != nil {
		t.Fatalf("GetOrLoad(second read after invalidation) error = %v", err)
	}
	if !third.Hit {
		t.Fatal("GetOrLoad() missed after recalculating under the new epoch")
	}
	assertSlotHour(t, third.Snapshot, 10)

	// La entrada vieja sigue existiendo, pero la clave vigente es distinta:
	// el epoch nuevo la volvió inalcanzable sin borrarla.
	newDataKey, err := cache.keys.dataKey(req, newEpoch)
	if err != nil {
		t.Fatalf("dataKey(new epoch) error = %v", err)
	}
	if oldDataKey == newDataKey {
		t.Fatal("data keys from different epochs must not collide")
	}
	if _, err := store.Get(ctx, newDataKey); err != nil {
		t.Fatalf("Get(new data key) error = %v", err)
	}
}

// TestCacheIntegration_EpochExpiresDuringLoad cubre la carrera entre la
// pérdida del epoch y una lectura en curso: la escritura de esa lectura queda
// huérfana bajo el epoch viejo y no se sirve con el epoch nuevo.
func TestCacheIntegration_EpochExpiresDuringLoad(t *testing.T) {
	cache, store, _ := newIntegrationCache(t)
	ctx, cancel := integrationContext(t)
	defer cancel()

	req := testRequest()

	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	load := func(context.Context, Request) (Snapshot, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return testSnapshot(req, 9), nil
	}

	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := cache.GetOrLoad(ctx, req, load)
		done <- outcome{result: result, err: err}
	}()

	<-entered

	epochKey, err := cache.keys.epochKey(req.ClinicaID, req.ProfesionalID)
	if err != nil {
		t.Fatalf("epochKey() error = %v", err)
	}
	oldEpoch, err := store.Get(ctx, epochKey)
	if err != nil {
		t.Fatalf("Get(old epoch) error = %v", err)
	}
	if err := store.client.Del(ctx, epochKey).Err(); err != nil {
		t.Fatalf("DEL(epoch) error = %v", err)
	}

	close(release)
	first := <-done
	if first.err != nil {
		t.Fatalf("in-flight GetOrLoad() error = %v", first.err)
	}
	if first.result.Hit {
		t.Fatal("in-flight GetOrLoad() reported a hit, want a miss")
	}

	oldDataKey, err := cache.keys.dataKey(req, oldEpoch)
	if err != nil {
		t.Fatalf("dataKey(old epoch) error = %v", err)
	}
	if _, err := store.Get(ctx, oldDataKey); err != nil {
		t.Fatalf("in-flight read did not write under the old epoch: %v", err)
	}

	second, err := cache.GetOrLoad(ctx, req, load)
	if err != nil {
		t.Fatalf("GetOrLoad(after epoch loss) error = %v", err)
	}
	if second.Hit {
		t.Fatal("GetOrLoad() served data after the epoch was lost")
	}

	newEpoch, err := store.Get(ctx, epochKey)
	if err != nil {
		t.Fatalf("Get(new epoch) error = %v", err)
	}
	if newEpoch == oldEpoch {
		t.Fatal("epoch was not regenerated")
	}
	newDataKey, err := cache.keys.dataKey(req, newEpoch)
	if err != nil {
		t.Fatalf("dataKey(new epoch) error = %v", err)
	}
	if newDataKey == oldDataKey {
		t.Fatal("data keys from different epochs must not collide")
	}

	third, err := cache.GetOrLoad(ctx, req, load)
	if err != nil {
		t.Fatalf("GetOrLoad(after regeneration) error = %v", err)
	}
	if !third.Hit {
		t.Fatal("GetOrLoad() missed after recalculating under the new epoch")
	}
}

func TestCacheIntegration_InvalidPayloadFromAnotherClinicIsRejected(t *testing.T) {
	cache, store, _ := newIntegrationCache(t)
	ctx, cancel := integrationContext(t)
	defer cancel()

	req := testRequest()
	load, calls := countingLoader(testSnapshot(req, 9))

	if _, err := cache.GetOrLoad(ctx, req, load); err != nil {
		t.Fatalf("GetOrLoad(first) error = %v", err)
	}

	epochKey, err := cache.keys.epochKey(req.ClinicaID, req.ProfesionalID)
	if err != nil {
		t.Fatalf("epochKey() error = %v", err)
	}
	epoch, err := store.Get(ctx, epochKey)
	if err != nil {
		t.Fatalf("Get(epoch) error = %v", err)
	}
	key, err := cache.keys.dataKey(req, epoch)
	if err != nil {
		t.Fatalf("dataKey() error = %v", err)
	}

	wrong := testSnapshot(req, 9)
	wrong.ClinicaID = "clinica-b"
	raw, err := encodeSnapshot(wrong)
	if err != nil {
		t.Fatalf("encodeSnapshot() error = %v", err)
	}
	if err := store.Set(ctx, key, raw, DefaultDataTTL); err != nil {
		t.Fatalf("Set(wrong snapshot) error = %v", err)
	}

	result, err := cache.GetOrLoad(ctx, req, load)
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

	third, err := cache.GetOrLoad(ctx, req, load)
	if err != nil {
		t.Fatalf("GetOrLoad(after repair) error = %v", err)
	}
	if !third.Hit {
		t.Fatal("GetOrLoad() missed after the corrupt entry was replaced")
	}
}

// failingSetStore delega en Redis real salvo las escrituras, que fallan de
// forma determinista. Permite probar el camino de error sin apagar Redis.
type failingSetStore struct {
	*RedisStore
	err error
}

func (s failingSetStore) Set(context.Context, string, string, time.Duration) error {
	return s.err
}

func TestCacheIntegration_WriteFailureDoesNotCorruptState(t *testing.T) {
	store := newTestRedisStore(t)
	prefix := fmt.Sprintf("medicitas-it-%d", time.Now().UnixNano())
	cleanupPrefix(t, store, prefix)

	cfg := DefaultConfig()
	cfg.Prefix = prefix
	cfg.OpTimeout = 500 * time.Millisecond

	broken := failingSetStore{RedisStore: store, err: errors.New("redis write down")}
	cache, err := NewCache(broken, cfg)
	if err != nil {
		t.Fatalf("NewCache() error = %v", err)
	}

	ctx, cancel := integrationContext(t)
	defer cancel()

	req := testRequest()
	load, calls := countingLoader(testSnapshot(req, 9))

	first, err := cache.GetOrLoad(ctx, req, load)
	if err != nil {
		t.Fatalf("GetOrLoad(first) error = %v", err)
	}
	if first.Hit {
		t.Fatal("GetOrLoad() reported a hit while writes were failing")
	}
	if !errors.Is(first.CacheErr, ErrCacheWrite) {
		t.Fatalf("CacheErr = %v, want ErrCacheWrite", first.CacheErr)
	}
	assertSlotHour(t, first.Snapshot, 9)

	second, err := cache.GetOrLoad(ctx, req, load)
	if err != nil {
		t.Fatalf("GetOrLoad(second) error = %v", err)
	}
	if second.Hit {
		t.Fatal("GetOrLoad() served an entry that was never written")
	}
	if calls.Load() != 2 {
		t.Fatalf("loader calls = %d, want 2", calls.Load())
	}

	epochKey, err := cache.keys.epochKey(req.ClinicaID, req.ProfesionalID)
	if err != nil {
		t.Fatalf("epochKey() error = %v", err)
	}
	before, err := store.Get(ctx, epochKey)
	if err != nil {
		t.Fatalf("Get(epoch) error = %v", err)
	}

	err = cache.InvalidateAgenda(ctx, req.ClinicaID, req.ProfesionalID)
	if !errors.Is(err, ErrCacheInvalidation) {
		t.Fatalf("InvalidateAgenda() error = %v, want ErrCacheInvalidation", err)
	}

	after, err := store.Get(ctx, epochKey)
	if err != nil {
		t.Fatalf("Get(epoch after failed invalidation) error = %v", err)
	}
	if after != before {
		t.Fatal("epoch changed even though invalidation reported a failure")
	}
}

func TestCacheIntegration_ReadFailureFallsBackToLoader(t *testing.T) {
	bad, err := NewRedisStore(RedisConfig{
		Addr:         "127.0.0.1:1",
		DB:           15,
		DialTimeout:  20 * time.Millisecond,
		ReadTimeout:  20 * time.Millisecond,
		WriteTimeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewRedisStore() error = %v", err)
	}
	t.Cleanup(func() {
		_ = bad.Close()
	})

	cfg := DefaultConfig()
	cfg.OpTimeout = 200 * time.Millisecond
	cache, err := NewCache(bad, cfg)
	if err != nil {
		t.Fatalf("NewCache() error = %v", err)
	}

	ctx, cancel := integrationContext(t)
	defer cancel()

	req := testRequest()
	load, calls := countingLoader(testSnapshot(req, 9))

	result, err := cache.GetOrLoad(ctx, req, load)
	if err != nil {
		t.Fatalf("GetOrLoad() error = %v, want a fallback result", err)
	}
	if result.Hit {
		t.Fatal("GetOrLoad() reported a hit while Redis was unreachable")
	}
	if !errors.Is(result.CacheErr, ErrCacheRead) {
		t.Fatalf("CacheErr = %v, want ErrCacheRead", result.CacheErr)
	}
	if calls.Load() != 1 {
		t.Fatalf("loader calls = %d, want 1", calls.Load())
	}
	assertSlotHour(t, result.Snapshot, 9)
}
