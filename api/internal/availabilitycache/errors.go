package availabilitycache

import "errors"

var (
	// ErrMiss indica que la clave solicitada no existe en la caché.
	ErrMiss = errors.New("availability cache miss")

	// ErrInvalidRequest indica que la consulta no tiene los datos mínimos
	// para construir una clave válida.
	ErrInvalidRequest = errors.New("invalid availability request")

	// ErrInvalidConfig indica que la configuración del paquete o del cliente
	// Redis no es válida.
	ErrInvalidConfig = errors.New("invalid availability cache configuration")

	// ErrInvalidPayload indica que el valor leído no corresponde al formato
	// esperado o a la clave consultada.
	ErrInvalidPayload = errors.New("invalid availability cache payload")

	// ErrCacheRead indica un fallo al leer de Redis. La consulta puede
	// continuar con el Loader.
	ErrCacheRead = errors.New("availability cache read failed")

	// ErrCacheWrite indica un fallo al escribir en Redis. La consulta puede
	// continuar con el resultado del Loader, pero no queda cacheada.
	ErrCacheWrite = errors.New("availability cache write failed")

	// ErrCacheInvalidation indica que no se pudo publicar un epoch nuevo.
	// Es un fallo que el llamador debe tratar explícitamente: la reserva
	// puede estar confirmada en PostgreSQL aunque la caché siga vieja.
	ErrCacheInvalidation = errors.New("availability cache invalidation failed")

	// ErrStaleSnapshot indica que el Snapshot se calculó mientras la agenda
	// cambiaba de versión. No se almacena ni se sirve como vigente.
	ErrStaleSnapshot = errors.New("availability snapshot stale")
)
