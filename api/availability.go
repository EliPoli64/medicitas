package main

// RF7: calcular los espacios disponibles de un profesional en una fecha a
// partir de su horario, la duración del servicio solicitado, sus bloqueos y
// sus citas vigentes.
// RF8: consultar la disponibilidad por servicio en un rango de fechas,
// devolviendo los profesionales que tienen espacio y cuáles son.
//
// El cálculo es un espejo deliberado del trigger validarCitaEnHorario del
// motor (I3): el espacio debe caber ENTRE un solo bloque de horario vigente
// para ese día de la semana y no solapar bloqueos ni citas vigentes (estado
// reservada). Así, todo espacio que aquí se ofrece es reservable según la BD.
//
// La duración viene siempre del servicio (I4): la API nunca la recibe del
// cliente. Los inicios se alinean a una rejilla de `pasoMinutos` desde la
// medianoche local de la clínica y los espacios que ya comenzaron no se
// ofrecen (disponibilidad hacia adelante, RF6).
//
// Al integrar la caché (RF7 servido desde Redis, docs/cache-disponibilidad.md)
// este cálculo será el LoaderFunc; la forma del dato (espacios inicio/fin por
// clínica+profesional+servicio+fecha) ya coincide con availabilitycache.
//
// Tenant siempre del token (I5): `?clinicaId=` se ignora.
// Índices que sirven estas consultas: docs/disponibilidad.md.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"medicitas/api/internal/availabilitycache"

	"github.com/gin-gonic/gin"
)

var (
	errServicioDispoFuera       = errors.New("servicio no encontrado en la clínica")
	errServicioDispoInactivo    = errors.New("el servicio no está activo")
	errProfesionalDispoFuera    = errors.New("profesional no encontrado en la clínica")
	errProfesionalDispoInactivo = errors.New("el profesional no está activo")
	errProfesionalSinServicio   = errors.New("el profesional no está habilitado para el servicio")
	errFechaDispoInvalida       = errors.New("fecha inválida; usar YYYY-MM-DD")
	errRangoDispoInvalido       = errors.New("rango de fechas inválido: desde debe ser <= hasta y no exceder 62 días")
	errPasoDispoInvalido        = errors.New("pasoMinutos debe estar entre 5 y 120")
)

const (
	pasoDispoDefault  = 30
	pasoDispoMin      = 5
	pasoDispoMax      = 120
	rangoDispoMaxDias = 62
	minutosPorDia     = 24 * 60
)

// Espacio es un intervalo disponible calculado (RF7). Inicio/Fin viajan como
// instantes RFC3339 en la zona de la clínica; horaLocal es la lectura cómoda
// del reloj local ("HH:MM").
type Espacio struct {
	Inicio    time.Time `json:"inicio"`
	Fin       time.Time `json:"fin"`
	HoraLocal string    `json:"horaLocal"`
}

// Rango es un intervalo absoluto (bloqueo o cita vigente) que toca el rango
// consultado.
type Rango struct {
	Inicio time.Time `json:"inicio"`
	Fin    time.Time `json:"fin"`
}

// HorarioAgenda es un bloque de "HorarioAtencion" traducido a minutos locales
// del día, con su vigencia como fechas "YYYY-MM-DD".
type HorarioAgenda struct {
	DiaSemana    int    `json:"diaSemana"` // 1=lunes … 7=domingo
	MinutoInicio int    `json:"minutoInicio"`
	MinutoFin    int    `json:"minutoFin"`
	VigenteDesde string `json:"vigenteDesde"`
	VigenteHasta string `json:"vigenteHasta"` // "" = sin límite
}

// DatosAgenda es la materia prima del cálculo: horarios, bloqueos y citas
// vigentes que tocan el rango consultado de un profesional.
type DatosAgenda struct {
	Horarios []HorarioAgenda
	Bloqueos []Rango
	Citas    []Rango
}

