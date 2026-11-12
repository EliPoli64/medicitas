package availabilitycache

import (
	"context"
	"errors"
	"fmt"
)

// Cache implementa la política de caché de disponibilidad.
//
// La fuente de verdad es el Loader; Redis solo guarda snapshots derivados.
// Una entrada es válida únicamente bajo el epoch vigente de la agenda del
// profesional. Publicar un epoch nuevo hace inalcanzables las entradas
// anteriores sin borrarlas.
//
// Cuando se configura un AgendaVersionResolver, cada cache hit también se
// valida contra la versión autoritativa de PostgreSQL. Esto es una segunda
// barrera tras el epoch: detecta snapshots que quedaron obsoletos cuando una
// invalidación no llegó a Redis o cuando Redis conserva datos huérfanos.
type Cache struct {
	store    Store
	keys     keyBuilder
	cfg      Config
	newEpoch func() (string, error)
	resolver AgendaVersionResolver
}

// CacheOption permite configurar dependencias opcionales de Cache sin romper
// la firma de NewCache.
type CacheOption func(*Cache)

// WithVersionResolver conecta Cache con la fuente autoritativa de versiones
// de agendas. Si no se provee, Cache conserva el comportamiento basado solo
// en epochs.
func WithVersionResolver(r AgendaVersionResolver) CacheOption {
	return func(c *Cache) { c.resolver = r }
}

