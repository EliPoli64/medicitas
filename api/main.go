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

	// requestId por petición: correlaciona la bitácora (RF18).
	r.Use(requestID())

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

	// RF14 lectura + RF16 historial; escritura con OpPacientesEscritura.
	r.GET("/pacientes", append(secure(OpPacientesLectura), listPacientes)...)
	r.POST("/pacientes", append(secure(OpPacientesEscritura), createPaciente)...)
	r.GET("/pacientes/:id", append(secure(OpPacientesLectura), getPaciente)...)
	r.PATCH("/pacientes/:id", append(secure(OpPacientesEscritura), updatePaciente)...)
	r.POST("/pacientes/:id/contactos", append(secure(OpPacientesEscritura), createContacto)...)
	r.GET("/pacientes/:id/citas", append(secure(OpHistorialPaciente), listPacienteCitas)...)
	r.PATCH("/contactos/:id", append(secure(OpPacientesEscritura), updateContacto)...)

	// RF1: alta de clínica + primer administrador (matriz: solo adm-plataforma).
	r.POST("/clinicas", append(secure(OpAltaClinica), createClinica)...)

	// RF2: gestión de usuarios de la clínica (matriz: adm-clinica).
	r.GET("/usuarios", append(secure(OpGestionCatalogo), listUsuarios)...)
	r.POST("/usuarios", append(secure(OpGestionCatalogo), createUsuario)...)
	r.PATCH("/usuarios/:id", append(secure(OpGestionCatalogo), updateUsuario)...)

	// RF3: catálogo de servicios y especialidades (matriz: adm-clinica).
	r.GET("/especialidades", append(secure(OpGestionCatalogo), listEspecialidades)...)
	r.GET("/servicios", append(secure(OpGestionCatalogo), listServicios)...)
	r.POST("/servicios", append(secure(OpGestionCatalogo), createServicio)...)
	r.PATCH("/servicios/:id", append(secure(OpGestionCatalogo), updateServicio)...)

	// RF4: profesionales y servicios habilitados (matriz: adm-clinica).
	r.GET("/profesionales", append(secure(OpGestionCatalogo), listProfesionales)...)
	r.POST("/profesionales", append(secure(OpGestionCatalogo), createProfesional)...)
	r.PATCH("/profesionales/:id", append(secure(OpGestionCatalogo), updateProfesional)...)
	r.PUT("/profesionales/:id/servicios", append(secure(OpGestionCatalogo), setProfesionalServicios)...)

	// RF7/RF8: disponibilidad (matriz: adm-clinica, recepcion, profesional).
	r.GET("/profesionales/:id/disponibilidad", append(secure(OpConsultarDisponibilidad), listDisponibilidadProfesional)...)
	r.GET("/servicios/:id/disponibilidad", append(secure(OpConsultarDisponibilidad), listDisponibilidadServicio)...)

	// RF5: horarios semanales (adm-clinica o profesional propio).
	r.GET("/profesionales/:id/horarios", append(secure(OpHorariosBloqueos), listHorarios)...)
	r.POST("/profesionales/:id/horarios", append(secure(OpHorariosBloqueos), createHorario)...)
	r.PATCH("/horarios/:id", append(secure(OpHorariosBloqueos), updateHorario)...)

	// RF6: bloqueos + conflictos de citas reservadas.
	r.GET("/profesionales/:id/bloqueos", append(secure(OpHorariosBloqueos), listBloqueos)...)
	r.POST("/profesionales/:id/bloqueos", append(secure(OpHorariosBloqueos), createBloqueo)...)
	r.DELETE("/bloqueos/:id", append(secure(OpHorariosBloqueos), deleteBloqueo)...)

	// RF15: nota clínica (solo el profesional que atendió la cita).
	r.POST("/citas/:id/nota", append(secure(OpNotaClinica), createNota)...)
	r.GET("/citas/:id/nota", append(secure(OpNotaClinica), getNota)...)

	// RF20: agenda del día (zona de la clínica).
	r.GET("/agenda", append(secure(OpAgendaDia), getAgenda)...)
	// RF21: conteo de citas por estado.
	r.GET("/citas/resumen", append(secure(OpConteoCitas), getCitasResumen)...)

	// RF19: consulta de la bitácora (matriz: adm-clinica).
	r.GET("/bitacora", append(secure(OpBitacora), listBitacora)...)

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
	clinicStore = newPGClinicStore(db)
	userAdminStore = newPGUserAdminStore(db)
	serviceStore = newPGServiceStore(db)
	professionalStore = newPGProfessionalStore(db)
	disponibilidadStore = newPGDisponibilidadStore(db)
	scheduleStore = newPGScheduleStore(db)
	blockStore = newPGBlockStore(db)
	agendaStore = newPGAgendaStore(db)
	statsStore = newPGStatsStore(db)

	// Stores de notas clínicas y auditoría (RF15, RF18-RF19).
	notaStore = newPGNotaStore(db)
	auditStore = newPGAuditStore(db)

	// Caché de disponibilidad (RF7/RF8). Todavía no se usa en los handlers.
	// Una configuración inválida es fatal; una caída de Redis solo deshabilita
	// la caché y la API sigue sirviendo desde PostgreSQL.
	if cacheRuntime, err := newAvailabilityCacheFromEnv(context.Background()); err != nil {
		if isAvailabilityCacheConfigError(err) {
			log.Fatalf("redis: configuración inválida: %v", err)
		}
		log.Printf("redis: caché de disponibilidad deshabilitada: %v", err)
	} else {
		availabilityCache = cacheRuntime.Cache
		defer cacheRuntime.Close()
		log.Printf("redis: caché de disponibilidad habilitada en %s (db %d)", cacheRuntime.Addr, cacheRuntime.DB)
	}

	identityAdmin = newHTTPIdentityAdminFromEnv()
	if err := setupRouter().Run(":" + port); err != nil {
		panic(err)
	}
}
