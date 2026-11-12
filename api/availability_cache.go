package main

// Inicialización de la caché de disponibilidad en la API.
//
// La caché es una optimización: PostgreSQL sigue siendo la fuente de verdad.
// Por eso el arranque es tolerante a que Redis no responda (se deshabilita la
// caché y la API sigue funcionando), pero no tolerante a una configuración
// inválida (un puerto mal escrito es un error de despliegue y debe corregirse).
//
// Los handlers de RF7 consultan `availabilityCache` cuando está disponible;
// si es nil (por ejemplo, porque Redis no respondió al arranque), calculan
// desde PostgreSQL directamente.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"medicitas/api/internal/availabilitycache"
	"medicitas/api/internal/availabilityversion"
)

const (
	redisDefaultHost  = "localhost"
	redisDefaultPort  = "6379"
	redisDialTimeout  = 1 * time.Second
	redisReadTimeout  = 1 * time.Second
	redisWriteTimeout = 1 * time.Second
	redisPingTimeout  = 2 * time.Second
)

// availabilityCache queda disponible para los handlers de RF7/RF8 en una etapa
// posterior. Mientras sea nil, la API debe calcular desde PostgreSQL.
var availabilityCache *availabilitycache.Cache

// availabilityCacheRuntime agrupa la caché con el store que la respalda para
// poder cerrar el pool de conexiones al terminar.
type availabilityCacheRuntime struct {
	Cache *availabilitycache.Cache
	Addr  string
	DB    int

	store *availabilitycache.RedisStore
}

// Close libera las conexiones de Redis. Es seguro llamarlo sobre un runtime
// nil.
func (r *availabilityCacheRuntime) Close() error {
	if r == nil || r.store == nil {
		return nil
	}
	return r.store.Close()
}

// redisConfigFromEnv construye la configuración de conexión a partir de las
// variables que ya usa docker-compose:
//
//	REDIS_HOST      host de Redis (por defecto localhost)
//	REDIS_PORT      puerto de Redis (por defecto 6379)
//	REDIS_PASSWORD  password opcional
//	REDIS_DB        base lógica (por defecto 0)
//
// Los errores de formato se envuelven con ErrInvalidConfig para que el
// llamador pueda distinguirlos de una caída de Redis.
func redisConfigFromEnv() (availabilitycache.RedisConfig, error) {
	host := strings.TrimSpace(os.Getenv("REDIS_HOST"))
	if host == "" {
		host = redisDefaultHost
	}

	portText := strings.TrimSpace(os.Getenv("REDIS_PORT"))
	if portText == "" {
		portText = redisDefaultPort
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return availabilitycache.RedisConfig{}, fmt.Errorf(
			"%w: REDIS_PORT debe ser un puerto entre 1 y 65535, se recibió %q",
			availabilitycache.ErrInvalidConfig, portText,
		)
	}

	db := 0
	if raw := strings.TrimSpace(os.Getenv("REDIS_DB")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			return availabilitycache.RedisConfig{}, fmt.Errorf(
				"%w: REDIS_DB debe ser un entero no negativo, se recibió %q",
				availabilitycache.ErrInvalidConfig, raw,
			)
		}
		db = parsed
	}

	return availabilitycache.RedisConfig{
		Addr:         net.JoinHostPort(host, strconv.Itoa(port)),
		Password:     os.Getenv("REDIS_PASSWORD"),
		DB:           db,
		DialTimeout:  redisDialTimeout,
		ReadTimeout:  redisReadTimeout,
		WriteTimeout: redisWriteTimeout,
	}, nil
}

// newAvailabilityCacheFromEnv construye RedisStore, comprueba la conexión con
// un Ping acotado y construye Cache. Si algo falla, cierra el store para no
// dejar conexiones abiertas.
//
// Un error de configuración envuelve ErrInvalidConfig. Un error de conexión
// no: el llamador puede decidir degradar la caché y seguir sirviendo desde
// PostgreSQL.
//
// Si db no es nil, se conecta un AgendaVersionResolver sobre PostgreSQL. Eso
// permite que GetOrLoad valide cada cache hit contra la versión autoritativa
// de la agenda.
func newAvailabilityCacheFromEnv(ctx context.Context, db *sql.DB) (*availabilityCacheRuntime, error) {
	cfg, err := redisConfigFromEnv()
	if err != nil {
		return nil, err
	}

	store, err := availabilitycache.NewRedisStore(cfg)
	if err != nil {
		return nil, err
	}

	pingCtx, cancel := context.WithTimeout(ctx, redisPingTimeout)
	defer cancel()
	if err := store.Ping(pingCtx); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("redis no responde en %s: %w", cfg.Addr, err)
	}

	cacheOpts := []availabilitycache.CacheOption{}
	if db != nil {
		cacheOpts = append(cacheOpts, availabilitycache.WithVersionResolver(availabilityversion.NewPostgresResolver(db)))
	}
	cache, err := availabilitycache.NewCache(store, availabilitycache.DefaultConfig(), cacheOpts...)
	if err != nil {
		_ = store.Close()
		return nil, err
	}

	return &availabilityCacheRuntime{
		Cache: cache,
		Addr:  cfg.Addr,
		DB:    cfg.DB,
		store: store,
	}, nil
}

// isAvailabilityCacheConfigError permite a main distinguir una configuración
// inválida (error de despliegue) de una caída de Redis (degradación).
func isAvailabilityCacheConfigError(err error) bool {
	return errors.Is(err, availabilitycache.ErrInvalidConfig)
}
