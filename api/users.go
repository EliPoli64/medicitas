package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

const ctxDomainUserKey = "domainUser"

var (
	errUserNotFound = errors.New("usuario no registrado")
	errUserInactive = errors.New("usuario inactivo")
)

// DomainUser es la fila de dominio ligada al JWT via idpSub (fuente de verdad del tenant).
type DomainUser struct {
	UsuarioID            string  `json:"usuarioId"`
	IdpSub               string  `json:"idpSub"`
	Activo               bool    `json:"activo"`
	ClinicaIdentificador *string `json:"clinicaIdentificador"` // null si adm plataforma
	RolCodigo            string  `json:"rolCodigo"`            // código BD (adm_plataforma, …)
}

// UserStore abstrae el lookup para tests sin Postgres.
type UserStore interface {
	FindByIdpSub(ctx context.Context, idpSub string) (DomainUser, error)
}

type pgUserStore struct {
	db *sql.DB
}

func newPGUserStore(database *sql.DB) *pgUserStore {
	return &pgUserStore{db: database}
}

func (s *pgUserStore) FindByIdpSub(ctx context.Context, idpSub string) (DomainUser, error) {
	const q = `
SELECT u."id"::text, u."idpSub", u."activo",
       c."identificador", r."codigo"
FROM "Usuario" u
LEFT JOIN "Clinica" c ON c."id" = u."clinicaId"
JOIN "Rol" r ON r."id" = u."rolId"
WHERE u."idpSub" = $1`

	var u DomainUser
	var clinica sql.NullString
	err := s.db.QueryRowContext(ctx, q, idpSub).Scan(
		&u.UsuarioID, &u.IdpSub, &u.Activo, &clinica, &u.RolCodigo,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return DomainUser{}, errUserNotFound
	}
	if err != nil {
		return DomainUser{}, fmt.Errorf("consultar usuario: %w", err)
	}
	if clinica.Valid {
		s := clinica.String
		u.ClinicaIdentificador = &s
	}
	if !u.Activo {
		return u, errUserInactive
	}
	return u, nil
}

// userStore es el store global (inyectable en tests).
var userStore UserStore

// loadDomainUser resuelve Usuario por JWT.sub (= idpSub). Va después de authRequired.
func loadDomainUser() gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := identityFromContext(c)
		if !ok || id.Sub == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "token inválido o ausente"})
			return
		}
		if userStore == nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "almacén de usuarios no disponible"})
			return
		}
		du, err := userStore.FindByIdpSub(c.Request.Context(), id.Sub)
		if errors.Is(err, errUserNotFound) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "usuario no registrado en el sistema"})
			return
		}
		if errors.Is(err, errUserInactive) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "usuario inactivo"})
			return
		}
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "no se pudo resolver el usuario"})
			return
		}
		if err := assertClinicaClaimAligned(id, du); err != nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		c.Set(ctxDomainUserKey, du)
		c.Next()
	}
}

// assertClinicaClaimAligned: la clínica de dominio es Postgres. Si el claim JWT
// trae clinica_id y no coincide con la BD, se rechaza (I5 / token desfasado).
func assertClinicaClaimAligned(id Identity, du DomainUser) error {
	claim := id.ClinicaID
	var domain string
	if du.ClinicaIdentificador != nil {
		domain = *du.ClinicaIdentificador
	}
	if claim == "" {
		return nil // sin claim: se usa solo BD (o plataforma sin clínica)
	}
	if claim != domain {
		return fmt.Errorf("clínica del token no coincide con el usuario registrado")
	}
	return nil
}

// secure encadena permiso de matriz + Usuario de dominio (patrón de rutas de negocio).
func secure(op Operation) []gin.HandlerFunc {
	return []gin.HandlerFunc{requirePerm(op), loadDomainUser()}
}

func domainUserFromContext(c *gin.Context) (DomainUser, bool) {
	v, ok := c.Get(ctxDomainUserKey)
	if !ok {
		return DomainUser{}, false
	}
	u, ok := v.(DomainUser)
	return u, ok
}
