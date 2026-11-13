package main

// RF3: mantener el catálogo de servicios de la clínica, donde cada servicio
// lleva nombre, duración en minutos y la especialidad que requiere.
//
// La duración determina el tamaño del espacio en la agenda (I4), por lo que
// cambiarla altera la disponibilidad: al integrar la caché (RF7) un cambio de
// duración debe invalidar las agendas de los profesionales que ofrecen el
// servicio (ver docs/cache-disponibilidad.md §9).
//
// La especialidad es una tabla global (catálogo base del dominio); el servicio
// pertenece a la clínica del token (I5).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

var (
	errServicioFueraDeClinica = errors.New("servicio no encontrado en la clínica")
	errEspecialidadInvalida   = errors.New("la especialidad no existe o no está activa")
	errServicioDuplicado      = errors.New("ya existe un servicio con ese nombre en la clínica")
	errDuracionInvalida       = errors.New("duracionMinutos debe estar entre 1 y 1440")
)

// Especialidad es la proyección de "Especialidad" (catálogo global).
type Especialidad struct {
	ID     string `json:"id"`
	Codigo string `json:"codigo"`
	Nombre string `json:"nombre"`
	Activo bool   `json:"activo"`
}

// Service es la proyección de "Servicio".
type Service struct {
	ID                 string `json:"id"`
	Nombre             string `json:"nombre"`
	DuracionMinutos    int    `json:"duracionMinutos"`
	EspecialidadID     string `json:"especialidadId"`
	EspecialidadNombre string `json:"especialidadNombre"`
	Activo             bool   `json:"activo"`
}

// ServiceStore abstrae la persistencia para tests sin Postgres.
type ServiceStore interface {
	CreateServicio(ctx context.Context, clinicaIdent, nombre string, duracionMinutos int, especialidadID string) (Service, error)
	ListServicios(ctx context.Context, clinicaIdent string) ([]Service, error)
	GetServicio(ctx context.Context, clinicaIdent, id string) (Service, error)
	// UpdateServicio aplica nombre y/o duración. Nil = sin cambio.
	UpdateServicio(ctx context.Context, clinicaIdent, id string, nombre *string, duracion *int) (Service, error)
	// SetServicioActivo hace la baja lógica del catálogo.
	SetServicioActivo(ctx context.Context, clinicaIdent, id string, activo bool) (Service, error)
	// ProfesionalesDelServicio devuelve los IDs de profesionales de la clínica
	// que tienen el servicio habilitado. Se usa para invalidar la caché cuando
	// cambian la duración o el estado de un servicio.
	ProfesionalesDelServicio(ctx context.Context, clinicaIdent, servicioID string) ([]string, error)
	ListEspecialidades(ctx context.Context) ([]Especialidad, error)
}

type pgServiceStore struct {
	db *sql.DB
}

func newPGServiceStore(database *sql.DB) *pgServiceStore {
	return &pgServiceStore{db: database}
}

func (s *pgServiceStore) CreateServicio(ctx context.Context, clinicaIdent, nombre string, duracionMinutos int, especialidadID string) (Service, error) {
	// La especialidad debe existir y estar activa (RF3: "la especialidad que
	// requiere"); el CHECK y la FK del esquema son la red de seguridad.
	var svc Service
	err := dbq(ctx, s.db).QueryRowContext(ctx, `
INSERT INTO "Servicio" ("clinicaId", "nombre", "duracionMinutos", "especialidadId")
VALUES ((SELECT "id" FROM "Clinica" WHERE "identificador" = $1), $2, $3,
        (SELECT "id" FROM "Especialidad" WHERE "id" = $4 AND "activo" = true))
RETURNING "id"::text, "nombre", "duracionMinutos", "especialidadId"::text,
          (SELECT e."nombre" FROM "Especialidad" e WHERE e."id" = "Servicio"."especialidadId"),
          "activo"`,
		clinicaIdent, nombre, duracionMinutos, especialidadID,
	).Scan(&svc.ID, &svc.Nombre, &svc.DuracionMinutos, &svc.EspecialidadID, &svc.EspecialidadNombre, &svc.Activo)
	if isUniqueViolation(err, "ServicioNombreUq") {
		return Service{}, errServicioDuplicado
	}
	if err != nil {
		// FK nula si la especialidad no existe/activa: la subconsulta da NULL.
		if isForeignKeyViolation(err) || isNotNullViolation(err) {
			return Service{}, errEspecialidadInvalida
		}
		return Service{}, fmt.Errorf("insertar servicio: %w", err)
	}
	return svc, nil
}

