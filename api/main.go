package main

import (
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

func setupRouter() *gin.Engine {
	r := gin.Default()

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "OK"})
	})

	// Listo si la config de Keycloak está presente (sin exigir Postgres aún).
	r.GET("/ready", func(c *gin.Context) {
		if !authConfigured() {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "DOWN",
				"error":  "falta KC_ISSUER_URL o KC_JWKS_URL",
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "OK"})
	})

	r.GET("/me", authRequired(), func(c *gin.Context) {
		id, ok := identityFromContext(c)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "identidad no disponible"})
			return
		}
		var clinica any
		if id.ClinicaID == "" {
			clinica = nil
		} else {
			clinica = id.ClinicaID
		}
		c.JSON(http.StatusOK, gin.H{
			"sub":       id.Sub,
			"username":  id.Username,
			"roles":     id.Roles,
			"clinicaId": clinica,
		})
	})

	return r
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	if err := setupRouter().Run(":" + port); err != nil {
		panic(err)
	}
}
