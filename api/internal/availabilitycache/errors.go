package availabilitycache

import "errors"

var (
	// ErrMiss indica que la clave solicitada no existe en la caché.
	ErrMiss = errors.New("availability cache miss")

	// ErrInvalidRequest indica que la consulta no tiene los datos mínimos
	// para construir una clave válida.
	ErrInvalidRequest = errors.New("invalid availability request")

	// ErrInvalidPayload indica que el valor leído no corresponde al formato
	// esperado o a la clave consultada.
	ErrInvalidPayload = errors.New("invalid availability cache payload")
)