func (s *pgServiceStore) ListServicios(ctx context.Context, clinicaIdent string) ([]Service, error) {
	rows, err := dbq(ctx, s.db).QueryContext(ctx, `
SELECT s."id"::text, s."nombre", s."duracionMinutos", s."especialidadId"::text,
       e."nombre", s."activo"
FROM "Servicio" s
JOIN "Clinica" c     ON c."id" = s."clinicaId"
JOIN "Especialidad" e ON e."id" = s."especialidadId"
WHERE c."identificador" = $1
ORDER BY s."nombre"`,
		clinicaIdent)
	if err != nil {
		return nil, fmt.Errorf("listar servicios: %w", err)
	}
	defer rows.Close()

	out := make([]Service, 0)
	for rows.Next() {
		var svc Service
		if err := rows.Scan(&svc.ID, &svc.Nombre, &svc.DuracionMinutos,
			&svc.EspecialidadID, &svc.EspecialidadNombre, &svc.Activo); err != nil {
			return nil, fmt.Errorf("leer servicio: %w", err)
		}
		out = append(out, svc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterar servicios: %w", err)
	}
	return out, nil
}

func (s *pgServiceStore) GetServicio(ctx context.Context, clinicaIdent, id string) (Service, error) {
	var svc Service
	err := dbq(ctx, s.db).QueryRowContext(ctx, `
SELECT s."id"::text, s."nombre", s."duracionMinutos", s."especialidadId"::text,
       e."nombre", s."activo"
FROM "Servicio" s
JOIN "Clinica" c     ON c."id" = s."clinicaId"
JOIN "Especialidad" e ON e."id" = s."especialidadId"
WHERE c."identificador" = $1 AND s."id" = $2`,
		clinicaIdent, id,
	).Scan(&svc.ID, &svc.Nombre, &svc.DuracionMinutos,
		&svc.EspecialidadID, &svc.EspecialidadNombre, &svc.Activo)
	if errors.Is(err, sql.ErrNoRows) {
		return Service{}, errServicioFueraDeClinica
	}
	if err != nil {
		return Service{}, fmt.Errorf("consultar servicio: %w", err)
	}
	return svc, nil
}

func (s *pgServiceStore) UpdateServicio(ctx context.Context, clinicaIdent, id string, nombre *string, duracion *int) (Service, error) {
	var nombreVal sql.NullString
	if nombre != nil {
		nombreVal = sql.NullString{Valid: true, String: *nombre}
	}
	var duracionVal sql.NullInt64
	if duracion != nil {
		duracionVal = sql.NullInt64{Valid: true, Int64: int64(*duracion)}
	}
	var svc Service
	err := dbq(ctx, s.db).QueryRowContext(ctx, `
UPDATE "Servicio" s
SET "nombre"          = COALESCE($3, s."nombre"),
    "duracionMinutos" = COALESCE($4, s."duracionMinutos")
FROM "Clinica" c
WHERE s."id" = $2
  AND s."clinicaId" = c."id"
  AND c."identificador" = $1
RETURNING s."id"::text, s."nombre", s."duracionMinutos", s."especialidadId"::text,
          (SELECT e."nombre" FROM "Especialidad" e WHERE e."id" = s."especialidadId"),
          s."activo"`,
		clinicaIdent, id, nombreVal, duracionVal,
	).Scan(&svc.ID, &svc.Nombre, &svc.DuracionMinutos, &svc.EspecialidadID, &svc.EspecialidadNombre, &svc.Activo)
	if errors.Is(err, sql.ErrNoRows) {
		return Service{}, errServicioFueraDeClinica
	}
	if isUniqueViolation(err, "ServicioNombreUq") {
		return Service{}, errServicioDuplicado
	}
	if isCheckViolation(err) {
		return Service{}, errDuracionInvalida
	}
	if err != nil {
		return Service{}, fmt.Errorf("actualizar servicio: %w", err)
	}
	return svc, nil
}

func (s *pgServiceStore) SetServicioActivo(ctx context.Context, clinicaIdent, id string, activo bool) (Service, error) {
	var svc Service
	err := dbq(ctx, s.db).QueryRowContext(ctx, `
UPDATE "Servicio" s
SET "activo" = $3
FROM "Clinica" c
WHERE s."id" = $2
  AND s."clinicaId" = c."id"
  AND c."identificador" = $1
RETURNING s."id"::text, s."nombre", s."duracionMinutos", s."especialidadId"::text,
          (SELECT e."nombre" FROM "Especialidad" e WHERE e."id" = s."especialidadId"),
          s."activo"`,
		clinicaIdent, id, activo,
	).Scan(&svc.ID, &svc.Nombre, &svc.DuracionMinutos, &svc.EspecialidadID, &svc.EspecialidadNombre, &svc.Activo)
	if errors.Is(err, sql.ErrNoRows) {
		return Service{}, errServicioFueraDeClinica
	}
	if err != nil {
		return Service{}, fmt.Errorf("activar/desactivar servicio: %w", err)
	}
	return svc, nil
}

func (s *pgServiceStore) ProfesionalesDelServicio(ctx context.Context, clinicaIdent, servicioID string) ([]string, error) {
	rows, err := dbq(ctx, s.db).QueryContext(ctx, `
SELECT ps."profesionalId"::text
FROM "ProfesionalServicio" ps
JOIN "Clinica" c ON c."id" = ps."clinicaId"
WHERE c."identificador" = $1 AND ps."servicioId" = $2`,
		clinicaIdent, servicioID,
	)
	if err != nil {
		return nil, fmt.Errorf("listar profesionales del servicio: %w", err)
	}
	defer rows.Close()

	out := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("leer profesional del servicio: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *pgServiceStore) ListEspecialidades(ctx context.Context) ([]Especialidad, error) {
	rows, err := dbq(ctx, s.db).QueryContext(ctx, `
SELECT "id"::text, "codigo", "nombre", "activo"
FROM "Especialidad"
ORDER BY "nombre"`)
	if err != nil {
		return nil, fmt.Errorf("listar especialidades: %w", err)
	}
	defer rows.Close()

	out := make([]Especialidad, 0)
	for rows.Next() {
		var e Especialidad
		if err := rows.Scan(&e.ID, &e.Codigo, &e.Nombre, &e.Activo); err != nil {
			return nil, fmt.Errorf("leer especialidad: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterar especialidades: %w", err)
	}
	return out, nil
}

// serviceStore es el store global (inyectable en tests).
var serviceStore ServiceStore

// --- Handlers ---

type createServicioRequest struct {
	Nombre          string `json:"nombre"`
	DuracionMinutos int    `json:"duracionMinutos"`
	EspecialidadID  string `json:"especialidadId"`
}

func (r *createServicioRequest) validate() []string {
	var errs []string
	if r.Nombre == "" || len(r.Nombre) > 120 {
		errs = append(errs, "nombre es obligatorio (máx. 120 caracteres)")
	}
	if r.DuracionMinutos <= 0 || r.DuracionMinutos > 1440 {
		errs = append(errs, "duracionMinutos debe estar entre 1 y 1440")
	}
	if r.EspecialidadID == "" {
		errs = append(errs, "especialidadId es obligatorio")
	}
	return errs
}

// createServicio maneja POST /servicios (RF3).
func createServicio(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, serviceStore == nil, "servicio de catálogo no disponible")
	if !ok {
		return
	}
	var req createServicioRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo JSON inválido", "detalles": []string{err.Error()}})
		return
	}
	if errs := req.validate(); len(errs) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida", "detalles": errs})
		return
	}

	svc, err := serviceStore.CreateServicio(c.Request.Context(), clinica,
		req.Nombre, req.DuracionMinutos, req.EspecialidadID)
	switch {
	case errors.Is(err, errServicioDuplicado):
		c.JSON(http.StatusConflict, gin.H{"error": errServicioDuplicado.Error()})
	case errors.Is(err, errEspecialidadInvalida):
		c.JSON(http.StatusBadRequest, gin.H{"error": errEspecialidadInvalida.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo crear el servicio"})
	default:
		c.JSON(http.StatusCreated, gin.H{
			"clinicaIdentificador": clinica,
			"servicio":             svc,
		})
	}
}

// listServicios maneja GET /servicios (RF3).
func listServicios(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, serviceStore == nil, "servicio de catálogo no disponible")
	if !ok {
		return
	}
	svcs, err := serviceStore.ListServicios(c.Request.Context(), clinica)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudieron listar los servicios"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"clinicaIdentificador": clinica,
		"servicios":            svcs,
	})
}

