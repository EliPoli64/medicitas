package main

// RF4: registrar profesionales con su especialidad y asociarles los servicios
// que están habilitados para atender. "Un profesional atiende solo los
// servicios que tiene asociados" (spec §2).
//
// El profesional es un "Usuario" de la clínica con rol cuyo código es
// `profesional`; el trigger validarRolProfesional del esquema exige que el
// usuario exista, esté activo, pertenezca a la clínica y tenga ese rol. La
// asociación profesional↔servicio valida además que el servicio corresponda a
// la especialidad del profesional (trigger validarEspecialidadProfesional-
// Servicio): ambas reglas se comprueban antes en la API para responder 400/409
// con mensajes claros, y el motor es la red de seguridad.
//
// El tenant siempre es la clínica del token (I5).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

var (
	errProfesionalFueraDeClinica = errors.New("profesional no encontrado en la clínica")
	errProfesionalDuplicado      = errors.New("el usuario ya está registrado como profesional en la clínica")
	errUsuarioNoProfesional      = errors.New("el usuario no existe, no está activo o no tiene rol profesional en la clínica")
	errServicioIncompatible      = errors.New("el servicio no corresponde a la especialidad del profesional")
	errServicioFuera             = errors.New("el servicio no existe o no está activo en la clínica")
)

// ProfessionalService es la proyección de "ProfesionalServicio".
type ProfessionalService struct {
	ID              string `json:"id"`
	Nombre          string `json:"nombre"`
	DuracionMinutos int    `json:"duracionMinutos"`
}

// Professional es la proyección de "Profesional".
type Professional struct {
	ID                 string                `json:"id"`
	UsuarioID          string                `json:"usuarioId"`
	IdpSub             string                `json:"idpSub"`
	EspecialidadID     string                `json:"especialidadId"`
	EspecialidadNombre string                `json:"especialidadNombre"`
	Activo             bool                  `json:"activo"`
	Servicios          []ProfessionalService `json:"servicios"`
}

// ProfessionalStore abstrae la persistencia para tests sin Postgres.
type ProfessionalStore interface {
	CreateProfesional(ctx context.Context, clinicaIdent, usuarioID, especialidadID string) (Professional, error)
	ListProfesionales(ctx context.Context, clinicaIdent string) ([]Professional, error)
	GetProfesional(ctx context.Context, clinicaIdent, id string) (Professional, error)
	SetProfesionalActivo(ctx context.Context, clinicaIdent, id string, activo bool) (Professional, error)
	// SetServicios reemplaza el conjunto de servicios habilitados (RF4).
	// Valida existencia, pertenencia a la clínica y compatibilidad de
	// especialidad; todo en una transacción.
	SetServicios(ctx context.Context, clinicaIdent, profesionalID string, servicioIDs []string) ([]ProfessionalService, error)
}

type pgProfessionalStore struct {
	db *sql.DB
}

func newPGProfessionalStore(database *sql.DB) *pgProfessionalStore {
	return &pgProfessionalStore{db: database}
}