// ProfesionalDispo identifica a un profesional para la consulta de
// disponibilidad (RF7/RF8).
type ProfesionalDispo struct {
	ID           string `json:"id"`
	IdpSub       string `json:"idpSub"`
	Especialidad string `json:"especialidad"`
	Activo       bool   `json:"activo"`
}

// DisponibilidadStore abstrae la persistencia de RF7/RF8 (tests sin Postgres).
type DisponibilidadStore interface {
	ZonaHoraria(ctx context.Context, clinicaIdent string) (string, error)
	// ServicioDispo resuelve la duración del servicio (I4). errServicioDispoFuera si no es de la clínica.
	ServicioDispo(ctx context.Context, clinicaIdent, servicioID string) (duracionMinutos int, activo bool, err error)
	// ProfesionalDispo resuelve el profesional dentro de la clínica (I5).
	ProfesionalDispo(ctx context.Context, clinicaIdent, profesionalID string) (ProfesionalDispo, error)
	// ProfesionalHabilitado indica si el profesional atiende ese servicio (RF4).
	ProfesionalHabilitado(ctx context.Context, clinicaIdent, profesionalID, servicioID string) (bool, error)
	// ProfesionalesDelServicio lista los profesionales activos habilitados (RF8).
	ProfesionalesDelServicio(ctx context.Context, clinicaIdent, servicioID string) ([]ProfesionalDispo, error)
	// DatosAgenda carga horarios, bloqueos y citas vigentes que tocan [desde, hasta).
	DatosAgenda(ctx context.Context, clinicaIdent, profesionalID string, desde, hasta time.Time) (DatosAgenda, error)
}

// --- Cálculo puro (sin BD): unit-testeable tal cual ---

type rangoMin struct{ a, b int } // [a, b) en minutos locales del día

// espaciosDelDia calcula los espacios de una fecha local de la clínica.
func espaciosDelDia(datos DatosAgenda, fecha time.Time, zona *time.Location, duracionMin, pasoMin int, ahora time.Time) []Espacio {
	dia := time.Date(fecha.Year(), fecha.Month(), fecha.Day(), 0, 0, 0, 0, zona)
	diaFin := dia.AddDate(0, 0, 1)
	isoDow := int(dia.Weekday()) // domingo=0 → 7
	if isoDow == 0 {
		isoDow = 7
	}
	fechaStr := dia.Format("2006-01-02")

	// Ocupados del día en minutos locales, recortados al día. Esto absorbe
	// bloqueos multidía (una vacación larga cruza muchas fechas).
	ocup := make([]rangoMin, 0, len(datos.Bloqueos)+len(datos.Citas))
	for _, grupo := range [][]Rango{datos.Bloqueos, datos.Citas} {
		for _, r := range grupo {
			if !r.Inicio.Before(diaFin) || !r.Fin.After(dia) {
				continue
			}
			a := int(r.Inicio.Sub(dia) / time.Minute)
			b := int(r.Fin.Sub(dia) / time.Minute)
			if a < 0 {
				a = 0
			}
			if b > minutosPorDia {
				b = minutosPorDia
			}
			if b > a {
				ocup = append(ocup, rangoMin{a, b})
			}
		}
	}
	ocup = fusionar(ocup)

	espacios := make([]Espacio, 0)
	for _, h := range datos.Horarios {
		if h.DiaSemana != isoDow || h.VigenteDesde > fechaStr {
			continue
		}
		if h.VigenteHasta != "" && h.VigenteHasta < fechaStr {
			continue
		}
		for _, libre := range restar(rangoMin{h.MinutoInicio, h.MinutoFin}, ocup) {
			// Rejilla de `paso` minutos alineada a la medianoche local.
			k := ((libre.a + pasoMin - 1) / pasoMin) * pasoMin
			for ; k+duracionMin <= libre.b; k += pasoMin {
				inicio := dia.Add(time.Duration(k) * time.Minute)
				if !inicio.After(ahora) {
					continue // disponibilidad hacia adelante (RF6)
				}
				espacios = append(espacios, Espacio{
					Inicio:    inicio,
					Fin:       inicio.Add(time.Duration(duracionMin) * time.Minute),
					HoraLocal: fmt.Sprintf("%02d:%02d", k/60, k%60),
				})
			}
		}
	}
	sort.Slice(espacios, func(i, j int) bool { return espacios[i].Inicio.Before(espacios[j].Inicio) })
	return espacios
}

