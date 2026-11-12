package main

// RF2: gestionar los usuarios de una clínica — alta, baja lógica y asignación
// de rol. Un usuario pertenece a exactamente una clínica (la del token: I5);
// el administrador de plataforma no se crea por esta vía.
//
// Nota de modelo (spec §2): el dominio NO guarda username ni credenciales.
// "Usuario" conserva solo idpSub, clínica y rol; el nombre de usuario vive en
// Keycloak. Por eso las salidas exponen idpSub, no el login.
//
// Orden IdP → Postgres con compensación (mismo patrón que RF1): el cambio de
// rol y la baja lógica se aplican primero en Keycloak y luego en la BD; si la
// BD falla, se revierte el paso de Keycloak.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

// realmRoleByDBRole traduce "Rol"."codigo" (BD) al rol de realm de Keycloak.
// Mismo mapa que KEYCLOAK_ROLE_BY_DB del seed.
var realmRoleByDBRole = map[string]string{
	"adm_plataforma": "adm-plataforma",
	"adm_clinica":    "adm-clinica",
	"recepcion":      "recepcion",
	"profesional":    "profesional",
}

// rolesAsignables: un adm-clinica puede crear/gestionar estos roles dentro de
// SU clínica. adm_plataforma queda fuera: definir administradores de plataforma
// no es potestad de una clínica (matriz RF17: RF2 solo adm-clinica).
var rolesAsignables = map[string]bool{
	"adm_clinica": true,
	"recepcion":   true,
	"profesional": true,
}

var (
	errUsuarioFueraDeClinica   = errors.New("usuario no encontrado en la clínica")
	errRolNoAsignable          = errors.New("rol no asignable desde una clínica")
	errRolConflictoProfesional = errors.New("el usuario está registrado como profesional activo; desactívelo antes de cambiar su rol")
)

// AdminUser es la proyección de "Usuario" para la gestión (RF2).
type AdminUser struct {
	ID        string `json:"id"`
	IdpSub    string `json:"idpSub"`
	RolCodigo string `json:"rolCodigo"`
	Activo    bool   `json:"activo"`
	CreadoEn  string `json:"creadoEn"`
}

// UserAdminStore abstrae la persistencia para tests sin Postgres.
type UserAdminStore interface {
	CreateUsuario(ctx context.Context, clinicaIdent, idpSub, rolCodigo string) (AdminUser, error)
	ListUsuarios(ctx context.Context, clinicaIdent string) ([]AdminUser, error)
	GetUsuario(ctx context.Context, clinicaIdent, id string) (AdminUser, error)
	// UpdateUsuario aplica rol (opcional) y/o activo (opcional). Nil = sin cambio.
	UpdateUsuario(ctx context.Context, clinicaIdent, id string, rol *string, activo *bool) (AdminUser, error)
}

type pgUserAdminStore struct {
	db *sql.DB
}

func newPGUserAdminStore(database *sql.DB) *pgUserAdminStore {
	return &pgUserAdminStore{db: database}
}

func (s *pgUserAdminStore) CreateUsuario(ctx context.Context, clinicaIdent, idpSub, rolCodigo string) (AdminUser, error) {
	var u AdminUser
	err := s.db.QueryRowContext(ctx, `
INSERT INTO "Usuario" ("idpSub", "clinicaId", "rolId")
VALUES ($1,
        (SELECT "id" FROM "Clinica" WHERE "identificador" = $2),
        (SELECT "id" FROM "Rol" WHERE "codigo" = $3))
RETURNING "id"::text, "idpSub", "activo", "creadoEn"::text`,
		idpSub, clinicaIdent, rolCodigo,
	).Scan(&u.ID, &u.IdpSub, &u.Activo, &u.CreadoEn)
	if isUniqueViolation(err, "UsuarioIdpSubUq") {
		return AdminUser{}, errUsuarioExists
	}
	if err != nil {
		return AdminUser{}, fmt.Errorf("insertar usuario: %w", err)
	}
	u.RolCodigo = rolCodigo
	return u, nil
}