func (s *pgProfessionalStore) CreateProfesional(ctx context.Context, clinicaIdent, usuarioID, especialidadID string) (Professional, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Professional{}, fmt.Errorf("abrir transacción: %w", err)
	}
	defer tx.Rollback()

	// Pre-validación para responder 400 con mensaje claro: el usuario debe
	// existir en la clínica, estar activo y tener rol profesional (RF4). El
	// trigger validarRolProfesional del motor vuelve a comprobarlo.
	var ok bool
	err = tx.QueryRowContext(ctx, `
SELECT true
FROM "Usuario" u
JOIN "Clinica" c ON c."id" = u."clinicaId"
JOIN "Rol"     r ON r."id" = u."rolId"
WHERE c."identificador" = $1 AND u."id" = $2
  AND u."activo" = true AND r."codigo" = 'profesional'`,
		clinicaIdent, usuarioID,
	).Scan(&ok)
	if errors.Is(err, sql.ErrNoRows) {
		return Professional{}, errUsuarioNoProfesional
	}
	if err != nil {
		return Professional{}, fmt.Errorf("consultar usuario: %w", err)
	}

	// La especialidad debe existir y estar activa (mensaje propio, no el de
	// la violación NOT NULL de la subconsulta).
	var espOK bool
	err = tx.QueryRowContext(ctx,
		`SELECT true FROM "Especialidad" WHERE "id" = $1 AND "activo" = true`,
		especialidadID,
	).Scan(&espOK)
	if errors.Is(err, sql.ErrNoRows) {
		return Professional{}, errEspecialidadInvalida
	}
	if err != nil {
		return Professional{}, fmt.Errorf("consultar especialidad: %w", err)
	}

	var p Professional
	err = tx.QueryRowContext(ctx, `
INSERT INTO "Profesional" ("clinicaId", "usuarioId", "especialidadId")
VALUES ((SELECT "id" FROM "Clinica" WHERE "identificador" = $1), $2,
        (SELECT "id" FROM "Especialidad" WHERE "id" = $3 AND "activo" = true))
RETURNING "id"::text, "usuarioId"::text, "especialidadId"::text, "activo",
          (SELECT u."idpSub" FROM "Usuario" u WHERE u."id" = "Profesional"."usuarioId"),
          (SELECT e."nombre" FROM "Especialidad" e WHERE e."id" = "Profesional"."especialidadId")`,
		clinicaIdent, usuarioID, especialidadID,
	).Scan(&p.ID, &p.UsuarioID, &p.EspecialidadID, &p.Activo, &p.IdpSub, &p.EspecialidadNombre)
	if isUniqueViolation(err, "ProfesionalUsuarioUq") {
		return Professional{}, errProfesionalDuplicado
	}
	if err != nil {
		if isForeignKeyViolation(err) || isNotNullViolation(err) {
			return Professional{}, errUsuarioNoProfesional
		}
		// RAISE EXCEPTION de los triggers (rol no profesional, especialidad
		// que no corresponde): red de seguridad del motor.
		if isRaiseException(err) {
			return Professional{}, errUsuarioNoProfesional
		}
		return Professional{}, fmt.Errorf("insertar profesional: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Professional{}, fmt.Errorf("confirmar transacción: %w", err)
	}
	p.Servicios = []ProfessionalService{}
	return p, nil
}

