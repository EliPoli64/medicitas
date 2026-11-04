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
type Cache struct {
	store    Store
	keys     keyBuilder
	cfg      Config
	newEpoch func() (string, error)
}

// NewCache construye una caché. La conexión con Redis se comprueba aparte,
// mediante el método Ping del Store concreto.
func NewCache(store Store, cfg Config) (*Cache, error) {
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

	return &Cache{
		store:    store,
		keys:     keys,
		cfg:      cfg,
		newEpoch: newEpochToken,
	}, nil
}

// GetOrLoad devuelve la disponibilidad desde Redis o, si no hay una entrada
// válida, desde el Loader.
//
// Un fallo de Redis no invalida la consulta: el Loader sigue siendo la fuente
// de verdad y el error de caché se informa en Result.CacheErr. El error de
// retorno se reserva para la validación o para un fallo del Loader.
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
			return Result{Snapshot: snapshot, Hit: true}, nil
		}

		// El valor existe pero no es confiable. Se recalcula y se intenta
		// reemplazar, sin servirlo nunca al llamador.
		snapshot, loadErr := c.load(ctx, req, load)
		if loadErr != nil {
			return Result{}, loadErr
		}
		setErr := c.setKey(ctx, key, snapshot)
		return Result{Snapshot: snapshot, CacheErr: errors.Join(decodeErr, setErr)}, nil
	}

	if !errors.Is(getErr, ErrMiss) {
		return c.loadResult(ctx, req, load, getErr)
	}

	snapshot, loadErr := c.load(ctx, req, load)
	if loadErr != nil {
		return Result{}, loadErr
	}
	setErr := c.setKey(ctx, key, snapshot)
	return Result{Snapshot: snapshot, CacheErr: setErr}, nil
}

func (c *Cache) loadResult(ctx context.Context, req Request, load LoaderFunc, cacheErr error) (Result, error) {
	snapshot, err := c.load(ctx, req, load)
	if err != nil {
		return Result{}, err
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