func (s *pgUserAdminStore) ListUsuarios(ctx context.Context, clinicaIdent string) ([]AdminUser, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT u."id"::text, u."idpSub", r."codigo", u."activo", u."creadoEn"::text
FROM "Usuario" u
JOIN "Clinica" c ON c."id" = u."clinicaId"
JOIN "Rol"     r ON r."id" = u."rolId"
WHERE c."identificador" = $1
ORDER BY u."creadoEn"`,
		clinicaIdent)
	if err != nil {
		return nil, fmt.Errorf("listar usuarios: %w", err)
	}
	defer rows.Close()

	out := make([]AdminUser, 0)
	for rows.Next() {
		var u AdminUser
		if err := rows.Scan(&u.ID, &u.IdpSub, &u.RolCodigo, &u.Activo, &u.CreadoEn); err != nil {
			return nil, fmt.Errorf("leer usuario: %w", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterar usuarios: %w", err)
	}
	return out, nil
}

func (s *pgUserAdminStore) GetUsuario(ctx context.Context, clinicaIdent, id string) (AdminUser, error) {
	var u AdminUser
	err := s.db.QueryRowContext(ctx, `
SELECT u."id"::text, u."idpSub", r."codigo", u."activo", u."creadoEn"::text
FROM "Usuario" u
JOIN "Clinica" c ON c."id" = u."clinicaId"
JOIN "Rol"     r ON r."id" = u."rolId"
WHERE c."identificador" = $1 AND u."id" = $2`,
		clinicaIdent, id,
	).Scan(&u.ID, &u.IdpSub, &u.RolCodigo, &u.Activo, &u.CreadoEn)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminUser{}, errUsuarioFueraDeClinica
	}
	if err != nil {
		return AdminUser{}, fmt.Errorf("consultar usuario: %w", err)
	}
	return u, nil
}

func (s *pgUserAdminStore) UpdateUsuario(ctx context.Context, clinicaIdent, id string, rol *string, activo *bool) (AdminUser, error) {
	var rolID any
	if rol != nil {
		rolID = *rol
	}
	var activoVal sql.NullBool
	if activo != nil {
		activoVal = sql.NullBool{Valid: true, Bool: *activo}
	}
	var u AdminUser
	err := s.db.QueryRowContext(ctx, `
UPDATE "Usuario" u
SET "rolId"  = COALESCE((SELECT r."id" FROM "Rol" r WHERE r."codigo" = $3), u."rolId"),
    "activo" = COALESCE($4, u."activo")
FROM "Clinica" c
WHERE u."id" = $2
  AND u."clinicaId" = c."id"
  AND c."identificador" = $1
RETURNING u."id"::text, u."idpSub",
          (SELECT r."codigo" FROM "Rol" r WHERE r."id" = u."rolId"),
          u."activo", u."creadoEn"::text`,
		clinicaIdent, id, rolID, activoVal,
	).Scan(&u.ID, &u.IdpSub, &u.RolCodigo, &u.Activo, &u.CreadoEn)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminUser{}, errUsuarioFueraDeClinica
	}
	if err != nil {
		return AdminUser{}, fmt.Errorf("actualizar usuario: %w", err)
	}
	return u, nil
}

// userAdminStore es el store global (inyectable en tests).
var userAdminStore UserAdminStore

// --- Handlers ---

type createUsuarioRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Email    string `json:"email"`
	Nombre   string `json:"nombre"`
	Apellido string `json:"apellido"`
	Rol      string `json:"rol"`
}

func (r *createUsuarioRequest) validate() []string {
	var errs []string
	if !usernameRe.MatchString(r.Username) {
		errs = append(errs, "username es obligatorio (3-64 caracteres: letras, dígitos, . _ -)")
	}
	if len(r.Password) < 8 {
		errs = append(errs, "password debe tener al menos 8 caracteres")
	}
	if !rolesAsignables[r.Rol] {
		errs = append(errs, "rol debe ser adm_clinica, recepcion o profesional")
	}
	if r.Email != "" && !containsAt(r.Email) {
		errs = append(errs, "email no es un correo válido")
	}
	return errs
}

// createUsuario maneja POST /usuarios (RF2, alta).
func createUsuario(c *gin.Context) {
	if userAdminStore == nil || identityAdmin == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de usuarios no disponible"})
		return
	}
	du, ok := domainUserFromContext(c)
	if !ok || du.ClinicaIdentificador == nil || *du.ClinicaIdentificador == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "usuario sin clínica"})
		return
	}
	clinica := *du.ClinicaIdentificador

	var req createUsuarioRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo JSON inválido", "detalles": []string{err.Error()}})
		return
	}
	if errs := req.validate(); len(errs) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida", "detalles": errs})
		return
	}

	// 1) Identidad primero (idpSub NOT NULL): rol de realm + claim de clínica.
	sub, err := identityAdmin.CreateUser(c.Request.Context(), NewIDPUser{
		Username:             req.Username,
		Password:             req.Password,
		Email:                emailOr(req.Email, req.Username),
		FirstName:            nombreOr(req.Nombre, "Usuario"),
		LastName:             nombreOr(req.Apellido, req.Rol),
		ClinicaIdentificador: clinica,
		RealmRole:            realmRoleByDBRole[req.Rol],
	})
	if errors.Is(err, errIDPUserExists) {
		c.JSON(http.StatusConflict, gin.H{"error": "el nombre de usuario ya existe en el proveedor de identidad"})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "no se pudo crear la identidad en el proveedor"})
		return
	}

	// 2) Dominio: fila Usuario anclada a la clínica del token.
	user, err := userAdminStore.CreateUsuario(c.Request.Context(), clinica, sub, req.Rol)
	if err != nil {
		if derr := identityAdmin.DeleteUser(c.Request.Context(), sub); derr != nil {
			log.Printf("rf2: compensación fallida al eliminar identidad %s: %v", sub, derr)
		}
		if errors.Is(err, errUsuarioExists) {
			c.JSON(http.StatusConflict, gin.H{"error": errUsuarioExists.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo registrar el usuario"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"clinicaIdentificador": clinica,
		"usuario":              user,
	})
}

// listUsuarios maneja GET /usuarios (RF2).
func listUsuarios(c *gin.Context) {
	if userAdminStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de usuarios no disponible"})
		return
	}
	du, ok := domainUserFromContext(c)
	if !ok || du.ClinicaIdentificador == nil || *du.ClinicaIdentificador == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "usuario sin clínica"})
		return
	}
	clinica := *du.ClinicaIdentificador
	users, err := userAdminStore.ListUsuarios(c.Request.Context(), clinica)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudieron listar los usuarios"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"clinicaIdentificador": clinica,
		"usuarios":             users,
	})
}

type updateUsuarioRequest struct {
	Rol    *string `json:"rol"`
	Activo *bool   `json:"activo"`
}

// updateUsuario maneja PATCH /usuarios/:id (RF2: rol y/o baja lógica).
func updateUsuario(c *gin.Context) {
	if userAdminStore == nil || identityAdmin == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de usuarios no disponible"})
		return
	}
	du, ok := domainUserFromContext(c)
	if !ok || du.ClinicaIdentificador == nil || *du.ClinicaIdentificador == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "usuario sin clínica"})
		return
	}
	clinica := *du.ClinicaIdentificador

	var req updateUsuarioRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo JSON inválido", "detalles": []string{err.Error()}})
		return
	}
	if req.Rol == nil && req.Activo == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida", "detalles": []string{"indicar rol y/o activo"}})
		return
	}
	if req.Rol != nil && !rolesAsignables[*req.Rol] {
		c.JSON(http.StatusBadRequest, gin.H{"error": errRolNoAsignable.Error()})
		return
	}

	// El objetivo se resuelve SIEMPRE dentro de la clínica del token (I5):
	// un id de otra clínica responde 404, sin filtrar su existencia.
	target, err := userAdminStore.GetUsuario(c.Request.Context(), clinica, c.Param("id"))
	if errors.Is(err, errUsuarioFueraDeClinica) {
		c.JSON(http.StatusNotFound, gin.H{"error": "usuario no encontrado"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo consultar el usuario"})
		return
	}

	// Cambiar el rol de un profesional activo rompería la coherencia con
	// "Profesional" (trigger validarRolProfesional); se rechaza con 409.
	if req.Rol != nil && *req.Rol != "profesional" && target.RolCodigo == "profesional" {
		c.JSON(http.StatusConflict, gin.H{"error": errRolConflictoProfesional.Error()})
		return
	}

	// 1) Keycloak primero; se guardan los valores previos para compensar.
	prevRol := target.RolCodigo
	prevActivo := target.Activo
	if req.Rol != nil && *req.Rol != target.RolCodigo {
		if err := identityAdmin.SetRealmRole(c.Request.Context(), target.IdpSub, realmRoleByDBRole[*req.Rol]); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "no se pudo actualizar el rol en el proveedor de identidad"})
			return
		}
	}
	if req.Activo != nil && *req.Activo != target.Activo {
		if err := identityAdmin.SetEnabled(c.Request.Context(), target.IdpSub, *req.Activo); err != nil {
			// Revierte el rol si ya se había cambiado.
			if req.Rol != nil && *req.Rol != prevRol {
				_ = identityAdmin.SetRealmRole(c.Request.Context(), target.IdpSub, realmRoleByDBRole[prevRol])
			}
			c.JSON(http.StatusBadGateway, gin.H{"error": "no se pudo actualizar la identidad en el proveedor"})
			return
		}
	}

	// 2) Dominio.
	updated, err := userAdminStore.UpdateUsuario(c.Request.Context(), clinica, target.ID, req.Rol, req.Activo)
	if err != nil {
		// Compensación: restaurar el estado previo en Keycloak.
		if req.Rol != nil && *req.Rol != prevRol {
			if cerr := identityAdmin.SetRealmRole(c.Request.Context(), target.IdpSub, realmRoleByDBRole[prevRol]); cerr != nil {
				log.Printf("rf2: compensación fallida al restaurar rol de %s: %v", target.IdpSub, cerr)
			}
		}
		if req.Activo != nil && *req.Activo != prevActivo {
			if cerr := identityAdmin.SetEnabled(c.Request.Context(), target.IdpSub, prevActivo); cerr != nil {
				log.Printf("rf2: compensación fallida al restaurar estado de %s: %v", target.IdpSub, cerr)
			}
		}
		if errors.Is(err, errUsuarioFueraDeClinica) {
			c.JSON(http.StatusNotFound, gin.H{"error": "usuario no encontrado"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo actualizar el usuario"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"clinicaIdentificador": clinica,
		"usuario":              updated,
	})
}

func containsAt(s string) bool {
	for _, r := range s {
		if r == '@' {
			return true
		}
	}
	return false
}

func emailOr(email, username string) string {
	if email != "" {
		return email
	}
	return username + "@medicitas.local"
}

func nombreOr(v, def string) string {
	if v != "" {
		return v
	}
	return def
}