// filtrarEspaciosFuturos descarta intervalos cuyo inicio ya no es futuro.
// El Snapshot de Redis se calculó con el reloj de ese momento; AgendaVersion
// no avanza solo porque pasó el tiempo, así que RF7 vuelve a filtrar al responder.
func filtrarEspaciosFuturos(espacios []Espacio, ahora time.Time) []Espacio {
	out := make([]Espacio, 0, len(espacios))
	for _, e := range espacios {
		if e.Inicio.After(ahora) {
			out = append(out, e)
		}
	}
	return out
}

// minutosHoraTexto convierte el texto de una columna time ("08:00:00") en
// minutos desde la medianoche y segundos.
func minutosHoraTexto(s string) (minutos, segundos int, ok bool) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 {
		return 0, 0, false
	}
	h, err := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err != nil || err2 != nil {
		return 0, 0, false
	}
	if len(parts) == 3 {
		if seg, err3 := strconv.Atoi(parts[2]); err3 == nil {
			segundos = seg
		}
	}
	return h*60 + m, segundos, true
}

// fusionar ordena y une rangos solapados.
func fusionar(in []rangoMin) []rangoMin {
	if len(in) == 0 {
		return nil
	}
	sort.Slice(in, func(i, j int) bool { return in[i].a < in[j].a })
	out := []rangoMin{in[0]}
	for _, r := range in[1:] {
		last := &out[len(out)-1]
		if r.a <= last.b {
			if r.b > last.b {
				last.b = r.b
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// restar devuelve las partes de r no cubiertas por ocup (ya fusionados).
func restar(r rangoMin, ocup []rangoMin) []rangoMin {
	out := make([]rangoMin, 0, 2)
	cur := r.a
	for _, o := range ocup {
		if o.b <= cur {
			continue
		}
		if o.a >= r.b {
			break
		}
		if o.a > cur {
			fin := o.a
			if fin > r.b {
				fin = r.b
			}
			out = append(out, rangoMin{cur, fin})
		}
		if o.b > cur {
			cur = o.b
		}
		if cur >= r.b {
			return out
		}
	}
	if cur < r.b {
		out = append(out, rangoMin{cur, r.b})
	}
	return out
}

// --- Store Postgres (consultas servidas por los índices del esquema) ---

type pgDisponibilidadStore struct {
	db *sql.DB
}

func newPGDisponibilidadStore(database *sql.DB) *pgDisponibilidadStore {
	return &pgDisponibilidadStore{db: database}
}

func (s *pgDisponibilidadStore) ZonaHoraria(ctx context.Context, clinicaIdent string) (string, error) {
	var zona string
	err := dbq(ctx, s.db).QueryRowContext(ctx,
		`SELECT c."zonaHoraria" FROM "Clinica" c WHERE c."identificador" = $1`,
		clinicaIdent,
	).Scan(&zona)
	if err != nil {
		return "", fmt.Errorf("consultar zona horaria: %w", err)
	}
	return zona, nil
}

func (s *pgDisponibilidadStore) ServicioDispo(ctx context.Context, clinicaIdent, servicioID string) (int, bool, error) {
	var duracion int
	var activo bool
	err := dbq(ctx, s.db).QueryRowContext(ctx, `
SELECT s."duracionMinutos", s."activo"
FROM "Servicio" s
JOIN "Clinica" c ON c."id" = s."clinicaId"
WHERE c."identificador" = $1 AND s."id" = $2`,
		clinicaIdent, servicioID,
	).Scan(&duracion, &activo)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, errServicioDispoFuera
	}
	if err != nil {
		return 0, false, fmt.Errorf("consultar servicio: %w", err)
	}
	return duracion, activo, nil
}

func (s *pgDisponibilidadStore) ProfesionalDispo(ctx context.Context, clinicaIdent, profesionalID string) (ProfesionalDispo, error) {
	var p ProfesionalDispo
	err := dbq(ctx, s.db).QueryRowContext(ctx, `
SELECT p."id"::text, u."idpSub", e."nombre", p."activo"
FROM "Profesional" p
JOIN "Clinica" c      ON c."id" = p."clinicaId"
JOIN "Usuario" u      ON u."id" = p."usuarioId" AND u."clinicaId" = p."clinicaId"
JOIN "Especialidad" e ON e."id" = p."especialidadId"
WHERE c."identificador" = $1 AND p."id" = $2`,
		clinicaIdent, profesionalID,
	).Scan(&p.ID, &p.IdpSub, &p.Especialidad, &p.Activo)
	if errors.Is(err, sql.ErrNoRows) {
		return ProfesionalDispo{}, errProfesionalDispoFuera
	}
	if err != nil {
		return ProfesionalDispo{}, fmt.Errorf("consultar profesional: %w", err)
	}
	return p, nil
}

func (s *pgDisponibilidadStore) ProfesionalHabilitado(ctx context.Context, clinicaIdent, profesionalID, servicioID string) (bool, error) {
	var ok bool
	err := dbq(ctx, s.db).QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM "ProfesionalServicio" ps
    JOIN "Clinica" c ON c."id" = ps."clinicaId"
    WHERE c."identificador" = $1
      AND ps."profesionalId" = $2
      AND ps."servicioId" = $3
)`,
		clinicaIdent, profesionalID, servicioID,
	).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("consultar habilitación: %w", err)
	}
	return ok, nil
}

func (s *pgDisponibilidadStore) ProfesionalesDelServicio(ctx context.Context, clinicaIdent, servicioID string) ([]ProfesionalDispo, error) {
	rows, err := dbq(ctx, s.db).QueryContext(ctx, `
SELECT p."id"::text, u."idpSub", e."nombre", p."activo"
FROM "ProfesionalServicio" ps
JOIN "Clinica" c      ON c."id" = ps."clinicaId"
JOIN "Profesional" p  ON p."id" = ps."profesionalId" AND p."clinicaId" = ps."clinicaId"
JOIN "Usuario" u      ON u."id" = p."usuarioId" AND u."clinicaId" = p."clinicaId"
JOIN "Especialidad" e ON e."id" = p."especialidadId"
WHERE c."identificador" = $1 AND ps."servicioId" = $2 AND p."activo" = true
ORDER BY e."nombre", u."idpSub"`,
		clinicaIdent, servicioID)
	if err != nil {
		return nil, fmt.Errorf("listar profesionales del servicio: %w", err)
	}
	defer rows.Close()

	out := make([]ProfesionalDispo, 0)
	for rows.Next() {
		var p ProfesionalDispo
		if err := rows.Scan(&p.ID, &p.IdpSub, &p.Especialidad, &p.Activo); err != nil {
			return nil, fmt.Errorf("leer profesional: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *pgDisponibilidadStore) DatosAgenda(ctx context.Context, clinicaIdent, profesionalID string, desde, hasta time.Time) (DatosAgenda, error) {
	desdeStr, hastaStr := desde.Format("2006-01-02"), hasta.Format("2006-01-02")
	var out DatosAgenda

	// Horarios activos cuya vigencia toca el rango.
	// Índice: HorarioAtencionProfesionalIdx (clinicaId, profesionalId, diaSemana) WHERE activo.
	rows, err := dbq(ctx, s.db).QueryContext(ctx, `
SELECT h."diaSemana", h."horaInicio", h."horaFin", h."vigenteDesde"::text, h."vigenteHasta"::text
FROM "HorarioAtencion" h
JOIN "Clinica" c ON c."id" = h."clinicaId"
WHERE c."identificador" = $1
  AND h."profesionalId" = $2
  AND h."activo" = true
  AND h."vigenteDesde" <= $4::date
  AND (h."vigenteHasta" IS NULL OR h."vigenteHasta" >= $3::date)`,
		clinicaIdent, profesionalID, desdeStr, hastaStr)
	if err != nil {
		return DatosAgenda{}, fmt.Errorf("consultar horarios: %w", err)
	}
	for rows.Next() {
		var h HorarioAgenda
		// pgx entrega las columnas time como texto ("08:00:00").
		var inicio, fin string
		var hasta sql.NullString
		if err := rows.Scan(&h.DiaSemana, &inicio, &fin, &h.VigenteDesde, &hasta); err != nil {
			rows.Close()
			return DatosAgenda{}, fmt.Errorf("leer horario: %w", err)
		}
		if hasta.Valid {
			h.VigenteHasta = hasta.String
		}
		// Segundos > 0 en el horario: el inicio se redondea hacia arriba y el
		// fin hacia abajo, para que todo espacio calculado cumpla el trigger.
		min, seg, ok := minutosHoraTexto(inicio)
		if !ok {
			rows.Close()
			return DatosAgenda{}, fmt.Errorf("horaInicio inválida: %q", inicio)
		}
		h.MinutoInicio = min
		if seg > 0 {
			h.MinutoInicio++
		}
		if min, _, ok = minutosHoraTexto(fin); !ok {
			rows.Close()
			return DatosAgenda{}, fmt.Errorf("horaFin inválida: %q", fin)
		}
		h.MinutoFin = min
		out.Horarios = append(out.Horarios, h)
	}
	if err := rows.Err(); err != nil {
		return DatosAgenda{}, fmt.Errorf("iterar horarios: %w", err)
	}

	// Bloqueos que tocan el rango.
	// Índice: BloqueoProfesionalRangoIdx (clinicaId, profesionalId, inicio, fin).
	bRows, err := dbq(ctx, s.db).QueryContext(ctx, `
SELECT b."inicio", b."fin"
FROM "Bloqueo" b
JOIN "Clinica" c ON c."id" = b."clinicaId"
WHERE c."identificador" = $1
  AND b."profesionalId" = $2
  AND b."inicio" < $4::timestamptz
  AND b."fin"    > $3::timestamptz`,
		clinicaIdent, profesionalID, desde.Format(time.RFC3339), hasta.Format(time.RFC3339))
	if err != nil {
		return DatosAgenda{}, fmt.Errorf("consultar bloqueos: %w", err)
	}
	for bRows.Next() {
		var r Rango
		if err := bRows.Scan(&r.Inicio, &r.Fin); err != nil {
			bRows.Close()
			return DatosAgenda{}, fmt.Errorf("leer bloqueo: %w", err)
		}
		out.Bloqueos = append(out.Bloqueos, r)
	}
	if err := bRows.Err(); err != nil {
		return DatosAgenda{}, fmt.Errorf("iterar bloqueos: %w", err)
	}

	// Citas vigentes que tocan el rango. Solo 'reservada' ocupa espacio: al
	// cancelar o reprogramar se libera de inmediato (I7), y atendida/ausente
	// son desenlaces de citas ya pasadas.
	//
	// El trigger validarCitaEnHorario prohíbe citas que crucen la medianoche
	// local; por eso toda cita que toca [desde, hasta) empieza dentro del
	// rango y basta acotar "inicio". Así la consulta se sirve por completo de
	// CitaProfesionalInicioIdx (rango cerrado sobre la tercera columna).
	// Índice: CitaProfesionalInicioIdx (clinicaId, profesionalId, inicio) WHERE estado='reservada'.
	cRows, err := dbq(ctx, s.db).QueryContext(ctx, `
SELECT ci."inicio", ci."fin"
FROM "Cita" ci
JOIN "Clinica" c ON c."id" = ci."clinicaId"
WHERE c."identificador" = $1
  AND ci."profesionalId" = $2
  AND ci."estado" = 'reservada'
  AND ci."inicio" >= $3::timestamptz
  AND ci."inicio" <  $4::timestamptz`,
		clinicaIdent, profesionalID, desde.Format(time.RFC3339), hasta.Format(time.RFC3339))
	if err != nil {
		return DatosAgenda{}, fmt.Errorf("consultar citas: %w", err)
	}
	for cRows.Next() {
		var r Rango
		if err := cRows.Scan(&r.Inicio, &r.Fin); err != nil {
			cRows.Close()
			return DatosAgenda{}, fmt.Errorf("leer cita: %w", err)
		}
		out.Citas = append(out.Citas, r)
	}
	if err := cRows.Err(); err != nil {
		return DatosAgenda{}, fmt.Errorf("iterar citas: %w", err)
	}
	return out, nil
}

// disponibilidadStore es el store global (inyectable en tests).
var disponibilidadStore DisponibilidadStore

// --- Handlers ---

// nowFn hace el reloj inyectable en tests.
var nowFn = time.Now

// DisponibilidadFecha agrupa la disponibilidad de un día (RF8).
type DisponibilidadFecha struct {
	Fecha         string                      `json:"fecha"`
	Profesionales []DisponibilidadProfesional `json:"profesionales"`
}

// DisponibilidadProfesional son los espacios de un profesional en una fecha.
type DisponibilidadProfesional struct {
	ProfesionalID string    `json:"profesionalId"`
	IdpSub        string    `json:"idpSub"`
	Especialidad  string    `json:"especialidad"`
	Espacios      []Espacio `json:"espacios"`
}

// resolverDisponibilidad carga clínica (I5), zona, servicio y parámetros
// comunes de RF7/RF8. Devuelve ok=false si ya respondió error.
func resolverDisponibilidad(c *gin.Context, servicioID string) (
	clinica, zona string, loc *time.Location, duracion, paso int, ok bool) {
	if disponibilidadStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de disponibilidad no disponible"})
		return
	}
	clinica, okClinica := clinicaFromContext(c, false, "")
	if !okClinica {
		return
	}

	zona, err := disponibilidadStore.ZonaHoraria(c.Request.Context(), clinica)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo resolver la zona horaria de la clínica"})
		return
	}
	loc, err = time.LoadLocation(zona)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "la zona horaria de la clínica no es válida"})
		return
	}

	paso = pasoDispoDefault
	if v := c.Query("pasoMinutos"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < pasoDispoMin || n > pasoDispoMax {
			c.JSON(http.StatusBadRequest, gin.H{"error": errPasoDispoInvalido.Error()})
			return
		}
		paso = n
	}

	duracion, activo, err := disponibilidadStore.ServicioDispo(c.Request.Context(), clinica, servicioID)
	switch {
	case errors.Is(err, errServicioDispoFuera):
		c.JSON(http.StatusNotFound, gin.H{"error": errServicioDispoFuera.Error()})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo consultar el servicio"})
		return
	}
	if !activo {
		c.JSON(http.StatusBadRequest, gin.H{"error": errServicioDispoInactivo.Error()})
		return
	}
	ok = true
	return clinica, zona, loc, duracion, paso, ok
}

// listDisponibilidadProfesional maneja GET /profesionales/:id/disponibilidad (RF7).
//
// Query: fecha=YYYY-MM-DD (obligatoria), servicioId (obligatorio: la duración
// viene del servicio, I4), pasoMinutos (opcional, 5–120, por defecto 30).
//
// Etapa 6: el cálculo pasa por availabilitycache.GetOrLoad cuando la caché
// está habilitada; si Redis no responde o el Snapshot no se puede validar, se
// calcula directamente desde PostgreSQL.
func listDisponibilidadProfesional(c *gin.Context) {
	profesionalID := c.Param("id")
	servicioID := c.Query("servicioId")
	if servicioID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida", "detalles": []string{"servicioId es obligatorio"}})
		return
	}
	fechaStr := c.Query("fecha")
	if !fechaRe.MatchString(fechaStr) {
		c.JSON(http.StatusBadRequest, gin.H{"error": errFechaDispoInvalida.Error()})
		return
	}

	clinica, okClinica := clinicaFromContext(c, false, "")
	if !okClinica {
		return
	}

	paso := pasoDispoDefault
	if v := c.Query("pasoMinutos"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < pasoDispoMin || n > pasoDispoMax {
			c.JSON(http.StatusBadRequest, gin.H{"error": errPasoDispoInvalido.Error()})
			return
		}
		paso = n
	}

	if disponibilidadStore == nil || loaderDisponibilidad == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de disponibilidad no disponible"})
		return
	}

	req := availabilitycache.Request{
		ClinicaID:     clinica,
		ProfesionalID: profesionalID,
		ServicioID:    servicioID,
		Fecha:         fechaStr,
		PasoMinutos:   paso,
	}

	ctx := c.Request.Context()
	var snapshot availabilitycache.Snapshot
	var loadErr error

	if availabilityCache != nil {
		result, err := availabilityCache.GetOrLoad(ctx, req, loaderDisponibilidad.Load)
		if err != nil {
			loadErr = err
		} else {
			if result.CacheErr != nil && errors.Is(result.CacheErr, availabilitycache.ErrStaleSnapshot) {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "disponibilidad temporalmente inconsistente, reintentar"})
				return
			}
			snapshot = result.Snapshot
		}
	} else {
		snapshot, loadErr = loaderDisponibilidad.Load(ctx, req)
	}

	if loadErr != nil {
		switch {
		case errors.Is(loadErr, availabilitycache.ErrInvalidRequest):
			c.JSON(http.StatusBadRequest, gin.H{"error": loadErr.Error()})
		case errors.Is(loadErr, errServicioDispoFuera):
			c.JSON(http.StatusNotFound, gin.H{"error": errServicioDispoFuera.Error()})
		case errors.Is(loadErr, errServicioDispoInactivo):
			c.JSON(http.StatusBadRequest, gin.H{"error": errServicioDispoInactivo.Error()})
		case errors.Is(loadErr, errProfesionalDispoFuera):
			c.JSON(http.StatusNotFound, gin.H{"error": errProfesionalDispoFuera.Error()})
		case errors.Is(loadErr, errProfesionalDispoInactivo):
			c.JSON(http.StatusBadRequest, gin.H{"error": errProfesionalDispoInactivo.Error()})
		case errors.Is(loadErr, errProfesionalSinServicio):
			c.JSON(http.StatusBadRequest, gin.H{"error": errProfesionalSinServicio.Error()})
		case errors.Is(loadErr, errFechaDispoInvalida):
			c.JSON(http.StatusBadRequest, gin.H{"error": errFechaDispoInvalida.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo calcular la disponibilidad"})
		}
		return
	}

	loc, err := time.LoadLocation(snapshot.ZonaHoraria)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "zona horaria de la clínica no es válida"})
		return
	}
	fecha, _ := time.ParseInLocation("2006-01-02", snapshot.Fecha, loc)
	dia := time.Date(fecha.Year(), fecha.Month(), fecha.Day(), 0, 0, 0, 0, loc)

	espacios := make([]Espacio, len(snapshot.Espacios))
	for i, slot := range snapshot.Espacios {
		inicioLocal := slot.Inicio.In(loc)
		k := int(inicioLocal.Sub(dia) / time.Minute)
		espacios[i] = Espacio{
			Inicio:    inicioLocal,
			Fin:       slot.Fin.In(loc),
			HoraLocal: fmt.Sprintf("%02d:%02d", k/60, k%60),
		}
	}
	espacios = filtrarEspaciosFuturos(espacios, nowFn())
	if espacios == nil {
		espacios = []Espacio{}
	}

	c.JSON(http.StatusOK, gin.H{
		"clinicaIdentificador": clinica,
		"profesionalId":        profesionalID,
		"servicioId":           servicioID,
		"fecha":                fechaStr,
		"zonaHoraria":          snapshot.ZonaHoraria,
		"duracionMinutos":      snapshot.DuracionMinutos,
		"pasoMinutos":          snapshot.PasoMinutos,
		"espacios":             espacios,
	})
}

// listDisponibilidadServicio maneja GET /servicios/:id/disponibilidad (RF8).
//
// Query: desde=YYYY-MM-DD, hasta=YYYY-MM-DD (ambas obligatorias, hasta 62
// días), pasoMinutos (opcional). Devuelve solo las fechas en las que algún
// profesional habilitado tiene al menos un espacio.
func listDisponibilidadServicio(c *gin.Context) {
	servicioID := c.Param("id")
	desdeStr, hastaStr := c.Query("desde"), c.Query("hasta")
	if !fechaRe.MatchString(desdeStr) || !fechaRe.MatchString(hastaStr) {
		c.JSON(http.StatusBadRequest, gin.H{"error": errFechaDispoInvalida.Error()})
		return
	}

	clinica, zona, loc, duracion, paso, ok := resolverDisponibilidad(c, servicioID)
	if !ok {
		return
	}

	desde, err := time.ParseInLocation("2006-01-02", desdeStr, loc)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errFechaDispoInvalida.Error()})
		return
	}
	hasta, err := time.ParseInLocation("2006-01-02", hastaStr, loc)
	if err != nil || hasta.Before(desde) {
		c.JSON(http.StatusBadRequest, gin.H{"error": errRangoDispoInvalido.Error()})
		return
	}
	dias := int(hasta.Sub(desde)/(24*time.Hour)) + 1
	if dias > rangoDispoMaxDias {
		c.JSON(http.StatusBadRequest, gin.H{"error": errRangoDispoInvalido.Error()})
		return
	}

	profs, err := disponibilidadStore.ProfesionalesDelServicio(c.Request.Context(), clinica, servicioID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudieron consultar los profesionales del servicio"})
		return
	}

	now := nowFn()
	fechas := make([]DisponibilidadFecha, 0)
	for d := 0; d < dias; d++ {
		fecha := desde.AddDate(0, 0, d)
		var conEspacios []DisponibilidadProfesional
		for _, prof := range profs {
			datos, err := disponibilidadStore.DatosAgenda(c.Request.Context(), clinica, prof.ID,
				fecha, fecha.AddDate(0, 0, 1))
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo calcular la disponibilidad"})
				return
			}
			espacios := espaciosDelDia(datos, fecha, loc, duracion, paso, now)
			if len(espacios) == 0 {
				continue
			}
			conEspacios = append(conEspacios, DisponibilidadProfesional{
				ProfesionalID: prof.ID,
				IdpSub:        prof.IdpSub,
				Especialidad:  prof.Especialidad,
				Espacios:      espacios,
			})
		}
		if len(conEspacios) > 0 {
			fechas = append(fechas, DisponibilidadFecha{
				Fecha:         fecha.Format("2006-01-02"),
				Profesionales: conEspacios,
			})
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"clinicaIdentificador": clinica,
		"servicioId":           servicioID,
		"zonaHoraria":          zona,
		"duracionMinutos":      duracion,
		"pasoMinutos":          paso,
		"desde":                desdeStr,
		"hasta":                hastaStr,
		"disponibilidad":       fechas,
	})
}
