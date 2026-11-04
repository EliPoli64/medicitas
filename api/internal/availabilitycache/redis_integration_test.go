//go:build integration

package availabilitycache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestRedisStore_Connection(t *testing.T) {
	store := newTestRedisStore(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
}

func TestRedisStore_SetGetSetNX(t *testing.T) {
	store := newTestRedisStore(t)
	prefix := fmt.Sprintf("medicitas-test-%d", time.Now().UnixNano())
	cleanupPrefix(t, store, prefix)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	key := prefix + ":key"
	if err := store.Set(ctx, key, "valor", 30*time.Second); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	got, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got != "valor" {
		t.Fatalf("Get() = %q, want %q", got, "valor")
	}

	ttl, err := store.client.TTL(ctx, key).Result()
	if err != nil {
		t.Fatalf("TTL() error = %v", err)
	}
	if ttl <= 0 {
		t.Fatalf("TTL() = %v, want a positive TTL", ttl)
	}

	if _, err := store.Get(ctx, prefix+":missing"); !errors.Is(err, ErrMiss) {
		t.Fatalf("Get(missing) error = %v, want ErrMiss", err)
	}

	nxKey := prefix + ":nx"
	created, err := store.SetNX(ctx, nxKey, "primero", time.Minute)
	if err != nil {
		t.Fatalf("SetNX(first) error = %v", err)
	}
	if !created {
		t.Fatal("SetNX(first) = false, want true")
	}

	created, err = store.SetNX(ctx, nxKey, "segundo", time.Minute)
	if err != nil {
		t.Fatalf("SetNX(second) error = %v", err)
	}
	if created {
		t.Fatal("SetNX(second) = true, want false")
	}

	got, err = store.Get(ctx, nxKey)
	if err != nil {
		t.Fatalf("Get(nx) error = %v", err)
	}
	if got != "primero" {
		t.Fatalf("Get(nx) = %q, want %q", got, "primero")
	}
}

func newTestRedisStore(t *testing.T) *RedisStore {
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
			t.Fatalf("REDIS_DB = %q is not a number", raw)
		}
		db = parsed
	}

	store, err := NewRedisStore(RedisConfig{
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.Ping(ctx); err != nil {
		_ = store.Close()
		t.Fatalf("Redis no disponible en %s (DB %d): %v", addr, db, err)
	}

	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
}

// cleanupPrefix borra solo las claves creadas por una prueba. No usa FLUSHDB.
func cleanupPrefix(t *testing.T, store *RedisStore, prefix string) {
	t.Helper()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var cursor uint64
		for {
			keys, next, err := store.client.Scan(ctx, cursor, prefix+"*", 100).Result()
			if err != nil {
				t.Logf("cleanup scan error: %v", err)
				return
			}
			if len(keys) > 0 {
				if err := store.client.Del(ctx, keys...).Err(); err != nil {
					t.Logf("cleanup delete error: %v", err)
					return
				}
			}
			cursor = next
			if cursor == 0 {
				return
			}
		}
	})
}