func (s *pgProfessionalStore) ListProfesionales(ctx context.Context, clinicaIdent string) ([]Professional, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT p."id"::text, p."usuarioId"::text, u."idpSub", p."especialidadId"::text,
       e."nombre", p."activo"
FROM "Profesional" p
JOIN "Clinica" c      ON c."id" = p."clinicaId"
JOIN "Usuario" u      ON u."id" = p."usuarioId" AND u."clinicaId" = p."clinicaId"
JOIN "Especialidad" e ON e."id" = p."especialidadId"
WHERE c."identificador" = $1
ORDER BY e."nombre", u."idpSub"`,
		clinicaIdent)
	if err != nil {
		return nil, fmt.Errorf("listar profesionales: %w", err)
	}
	defer rows.Close()

	type rowKey struct{ id, espID, espNombre string }
	var order []rowKey
	byID := map[string]*Professional{}
	for rows.Next() {
		var p Professional
		if err := rows.Scan(&p.ID, &p.UsuarioID, &p.IdpSub,
			&p.EspecialidadID, &p.EspecialidadNombre, &p.Activo); err != nil {
			return nil, fmt.Errorf("leer profesional: %w", err)
		}
		p.Servicios = []ProfessionalService{}
		byID[p.ID] = &p
		order = append(order, rowKey{p.ID, p.EspecialidadID, p.EspecialidadNombre})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterar profesionales: %w", err)
	}
	if len(order) == 0 {
		return []Professional{}, nil
	}

	// Servicios habilitados de todos los profesionales en una sola consulta.
	srvRows, err := s.db.QueryContext(ctx, `
SELECT ps."profesionalId"::text, s."id"::text, s."nombre", s."duracionMinutos"
FROM "ProfesionalServicio" ps
JOIN "Clinica" c  ON c."id" = ps."clinicaId"
JOIN "Servicio" s ON s."id" = ps."servicioId" AND s."clinicaId" = ps."clinicaId"
WHERE c."identificador" = $1
ORDER BY s."nombre"`,
		clinicaIdent)
	if err != nil {
		return nil, fmt.Errorf("listar servicios de profesionales: %w", err)
	}
	defer srvRows.Close()
	for srvRows.Next() {
		var profID string
		var svc ProfessionalService
		if err := srvRows.Scan(&profID, &svc.ID, &svc.Nombre, &svc.DuracionMinutos); err != nil {
			return nil, fmt.Errorf("leer servicio de profesional: %w", err)
		}
		if p, ok := byID[profID]; ok {
			p.Servicios = append(p.Servicios, svc)
		}
	}
	if err := srvRows.Err(); err != nil {
		return nil, fmt.Errorf("iterar servicios de profesionales: %w", err)
	}

	out := make([]Professional, 0, len(order))
	for _, k := range order {
		out = append(out, *byID[k.id])
	}
	return out, nil
}

func (s *pgProfessionalStore) GetProfesional(ctx context.Context, clinicaIdent, id string) (Professional, error) {
	list, err := s.ListProfesionales(ctx, clinicaIdent)
	if err != nil {
		return Professional{}, err
	}
	for _, p := range list {
		if p.ID == id {
			return p, nil
		}
	}
	return Professional{}, errProfesionalFueraDeClinica
}

func (s *pgProfessionalStore) SetProfesionalActivo(ctx context.Context, clinicaIdent, id string, activo bool) (Professional, error) {
	var p Professional
	err := s.db.QueryRowContext(ctx, `
UPDATE "Profesional" p
SET "activo" = $3
FROM "Clinica" c
WHERE p."id" = $2
  AND p."clinicaId" = c."id"
  AND c."identificador" = $1
RETURNING p."id"::text, p."usuarioId"::text, p."especialidadId"::text, p."activo",
          (SELECT u."idpSub" FROM "Usuario" u WHERE u."id" = p."usuarioId"),
          (SELECT e."nombre" FROM "Especialidad" e WHERE e."id" = p."especialidadId")`,
		clinicaIdent, id, activo,
	).Scan(&p.ID, &p.UsuarioID, &p.EspecialidadID, &p.Activo, &p.IdpSub, &p.EspecialidadNombre)
	if errors.Is(err, sql.ErrNoRows) {
		return Professional{}, errProfesionalFueraDeClinica
	}
	if err != nil {
		return Professional{}, fmt.Errorf("activar/desactivar profesional: %w", err)
	}
	return p, nil
}

// SetServicios reemplaza el conjunto de servicios habilitados del profesional
// en una transacción (RF4). Valida cada servicio contra la especialidad del
// profesional antes de insertar; el trigger del motor vuelve a comprobarlo.
func (s *pgProfessionalStore) SetServicios(ctx context.Context, clinicaIdent, profesionalID string, servicioIDs []string) ([]ProfessionalService, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("abrir transacción: %w", err)
	}
	defer tx.Rollback()

	// Profesional de la clínica + su especialidad (bloqueo para evitar
	// carreras con un cambio de especialidad en otra transacción).
	var especialidadID string
	err = tx.QueryRowContext(ctx, `
SELECT p."especialidadId"::text
FROM "Profesional" p
JOIN "Clinica" c ON c."id" = p."clinicaId"
WHERE c."identificador" = $1 AND p."id" = $2
FOR UPDATE OF p`,
		clinicaIdent, profesionalID,
	).Scan(&especialidadID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errProfesionalFueraDeClinica
	}
	if err != nil {
		return nil, fmt.Errorf("consultar profesional: %w", err)
	}

	// Valida los servicios pedidos: activos, de la clínica y de la misma
	// especialidad (RF4: "los servicios que está habilitado para atender").
	out := make([]ProfessionalService, 0, len(servicioIDs))
	seen := map[string]bool{}
	for _, sid := range servicioIDs {
		if seen[sid] {
			continue // el PK compuesto tolera duplicados; se normaliza aquí
		}
		seen[sid] = true
		var svc ProfessionalService
		var svcEsp string
		err := tx.QueryRowContext(ctx, `
SELECT s."id"::text, s."nombre", s."duracionMinutos", s."especialidadId"::text
FROM "Servicio" s
JOIN "Clinica" c ON c."id" = s."clinicaId"
WHERE c."identificador" = $1 AND s."id" = $2 AND s."activo" = true`,
			clinicaIdent, sid,
		).Scan(&svc.ID, &svc.Nombre, &svc.DuracionMinutos, &svcEsp)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errServicioFuera
		}
		if err != nil {
			return nil, fmt.Errorf("consultar servicio: %w", err)
		}
		if svcEsp != especialidadID {
			return nil, errServicioIncompatible
		}
		out = append(out, svc)
	}

	// Reemplazo atómico del conjunto.
	if _, err := tx.ExecContext(ctx, `
DELETE FROM "ProfesionalServicio"
WHERE "profesionalId" = $1
  AND "clinicaId" = (SELECT "id" FROM "Clinica" WHERE "identificador" = $2)`,
		profesionalID, clinicaIdent); err != nil {
		return nil, fmt.Errorf("limpiar servicios: %w", err)
	}
	for _, svc := range out {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO "ProfesionalServicio" ("clinicaId", "profesionalId", "servicioId")
VALUES ((SELECT "id" FROM "Clinica" WHERE "identificador" = $1), $2, $3)`,
			clinicaIdent, profesionalID, svc.ID); err != nil {
			return nil, fmt.Errorf("asociar servicio: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("confirmar transacción: %w", err)
	}
	return out, nil
}

