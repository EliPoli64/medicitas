package availabilitycache

import (
	"context"
	"time"
)

// Store abstrae las operaciones mínimas que la caché necesita de Redis.
//
// La implementación concreta se agrega en una etapa posterior. Mantener esta
// interfaz pequeña permite probar la lógica de la caché con un fake, sin
// levantar Redis ni PostgreSQL.
type Store interface {
	// Get devuelve ErrMiss cuando la clave no existe.
	Get(ctx context.Context, key string) (string, error)

	// Set guarda un valor con expiración. Un TTL de cero significa sin
	// expiración.
	Set(ctx context.Context, key, value string, ttl time.Duration) error

	// SetNX guarda un valor solo si la clave no existe. Devuelve true si
	// esta llamada creó la clave.
	SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error)
}

// LoaderFunc calcula la disponibilidad desde la fuente de verdad.
//
// La implementación concreta pertenece a la capa RF7 y se conecta en una fase
// posterior. La caché solo se encarga de decidir si usa el valor guardado o
// si debe invocar al Loader.
type LoaderFunc func(ctx context.Context, req Request) (Snapshot, error)

// AgendaVersionResolver consulta la versión autoritativa de una agenda.
//
// La implementación concreta pertenece a la capa de infraestructura (por
// ejemplo, PostgreSQL). availabilitycache usa este contrato para mantenerse
// independiente de la base de datos relacional; los tests pueden proveer un
// fake.
type AgendaVersionResolver interface {
	// Resolve devuelve la versión actual de la agenda para el profesional en
	// la clínica. Si la fila no existe, devuelve 0 y nil.
	Resolve(ctx context.Context, clinicaID, profesionalID string) (int64, error)
}
