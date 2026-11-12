package availabilitycache

import "time"

const (
	// SnapshotSchemaVersion identifica el formato del valor guardado en Redis.
	// Debe incrementarse si cambia la estructura de Snapshot.
	//
	// v2 agrega PasoMinutos al Snapshot. Un valor v1 se considera inválido y se
	// recalcula, porque no permite comprobar con qué rejilla se generó.
	//
	// v3 agrega AgendaVersion: la versión autoritativa de la agenda en
	// PostgreSQL al momento de calcular el Snapshot. Un Snapshot anterior sin
	// este campo se considera no verificable y se recalcula.
	SnapshotSchemaVersion = 3

	DefaultPrefix = "medicitas"
	// DefaultFormatVersion versiona el layout de las claves. v2 agrega el paso
	// efectivo a la clave de datos; las claves v1 quedan inalcanzables.
	DefaultFormatVersion = "v2"
	DefaultDataTTL       = 5 * time.Minute
	DefaultEpochTTL      = 24 * time.Hour
	DefaultOpTimeout     = 100 * time.Millisecond
)

// Request identifica una consulta de disponibilidad.
//
// ClinicaID debe provenir del contexto autenticado, nunca de un parámetro
// libre enviado por el cliente. Fecha es una fecha local de la clínica en
// formato YYYY-MM-DD.
//
// PasoMinutos es el paso efectivo de la rejilla de espacios (RF7). Debe venir
// ya resuelto por la capa que conoce el contrato HTTP, incluido su valor por
// defecto; la caché no inventa un default. Forma parte de la identidad de la
// entrada porque dos pasos distintos producen espacios distintos.
type Request struct {
	ClinicaID     string
	ProfesionalID string
	ServicioID    string
	Fecha         string
	PasoMinutos   int
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
	PasoMinutos     int       `json:"pasoMinutos"`
	AgendaVersion   int64     `json:"agendaVersion"`
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