type updateServicioRequest struct {
	Nombre          *string `json:"nombre"`
	DuracionMinutos *int    `json:"duracionMinutos"`
	Activo          *bool   `json:"activo"`
}

// updateServicio maneja PATCH /servicios/:id (RF3: mantener el catálogo).
func updateServicio(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, serviceStore == nil, "servicio de catálogo no disponible")
	if !ok {
		return
	}
	var req updateServicioRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo JSON inválido", "detalles": []string{err.Error()}})
		return
	}
	if req.Nombre == nil && req.DuracionMinutos == nil && req.Activo == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida", "detalles": []string{"indicar nombre, duracionMinutos y/o activo"}})
		return
	}
	if req.DuracionMinutos != nil && (*req.DuracionMinutos <= 0 || *req.DuracionMinutos > 1440) {
		c.JSON(http.StatusBadRequest, gin.H{"error": errDuracionInvalida.Error()})
		return
	}

	id := c.Param("id")
	var updated Service
	var err error
	// Solo la duración y el estado (activo) alteran los espacios ofrecidos.
	afectaDisponibilidad := false

	if req.Nombre != nil || req.DuracionMinutos != nil {
		updated, err = serviceStore.UpdateServicio(c.Request.Context(), clinica, id, req.Nombre, req.DuracionMinutos)
		if err == nil && req.DuracionMinutos != nil {
			afectaDisponibilidad = true
		}
	}
	if err == nil && req.Activo != nil {
		updated, err = serviceStore.SetServicioActivo(c.Request.Context(), clinica, id, *req.Activo)
		if err == nil {
			afectaDisponibilidad = true
		}
	}
	// Si UpdateServicio confirmó un cambio de disponibilidad y SetServicioActivo
	// falló después, el COMMIT parcial ya modificó la base; invalidamos igual.
	// Usamos el id del path porque SetServicioActivo puede devolver un Service
	// vacío al fallar.
	if err != nil && afectaDisponibilidad {
		invalidarAgendasPorServicio(c.Request.Context(), clinica, id)
	}
	switch {
	case errors.Is(err, errServicioFueraDeClinica):
		c.JSON(http.StatusNotFound, gin.H{"error": "servicio no encontrado"})
	case errors.Is(err, errServicioDuplicado):
		c.JSON(http.StatusConflict, gin.H{"error": errServicioDuplicado.Error()})
	case errors.Is(err, errDuracionInvalida):
		c.JSON(http.StatusBadRequest, gin.H{"error": errDuracionInvalida.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo actualizar el servicio"})
	default:
		if afectaDisponibilidad {
			invalidarAgendasPorServicio(c.Request.Context(), clinica, id)
		}
		c.JSON(http.StatusOK, gin.H{
			"clinicaIdentificador": clinica,
			"servicio":             updated,
		})
	}
}

// listEspecialidades maneja GET /especialidades (catálogo global de RF3).
func listEspecialidades(c *gin.Context) {
	if serviceStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de catálogo no disponible"})
		return
	}
	esps, err := serviceStore.ListEspecialidades(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudieron listar las especialidades"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"especialidades": esps})
}

// clinicaFromContext resuelve la clínica del dominio (I5) o responde error.
func clinicaFromContext(c *gin.Context, storeUnavailable bool, unavailableMsg string) (string, bool) {
	if storeUnavailable {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": unavailableMsg})
		return "", false
	}
	du, ok := domainUserFromContext(c)
	if !ok || du.ClinicaIdentificador == nil || *du.ClinicaIdentificador == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "usuario sin clínica"})
		return "", false
	}
	return *du.ClinicaIdentificador, true
}
