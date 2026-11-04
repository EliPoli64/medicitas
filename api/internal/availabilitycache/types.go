package availabilitycache

import "time"

const (
	// SnapshotSchemaVersion identifica el formato del valor guardado en Redis.
	// Debe incrementarse si cambia la estructura de Snapshot.
	SnapshotSchemaVersion = 1

	DefaultPrefix        = "medicitas"
	DefaultFormatVersion = "v1"
	DefaultDataTTL       = 5 * time.Minute
	DefaultEpochTTL      = 24 * time.Hour
	DefaultOpTimeout     = 100 * time.Millisecond
)

// Request identifica una consulta de disponibilidad.
//
// ClinicaID debe provenir del contexto autenticado, nunca de un parámetro
// libre enviado por el cliente. Fecha es una fecha local de la clínica en
// formato YYYY-MM-DD.
type Request struct {
	ClinicaID     string
	ProfesionalID string
	ServicioID    string
	Fecha         string
}

// Slot representa un intervalo de tiempo disponible.
type Slot struct {
	Inicio time.Time `json:"inicio"`
	Fin    time.Time `json:"fin"`
}

// Snapshot es el valor que se guarda en Redis para una combinación de
// clínica, profesional, servicio y fecha.
//
// Los identificadores y la fecha permiten validar que el valor leído
// corresponde a la clave consultada antes de servirlo.
type Snapshot struct {
	SchemaVersion   int       `json:"schemaVersion"`
	ClinicaID       string    `json:"clinicaId"`
	ProfesionalID   string    `json:"profesionalId"`
	ServicioID      string    `json:"servicioId"`
	Fecha           string    `json:"fecha"`
	ZonaHoraria     string    `json:"zonaHoraria"`
	DuracionMinutos int       `json:"duracionMinutos"`
	Espacios        []Slot    `json:"espacios"`
	GeneradoEn      time.Time `json:"generadoEn"`
}

// Result contiene el resultado de una consulta a la caché.
//
// Hit indica si el Snapshot se sirvió desde Redis. CacheErr informa un fallo
// no fatal de la caché: en ese caso el Snapshot proviene del Loader y la
// consulta de disponibilidad puede continuar.
type Result struct {
	Snapshot Snapshot
	Hit      bool
	CacheErr error
}

// Config agrupa los parámetros de la caché.
type Config struct {
	Prefix        string
	FormatVersion string
	DataTTL       time.Duration
	EpochTTL      time.Duration
	OpTimeout     time.Duration
}

// DefaultConfig devuelve la configuración inicial acordada para la Etapa 1.
func DefaultConfig() Config {
	return Config{
		Prefix:        DefaultPrefix,
		FormatVersion: DefaultFormatVersion,
		DataTTL:       DefaultDataTTL,
		EpochTTL:      DefaultEpochTTL,
		OpTimeout:     DefaultOpTimeout,
	}
}