// NewCache construye una caché. La conexión con Redis se comprueba aparte,
// mediante el método Ping del Store concreto.
func NewCache(store Store, cfg Config, opts ...CacheOption) (*Cache, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: store is nil", ErrInvalidConfig)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	keys, err := newKeyBuilder(cfg)
	if err != nil {
		return nil, err
	}

	c := &Cache{
		store:    store,
		keys:     keys,
		cfg:      cfg,
		newEpoch: newEpochToken,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// GetOrLoad devuelve la disponibilidad desde Redis o, si no hay una entrada
// válida, desde el Loader.
//
// Un fallo de Redis no invalida la consulta: el Loader sigue siendo la fuente
// de verdad y el error de caché se informa en Result.CacheErr. El error de
// retorno se reserva para la validación o para un fallo del Loader.
//
// Cuando hay AgendaVersionResolver configurado, un cache hit solo se sirve si
// la versión del Snapshot coincide con la versión autoritativa de PostgreSQL.
// Si no coincide, o si no se puede consultar la versión, se descarta el
// Snapshot y se recalcula desde el Loader.
func (c *Cache) GetOrLoad(ctx context.Context, req Request, load LoaderFunc) (Result, error) {
	if err := validateRequest(req); err != nil {
		return Result{}, err
	}
	if load == nil {
		return Result{}, fmt.Errorf("%w: loader is nil", ErrInvalidRequest)
	}

	epoch, epochErr := c.currentEpoch(ctx, req.ClinicaID, req.ProfesionalID)
	if epochErr != nil {
		return c.loadResult(ctx, req, load, epochErr)
	}

	key, err := c.keys.dataKey(req, epoch)
	if err != nil {
		// No debería ocurrir después de validar el request y el epoch; si
		// ocurre, se sirve el dato de la fuente de verdad sin cachearlo.
		cacheErr := fmt.Errorf("%w: %w", ErrCacheRead, err)
		return c.loadResult(ctx, req, load, cacheErr)
	}

	raw, getErr := c.getKey(ctx, key)
	if getErr == nil {
		snapshot, decodeErr := decodeSnapshot(raw, req)
		if decodeErr == nil {
			currentVersion, resolved, resolveErr := c.resolveVersion(ctx, req.ClinicaID, req.ProfesionalID)
			if resolveErr != nil {
				// No se puede comprobar la versión: no servir el Snapshot
				// cacheado como válido.
				return c.loadResult(ctx, req, load, fmt.Errorf("%w: %w", ErrCacheRead, resolveErr))
			}
			if !resolved {
				// Resolver no configurado: un Snapshot v3 requiere validación
				// y no puede servirse como HIT. Se recalcula desde la fuente.
				return c.resultFromLoadAndCache(ctx, req, load, key, nil)
			}
			if currentVersion == snapshot.AgendaVersion {
				return Result{Snapshot: snapshot, Hit: true}, nil
			}
			// Versión desactualizada: recalcular y reemplazar bajo el mismo
			// epoch, que sigue siendo el vigente.
			return c.resultFromLoadAndCache(ctx, req, load, key, nil)
		}

		// El valor existe pero no es confiable. Se recalcula y se intenta
		// reemplazar, sin servirlo nunca al llamador.
		return c.resultFromLoadAndCache(ctx, req, load, key, decodeErr)
	}

	if !errors.Is(getErr, ErrMiss) {
		return c.loadResult(ctx, req, load, getErr)
	}

	return c.resultFromLoadAndCache(ctx, req, load, key, nil)
}

func (c *Cache) loadResult(ctx context.Context, req Request, load LoaderFunc, cacheErr error) (Result, error) {
	snapshot, consistencyErr := c.loadConsistent(ctx, req, load)
	if consistencyErr != nil {
		// Un error de consistencia (resolver falló o la agenda cambió durante
		// el cálculo) no es fatal: se devuelve el Snapshot del Loader sin
		// cachear, informando el problema en CacheErr. Los errores del Loader
		// o de validación se propagan como errores de retorno.
		if errors.Is(consistencyErr, ErrCacheRead) || errors.Is(consistencyErr, ErrStaleSnapshot) {
			return Result{Snapshot: snapshot, CacheErr: errors.Join(cacheErr, consistencyErr)}, nil
		}
		return Result{}, consistencyErr
	}
	return Result{Snapshot: snapshot, CacheErr: cacheErr}, nil
}

func (c *Cache) load(ctx context.Context, req Request, load LoaderFunc) (Snapshot, error) {
	snapshot, err := load(ctx, req)
	if err != nil {
		return Snapshot{}, err
	}

	normalizeSnapshot(&snapshot)
	if err := validateSnapshot(snapshot, req); err != nil {
		return Snapshot{}, fmt.Errorf("load availability: %w", err)
	}
	return snapshot, nil
}

// loadAndCache calcula un Snapshot consistente y lo guarda en Redis. Si la
// agenda cambia de versión durante el cálculo, no se almacena.
func (c *Cache) loadAndCache(ctx context.Context, req Request, load LoaderFunc, key string) (Snapshot, error) {
	snapshot, consistencyErr := c.loadConsistent(ctx, req, load)
	if consistencyErr != nil {
		return snapshot, consistencyErr
	}
	return snapshot, c.setKey(ctx, key, snapshot)
}

// resultFromLoadAndCache ejecuta loadAndCache y clasifica el error. Los
// errores de caché o consistencia se devuelven en Result.CacheErr; los errores
// del Loader se propagan como error de retorno.
func (c *Cache) resultFromLoadAndCache(ctx context.Context, req Request, load LoaderFunc, key string, priorErr error) (Result, error) {
	snapshot, err := c.loadAndCache(ctx, req, load, key)
	if err != nil {
		if errors.Is(err, ErrCacheRead) || errors.Is(err, ErrStaleSnapshot) || errors.Is(err, ErrCacheWrite) {
			return Result{Snapshot: snapshot, CacheErr: errors.Join(priorErr, err)}, nil
		}
		return Result{}, err
	}
	return Result{Snapshot: snapshot, CacheErr: priorErr}, nil
}

// loadConsistent lee la versión de la agenda antes y después de invocar al
// Loader. Si ambas coinciden, el Snapshot se etiqueta con esa versión y se
// considera consistente con el punto de lectura. Si no coinciden, el Snapshot
// calculado se devuelve con ErrStaleSnapshot: no se garantiza que refleje el
// estado actual de la agenda.
func (c *Cache) loadConsistent(ctx context.Context, req Request, load LoaderFunc) (Snapshot, error) {
	var v0 int64
	resolved := false
	if c.resolver != nil {
		var err error
		v0, resolved, err = c.resolveVersion(ctx, req.ClinicaID, req.ProfesionalID)
		if err != nil {
			// El resolver falló antes de poder invocar al Loader. Se intenta
			// cargar desde la fuente de verdad de todos modos, porque un fallo
			// de PostgreSQL en la lectura de versión no debe ocultar un
			// resultado válido del Loader.
			snapshot, loadErr := c.load(ctx, req, load)
			if loadErr != nil {
				return Snapshot{}, loadErr
			}
			return snapshot, fmt.Errorf("%w: %w", ErrCacheRead, err)
		}
	}

	snapshot, err := c.load(ctx, req, load)
	if err != nil {
		return Snapshot{}, err
	}

	if resolved {
		v1, _, err := c.resolveVersion(ctx, req.ClinicaID, req.ProfesionalID)
		if err != nil {
			return snapshot, fmt.Errorf("%w: %w", ErrCacheRead, err)
		}
		if v0 != v1 {
			return snapshot, ErrStaleSnapshot
		}
		snapshot.AgendaVersion = v1
	}

	return snapshot, nil
}

// resolveVersion devuelve la versión autoritativa de la agenda. El segundo
// valor indica si hay un resolver configurado. Si no lo hay, la caché delega
// exclusivamente en el epoch.
func (c *Cache) resolveVersion(ctx context.Context, clinicaID, profesionalID string) (int64, bool, error) {
	if c.resolver == nil {
		return 0, false, nil
	}
	v, err := c.resolver.Resolve(ctx, clinicaID, profesionalID)
	if err != nil {
		return 0, true, err
	}
	return v, true, nil
}

func (c *Cache) getKey(ctx context.Context, key string) (string, error) {
	opCtx, cancel := c.opContext(ctx)
	defer cancel()

	raw, err := c.store.Get(opCtx, key)
	if err != nil {
		if errors.Is(err, ErrMiss) {
			return "", ErrMiss
		}
		return "", fmt.Errorf("%w: %w", ErrCacheRead, err)
	}
	return raw, nil
}

func (c *Cache) setKey(ctx context.Context, key string, snapshot Snapshot) error {
	raw, err := encodeSnapshot(snapshot)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCacheWrite, err)
	}

	opCtx, cancel := c.opContext(ctx)
	defer cancel()

	if err := c.store.Set(opCtx, key, raw, c.cfg.DataTTL); err != nil {
		return fmt.Errorf("%w: %w", ErrCacheWrite, err)
	}
	return nil
}

func (c *Cache) opContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.cfg.OpTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.cfg.OpTimeout)
}

// InvalidateAgenda publica un epoch nuevo para la agenda del profesional.
//
// Las entradas viejas dejan de ser alcanzables de inmediato. La operación no
// borra claves: eso evita recorrer patrones y permite que el TTL limpie los
// valores huérfanos.
func (c *Cache) InvalidateAgenda(ctx context.Context, clinicaID, profesionalID string) error {
	key, err := c.keys.epochKey(clinicaID, profesionalID)
	if err != nil {
		return err
	}

	if _, err := c.rotateEpoch(ctx, key); err != nil {
		return fmt.Errorf("%w: %w", ErrCacheInvalidation, err)
	}
	return nil
}