// professionalStore es el store global (inyectable en tests).
var professionalStore ProfessionalStore

// --- Handlers ---

type createProfesionalRequest struct {
	UsuarioID      string `json:"usuarioId"`
	EspecialidadID string `json:"especialidadId"`
}

func (r *createProfesionalRequest) validate() []string {
	var errs []string
	if r.UsuarioID == "" {
		errs = append(errs, "usuarioId es obligatorio")
	}
	if r.EspecialidadID == "" {
		errs = append(errs, "especialidadId es obligatorio")
	}
	return errs
}

// createProfesional maneja POST /profesionales (RF4).
func createProfesional(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, professionalStore == nil, "servicio de profesionales no disponible")
	if !ok {
		return
	}
	var req createProfesionalRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo JSON inválido", "detalles": []string{err.Error()}})
		return
	}
	if errs := req.validate(); len(errs) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida", "detalles": errs})
		return
	}

	p, err := professionalStore.CreateProfesional(c.Request.Context(), clinica, req.UsuarioID, req.EspecialidadID)
	switch {
	case errors.Is(err, errProfesionalDuplicado):
		c.JSON(http.StatusConflict, gin.H{"error": errProfesionalDuplicado.Error()})
	case errors.Is(err, errUsuarioNoProfesional):
		c.JSON(http.StatusBadRequest, gin.H{"error": errUsuarioNoProfesional.Error()})
	case errors.Is(err, errEspecialidadInvalida):
		c.JSON(http.StatusBadRequest, gin.H{"error": errEspecialidadInvalida.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo registrar el profesional"})
	default:
		c.JSON(http.StatusCreated, gin.H{
			"clinicaIdentificador": clinica,
			"profesional":          p,
		})
	}
}

// listProfesionales maneja GET /profesionales (RF4).
func listProfesionales(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, professionalStore == nil, "servicio de profesionales no disponible")
	if !ok {
		return
	}
	profs, err := professionalStore.ListProfesionales(c.Request.Context(), clinica)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudieron listar los profesionales"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"clinicaIdentificador": clinica,
		"profesionales":        profs,
	})
}

type setServiciosRequest struct {
	ServicioIDs []string `json:"servicioIds"`
}

// setProfesionalServicios maneja PUT /profesionales/:id/servicios (RF4).
func setProfesionalServicios(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, professionalStore == nil, "servicio de profesionales no disponible")
	if !ok {
		return
	}
	var req setServiciosRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo JSON inválido", "detalles": []string{err.Error()}})
		return
	}
	if req.ServicioIDs == nil {
		req.ServicioIDs = []string{} // lista vacía = sin servicios habilitados
	}

	svcs, err := professionalStore.SetServicios(c.Request.Context(), clinica, c.Param("id"), req.ServicioIDs)
	switch {
	case errors.Is(err, errProfesionalFueraDeClinica):
		c.JSON(http.StatusNotFound, gin.H{"error": "profesional no encontrado"})
	case errors.Is(err, errServicioFuera):
		c.JSON(http.StatusBadRequest, gin.H{"error": errServicioFuera.Error()})
	case errors.Is(err, errServicioIncompatible):
		c.JSON(http.StatusBadRequest, gin.H{"error": errServicioIncompatible.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudieron actualizar los servicios"})
	default:
		c.JSON(http.StatusOK, gin.H{
			"clinicaIdentificador": clinica,
			"servicios":            svcs,
		})
	}
}

type updateProfesionalRequest struct {
	Activo *bool `json:"activo"`
}

// updateProfesional maneja PATCH /profesionales/:id (baja lógica, RF4).
func updateProfesional(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, professionalStore == nil, "servicio de profesionales no disponible")
	if !ok {
		return
	}
	var req updateProfesionalRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo JSON inválido", "detalles": []string{err.Error()}})
		return
	}
	if req.Activo == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida", "detalles": []string{"indicar activo"}})
		return
	}

	p, err := professionalStore.SetProfesionalActivo(c.Request.Context(), clinica, c.Param("id"), *req.Activo)
	switch {
	case errors.Is(err, errProfesionalFueraDeClinica):
		c.JSON(http.StatusNotFound, gin.H{"error": "profesional no encontrado"})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo actualizar el profesional"})
	default:
		c.JSON(http.StatusOK, gin.H{
			"clinicaIdentificador": clinica,
			"profesional":          p,
		})
	}
}
