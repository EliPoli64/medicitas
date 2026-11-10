package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
)

func setupRouter() *gin.Engine {
	r := gin.Default()

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "OK"})
	})

	// Listo si Keycloak está configurado y Postgres responde a Ping.
	r.GET("/ready", func(c *gin.Context) {
		if !authConfigured() {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "DOWN",
				"error":  "falta KC_ISSUER_URL o KC_JWKS_URL",
			})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := pingDB(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "DOWN",
				"error":  "postgres no disponible",
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "OK"})
	})

	r.GET("/me", authRequired(), loadDomainUser(), func(c *gin.Context) {
		id, ok := identityFromContext(c)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "identidad no disponible"})
			return
		}
		du, ok := domainUserFromContext(c)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "usuario de dominio no disponible"})
			return
		}
		var clinicaJWT any
		if id.ClinicaID == "" {
			clinicaJWT = nil
		} else {
			clinicaJWT = id.ClinicaID
		}
		c.JSON(http.StatusOK, gin.H{
			"sub":                  id.Sub,
			"username":             id.Username,
			"roles":                id.Roles,
			"clinicaIdClaim":       clinicaJWT,
			"usuarioId":            du.UsuarioID,
			"idpSub":               du.IdpSub,
			"activo":               du.Activo,
			"clinicaIdentificador": du.ClinicaIdentificador,
			"rolCodigo":            du.RolCodigo,
		})
	})

	// Lectura de pacientes de la clínica del Usuario de dominio (I5: tenant no viene del query).
	r.GET("/pacientes", requirePerm(OpPacientesLectura), loadDomainUser(), listPacientes)

	// Capacidades según matriz RF17; exige también Usuario en Postgres.
	r.GET("/permisos", authRequired(), loadDomainUser(), func(c *gin.Context) {
		id, ok := identityFromContext(c)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "identidad no disponible"})
			return
		}
		du, ok := domainUserFromContext(c)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "usuario de dominio no disponible"})
			return
		}
		ops := AllowedOperations(id.Roles)
		names := make([]string, len(ops))
		for i, op := range ops {
			names[i] = string(op)
		}
		c.JSON(http.StatusOK, gin.H{
			"roles":                id.Roles,
			"rolCodigo":            du.RolCodigo,
			"usuarioId":            du.UsuarioID,
			"clinicaIdentificador": du.ClinicaIdentificador,
			"operaciones":          names,
		})
	})

	return r
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	if err := initDB(); err != nil {
		log.Fatalf("postgres: %v", err)
	}
	userStore = newPGUserStore(db)
	patientStore = newPGPatientStore(db)
	if err := setupRouter().Run(":" + port); err != nil {
		panic(err)
	}
}
