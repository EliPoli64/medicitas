//go:build integration

package main

// Pruebas de integración del versionado autoritativo de agendas en PostgreSQL.
// Requieren un PostgreSQL real con las migraciones 01_schema.sql y
// 02_agenda_version.sql aplicadas.
//
// Variables de entorno (con defaults para el contenedor aislado):
//   DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, DB_NAME.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// agendaVersionDB abre una conexión directa a PostgreSQL.
func agendaVersionDB(t testing.TB) *sql.DB {
	t.Helper()
	host := envOr("DB_HOST", "localhost")
	port := envOr("DB_PORT", "5432")
	user := envOr("DB_USER", envOr("POSTGRES_USER", "medicitas"))
	pass := envOr("DB_PASSWORD", envOr("POSTGRES_PASSWORD", "cambiar_en_local"))
	name := envOr("DB_NAME", envOr("POSTGRES_DB", "medicitas"))

	conn, err := sql.Open("pgx", postgresDSN(user, pass, host, port, name))
	if err != nil {
		t.Fatalf("abrir postgres: %v", err)
	}
	if err := conn.Ping(); err != nil {
		t.Fatalf("conexión a postgres: %v", err)
	}
	return conn
}

func postgresDSN(user, pass, host, port, name string) string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, pass),
		Host:   net.JoinHostPort(host, port),
		Path:   "/" + name,
	}
	q := u.Query()
	q.Set("sslmode", "disable")
	u.RawQuery = q.Encode()
	return u.String()
}

// versionDeAgenda lee la versión autoritativa de una agenda.
func versionDeAgenda(t *testing.T, db *sql.DB, clinicaID, profesionalID string) int64 {
	t.Helper()
	var v int64
	err := db.QueryRow(
		`SELECT "version" FROM "AgendaVersion" WHERE "clinicaId" = $1::uuid AND "profesionalId" = $2::uuid`,
		clinicaID, profesionalID,
	).Scan(&v)
	if err != nil {
		t.Fatalf("leer AgendaVersion: %v", err)
	}
	return v
}

// fixturesAgendaVersion crea una clínica mínima con profesional, servicio y
// asociación. Devuelve los IDs generados.
func fixturesAgendaVersion(t testing.TB, db *sql.DB) (clinicaID, profesionalID, servicioID, usuarioID string) {
	t.Helper()
	sufNum := time.Now().UnixNano()
	ident := fmt.Sprintf("clinica-%d", sufNum)

	clinicaID = newTestUUID()
	usuarioID = newTestUUID()
	especialidadID := newTestUUID()
	profesionalID = newTestUUID()
	servicioID = newTestUUID()

	ctx := context.Background()

	// Asegura el rol de profesional; varios tests comparten el mismo rol.
	mustExec(ctx, t, db,
		`INSERT INTO "Rol" ("id", "codigo", "nombre") VALUES (gen_random_uuid(), 'profesional', 'Profesional') ON CONFLICT ("codigo") DO NOTHING`)
	var rolID string
	if err := db.QueryRow(`SELECT "id"::text FROM "Rol" WHERE "codigo" = 'profesional'`).Scan(&rolID); err != nil {
		t.Fatalf("obtener rol profesional: %v", err)
	}

	mustExec(ctx, t, db,
		`INSERT INTO "Clinica" ("id", "identificador", "nombre") VALUES ($1::uuid, $2, $3)`,
		clinicaID, ident, "Clínica "+ident)
	mustExec(ctx, t, db,
		`INSERT INTO "Usuario" ("id", "idpSub", "clinicaId", "rolId", "activo") VALUES ($1::uuid, $2, $3::uuid, $4::uuid, true)`,
		usuarioID, fmt.Sprintf("idp-%d", sufNum), clinicaID, rolID)
	mustExec(ctx, t, db,
		`INSERT INTO "Especialidad" ("id", "codigo", "nombre") VALUES ($1::uuid, $2, $3)`,
		especialidadID, fmt.Sprintf("esp-%d", sufNum), fmt.Sprintf("Especialidad %d", sufNum))
	mustExec(ctx, t, db,
		`INSERT INTO "Profesional" ("id", "clinicaId", "usuarioId", "especialidadId", "activo") VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, true)`,
		profesionalID, clinicaID, usuarioID, especialidadID)
	mustExec(ctx, t, db,
		`INSERT INTO "Servicio" ("id", "clinicaId", "nombre", "duracionMinutos", "especialidadId", "activo") VALUES ($1::uuid, $2::uuid, $3, 30, $4::uuid, true)`,
		servicioID, clinicaID, fmt.Sprintf("Servicio %d", sufNum), especialidadID)
	mustExec(ctx, t, db,
		`INSERT INTO "ProfesionalServicio" ("clinicaId", "profesionalId", "servicioId") VALUES ($1::uuid, $2::uuid, $3::uuid)`,
		clinicaID, profesionalID, servicioID)

	return clinicaID, profesionalID, servicioID, usuarioID
}

func newTestUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type testExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func mustExec(ctx context.Context, t testing.TB, db testExecer, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// proximoLunesLocal devuelve el próximo lunes (no hoy) a las 00:00 local de
// Costa Rica, junto con su ISO day-of-week (1 = lunes).
func proximoLunesLocal() (time.Time, int) {
	loc, _ := time.LoadLocation("America/Costa_Rica")
	now := time.Now().In(loc)
	wd := int(now.Weekday()) // domingo=0
	days := (1 - wd + 7) % 7
	if days == 0 {
		days = 7
	}
	return now.AddDate(0, 0, days), 1
}

// fixtureHorarioYPaciente crea un horario de atención de 09:00 a 12:00 en el
// próximo lunes y un paciente, para poder insertar citas válidas.
func fixtureHorarioYPaciente(t testing.TB, db *sql.DB, clinicaID, profesionalID string) (citaBase time.Time, pacienteID string) {
	t.Helper()
	ctx := context.Background()
	pacienteID = newTestUUID()
	mustExec(ctx, t, db,
		`INSERT INTO "Paciente" ("id", "clinicaId", "identificacion", "nombre", "fechaNacimiento") VALUES ($1::uuid, $2::uuid, $3, $4, '2000-01-01')`,
		pacienteID, clinicaID, fmt.Sprintf("id-%d", time.Now().UnixNano()), fmt.Sprintf("Paciente %d", time.Now().UnixNano()))

	lunes, dow := proximoLunesLocal()
	mustExec(ctx, t, db,
		`INSERT INTO "HorarioAtencion" ("clinicaId", "profesionalId", "diaSemana", "horaInicio", "horaFin", "vigenteDesde", "vigenteHasta", "activo")
		 VALUES ($1::uuid, $2::uuid, $3, '09:00:00', '13:00:00', current_date - interval '1 year', current_date + interval '1 year', true)`,
		clinicaID, profesionalID, dow)

	loc, _ := time.LoadLocation("America/Costa_Rica")
	inicioLocal := time.Date(lunes.Year(), lunes.Month(), lunes.Day(), 10, 0, 0, 0, loc)
	return inicioLocal, pacienteID
}

// insertarCitaReservada inserta una cita válida en el horario creado por
// fixtureHorarioYPaciente. El offsetEnMinutos permite desplazar el inicio
// dentro de la franja 09:00-12:00 para evitar solapes.
func insertarCitaReservada(t *testing.T, db *sql.DB, clinicaID, profesionalID, servicioID, pacienteID string, citaBase time.Time, offsetMin int) string {
	t.Helper()
	inicio := citaBase.Add(time.Duration(offsetMin) * time.Minute)
	citaID := newTestUUID()
	mustExec(context.Background(), t, db,
		`INSERT INTO "Cita" ("id", "clinicaId", "pacienteId", "profesionalId", "servicioId", "inicio", "duracionMinutos", "fin", "estado")
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::timestamptz, 30, $6::timestamptz + interval '30 minutes', 'reservada')`,
		citaID, clinicaID, pacienteID, profesionalID, servicioID, inicio.UTC())
	return citaID
}

// crearSegundoProfesional crea un profesional adicional en la misma clínica,
// habilitado para el mismo servicio y con horario de atención en la misma
// franja que el profesional original. Devuelve el id del profesional y su usuario.
func crearSegundoProfesional(t *testing.T, db *sql.DB, clinicaID, servicioID string, citaBase time.Time) (profesionalID, usuarioID string) {
	t.Helper()
	ctx := context.Background()
	sufNum := time.Now().UnixNano()
	usuarioID = newTestUUID()
	profesionalID = newTestUUID()

	var rolID, especialidadID string
	if err := db.QueryRow(`SELECT "id"::text FROM "Rol" WHERE "codigo" = 'profesional'`).Scan(&rolID); err != nil {
		t.Fatalf("obtener rol profesional: %v", err)
	}
	if err := db.QueryRow(`SELECT "especialidadId"::text FROM "Servicio" WHERE "id" = $1::uuid`, servicioID).Scan(&especialidadID); err != nil {
		t.Fatalf("obtener especialidad del servicio: %v", err)
	}

	mustExec(ctx, t, db,
		`INSERT INTO "Usuario" ("id", "idpSub", "clinicaId", "rolId", "activo") VALUES ($1::uuid, $2, $3::uuid, $4::uuid, true)`,
		usuarioID, fmt.Sprintf("idp2-%d", sufNum), clinicaID, rolID)
	mustExec(ctx, t, db,
		`INSERT INTO "Profesional" ("id", "clinicaId", "usuarioId", "especialidadId", "activo") VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, true)`,
		profesionalID, clinicaID, usuarioID, especialidadID)
	mustExec(ctx, t, db,
		`INSERT INTO "ProfesionalServicio" ("clinicaId", "profesionalId", "servicioId") VALUES ($1::uuid, $2::uuid, $3::uuid)`,
		clinicaID, profesionalID, servicioID)

	lunes, dow := proximoLunesLocal()
	_ = citaBase
	mustExec(ctx, t, db,
		`INSERT INTO "HorarioAtencion" ("clinicaId", "profesionalId", "diaSemana", "horaInicio", "horaFin", "vigenteDesde", "vigenteHasta", "activo")
		 VALUES ($1::uuid, $2::uuid, $3, '09:00:00', '13:00:00', current_date - interval '1 year', current_date + interval '1 year', true)`,
		clinicaID, profesionalID, dow)
	_ = lunes
	return profesionalID, usuarioID
}

func TestAgendaVersionCreadaAlRegistrarProfesional(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	ctx := context.Background()
	sufNum := time.Now().UnixNano()
	ident := fmt.Sprintf("clinica-%d", sufNum)
	clinicaID := newTestUUID()
	usuarioID := newTestUUID()
	especialidadID := newTestUUID()
	profesionalID := newTestUUID()

	mustExec(ctx, t, db,
		`INSERT INTO "Rol" ("id", "codigo", "nombre") VALUES (gen_random_uuid(), 'profesional', 'Profesional') ON CONFLICT ("codigo") DO NOTHING`)
	var rolID string
	if err := db.QueryRow(`SELECT "id"::text FROM "Rol" WHERE "codigo" = 'profesional'`).Scan(&rolID); err != nil {
		t.Fatalf("obtener rol profesional: %v", err)
	}
	mustExec(ctx, t, db,
		`INSERT INTO "Clinica" ("id", "identificador", "nombre") VALUES ($1::uuid, $2, $3)`,
		clinicaID, ident, "Clínica "+ident)
	mustExec(ctx, t, db,
		`INSERT INTO "Usuario" ("id", "idpSub", "clinicaId", "rolId", "activo") VALUES ($1::uuid, $2, $3::uuid, $4::uuid, true)`,
		usuarioID, fmt.Sprintf("idp-%d", sufNum), clinicaID, rolID)
	mustExec(ctx, t, db,
		`INSERT INTO "Especialidad" ("id", "codigo", "nombre") VALUES ($1::uuid, $2, $3)`,
		especialidadID, fmt.Sprintf("esp-%d", sufNum), fmt.Sprintf("Especialidad %d", sufNum))
	mustExec(ctx, t, db,
		`INSERT INTO "Profesional" ("id", "clinicaId", "usuarioId", "especialidadId", "activo") VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, true)`,
		profesionalID, clinicaID, usuarioID, especialidadID)

	if v := versionDeAgenda(t, db, clinicaID, profesionalID); v != 0 {
		t.Fatalf("versión inicial = %d, want 0", v)
	}
}

func TestAgendaVersionRF5InsertarHorarioIncrementa(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	clinicaID, profesionalID, _, usuarioID := fixturesAgendaVersion(t, db)
	v0 := versionDeAgenda(t, db, clinicaID, profesionalID)

	mustExec(context.Background(), t, db,
		`INSERT INTO "HorarioAtencion" ("clinicaId", "profesionalId", "diaSemana", "horaInicio", "horaFin", "vigenteDesde", "activo")
		 VALUES ($1::uuid, $2::uuid, 1, '08:00:00', '12:00:00', current_date, true)`,
		clinicaID, profesionalID)

	if v := versionDeAgenda(t, db, clinicaID, profesionalID); v != v0+1 {
		t.Fatalf("tras INSERT horario: versión = %d, want %d", v, v0+1)
	}

	// UPDATE sin cambios relevantes (solo actualizadoEn) no debe incrementar.
	mustExec(context.Background(), t, db,
		`UPDATE "HorarioAtencion" SET "actualizadoEn" = now() WHERE "clinicaId" = $1::uuid AND "profesionalId" = $2::uuid`,
		clinicaID, profesionalID)
	if v := versionDeAgenda(t, db, clinicaID, profesionalID); v != v0+1 {
		t.Fatalf("UPDATE irrelevante cambió la versión a %d", v)
	}

	// UPDATE con cambio relevante debe incrementar.
	mustExec(context.Background(), t, db,
		`UPDATE "HorarioAtencion" SET "horaFin" = '13:00:00' WHERE "clinicaId" = $1::uuid AND "profesionalId" = $2::uuid`,
		clinicaID, profesionalID)
	if v := versionDeAgenda(t, db, clinicaID, profesionalID); v != v0+2 {
		t.Fatalf("tras UPDATE horario: versión = %d, want %d", v, v0+2)
	}

	_ = usuarioID
}

func TestAgendaVersionRF6BloqueoInsertDelete(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	clinicaID, profesionalID, _, usuarioID := fixturesAgendaVersion(t, db)
	v0 := versionDeAgenda(t, db, clinicaID, profesionalID)

	var bloqueoID string
	err := db.QueryRow(
		`INSERT INTO "Bloqueo" ("clinicaId", "profesionalId", "inicio", "fin", "motivo", "creadoPorUsuarioId")
		 VALUES ($1::uuid, $2::uuid, now(), now() + interval '1 hour', 'motivo', $3::uuid)
		 RETURNING "id"::text`,
		clinicaID, profesionalID, usuarioID,
	).Scan(&bloqueoID)
	if err != nil {
		t.Fatalf("insertar bloqueo: %v", err)
	}
	if v := versionDeAgenda(t, db, clinicaID, profesionalID); v != v0+1 {
		t.Fatalf("tras INSERT bloqueo: versión = %d, want %d", v, v0+1)
	}

	mustExec(context.Background(), t, db,
		`DELETE FROM "Bloqueo" WHERE "id" = $1::uuid`,
		bloqueoID)
	if v := versionDeAgenda(t, db, clinicaID, profesionalID); v != v0+2 {
		t.Fatalf("tras DELETE bloqueo: versión = %d, want %d", v, v0+2)
	}
}

func TestAgendaVersionRF3ServicioDuracionAfectaMultiplesProfesionales(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	clinicaID, p1, servicioID, _ := fixturesAgendaVersion(t, db)

	// Segundo profesional con el mismo servicio.
	p2Usuario := newTestUUID()
	p2 := newTestUUID()
	mustExec(context.Background(), t, db,
		`INSERT INTO "Usuario" ("id", "idpSub", "clinicaId", "rolId", "activo")
		 SELECT $1::uuid, $1::text, $2::uuid, "id", true FROM "Rol" WHERE "codigo" = 'profesional'`,
		p2Usuario, clinicaID)
	mustExec(context.Background(), t, db,
		`INSERT INTO "Profesional" ("id", "clinicaId", "usuarioId", "especialidadId", "activo")
		 SELECT $1::uuid, $2::uuid, $3::uuid, "especialidadId", true FROM "Profesional" WHERE "id" = $4::uuid`,
		p2, clinicaID, p2Usuario, p1)
	mustExec(context.Background(), t, db,
		`INSERT INTO "ProfesionalServicio" ("clinicaId", "profesionalId", "servicioId") VALUES ($1::uuid, $2::uuid, $3::uuid)`,
		clinicaID, p2, servicioID)

	v0p1 := versionDeAgenda(t, db, clinicaID, p1)
	v0p2 := versionDeAgenda(t, db, clinicaID, p2)

	// Cambio de duración: ambos profesionales deben verse afectados.
	mustExec(context.Background(), t, db,
		`UPDATE "Servicio" SET "duracionMinutos" = 45 WHERE "id" = $1::uuid`,
		servicioID)

	if v := versionDeAgenda(t, db, clinicaID, p1); v != v0p1+1 {
		t.Fatalf("p1 tras cambio duración: versión = %d, want %d", v, v0p1+1)
	}
	if v := versionDeAgenda(t, db, clinicaID, p2); v != v0p2+1 {
		t.Fatalf("p2 tras cambio duración: versión = %d, want %d", v, v0p2+1)
	}
}

func TestAgendaVersionRF3NombreServicioNoIncrementa(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	clinicaID, profesionalID, servicioID, _ := fixturesAgendaVersion(t, db)
	v0 := versionDeAgenda(t, db, clinicaID, profesionalID)

	mustExec(context.Background(), t, db,
		`UPDATE "Servicio" SET "nombre" = 'Otro nombre' WHERE "id" = $1::uuid`,
		servicioID)

	if v := versionDeAgenda(t, db, clinicaID, profesionalID); v != v0 {
		t.Fatalf("cambio de nombre incrementó la versión a %d", v)
	}
}

func TestAgendaVersionOperacionRechazadaNoIncrementa(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	clinicaID, profesionalID, _, _ := fixturesAgendaVersion(t, db)
	v0 := versionDeAgenda(t, db, clinicaID, profesionalID)

	// Horario con horaInicio >= horaFin debe fallar por CHECK.
	_, err := db.Exec(
		`INSERT INTO "HorarioAtencion" ("clinicaId", "profesionalId", "diaSemana", "horaInicio", "horaFin", "vigenteDesde", "activo")
		 VALUES ($1::uuid, $2::uuid, 1, '18:00:00', '08:00:00', current_date, true)`,
		clinicaID, profesionalID)
	if err == nil {
		t.Fatal("se esperaba error de CHECK")
	}

	if v := versionDeAgenda(t, db, clinicaID, profesionalID); v != v0 {
		t.Fatalf("operación rechazada incrementó la versión a %d", v)
	}
}

func TestAgendaVersionRollbackRevierteIncremento(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	clinicaID, profesionalID, _, _ := fixturesAgendaVersion(t, db)
	v0 := versionDeAgenda(t, db, clinicaID, profesionalID)

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	mustExec(context.Background(), t, tx,
		`INSERT INTO "HorarioAtencion" ("clinicaId", "profesionalId", "diaSemana", "horaInicio", "horaFin", "vigenteDesde", "activo")
		 VALUES ($1::uuid, $2::uuid, 1, '08:00:00', '12:00:00', current_date, true)`,
		clinicaID, profesionalID)

	// Dentro de la transacción la versión ya debe reflejar el cambio.
	var vTx int64
	if err := tx.QueryRow(
		`SELECT "version" FROM "AgendaVersion" WHERE "clinicaId" = $1::uuid AND "profesionalId" = $2::uuid`,
		clinicaID, profesionalID).Scan(&vTx); err != nil {
		t.Fatalf("leer versión en tx: %v", err)
	}
	if vTx != v0+1 {
		t.Fatalf("versión dentro de tx = %d, want %d", vTx, v0+1)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	if v := versionDeAgenda(t, db, clinicaID, profesionalID); v != v0 {
		t.Fatalf("tras rollback: versión = %d, want %d", v, v0)
	}
}

func TestAgendaVersionRF4CambioEstadoProfesional(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	clinicaID, profesionalID, _, _ := fixturesAgendaVersion(t, db)
	v0 := versionDeAgenda(t, db, clinicaID, profesionalID)

	mustExec(context.Background(), t, db,
		`UPDATE "Profesional" SET "activo" = false WHERE "id" = $1::uuid`,
		profesionalID)
	if v := versionDeAgenda(t, db, clinicaID, profesionalID); v != v0+1 {
		t.Fatalf("tras desactivar profesional: versión = %d, want %d", v, v0+1)
	}
}

func TestAgendaVersionRF4CambioServiciosHabilitados(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	clinicaID, profesionalID, servicioID, _ := fixturesAgendaVersion(t, db)
	v0 := versionDeAgenda(t, db, clinicaID, profesionalID)

	mustExec(context.Background(), t, db,
		`DELETE FROM "ProfesionalServicio" WHERE "clinicaId" = $1::uuid AND "profesionalId" = $2::uuid AND "servicioId" = $3::uuid`,
		clinicaID, profesionalID, servicioID)
	if v := versionDeAgenda(t, db, clinicaID, profesionalID); v != v0+1 {
		t.Fatalf("tras quitar servicio: versión = %d, want %d", v, v0+1)
	}
}

func TestAgendaVersionAislamientoPorClinica(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	clinicaA, pA, _, _ := fixturesAgendaVersion(t, db)
	clinicaB, pB, _, _ := fixturesAgendaVersion(t, db)

	v0A := versionDeAgenda(t, db, clinicaA, pA)
	v0B := versionDeAgenda(t, db, clinicaB, pB)

	mustExec(context.Background(), t, db,
		`UPDATE "Profesional" SET "activo" = false WHERE "id" = $1::uuid`,
		pA)

	if v := versionDeAgenda(t, db, clinicaA, pA); v != v0A+1 {
		t.Fatalf("clínica A: versión = %d, want %d", v, v0A+1)
	}
	if v := versionDeAgenda(t, db, clinicaB, pB); v != v0B {
		t.Fatalf("clínica B se vio afectada: versión = %d, want %d", v, v0B)
	}
}

func TestAgendaVersionConcurrenciaNoPierdeIncrementos(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	clinicaID, profesionalID, _, _ := fixturesAgendaVersion(t, db)
	v0 := versionDeAgenda(t, db, clinicaID, profesionalID)

	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			hi := fmt.Sprintf("%02d:00:00", 8+i)
			hf := fmt.Sprintf("%02d:30:00", 8+i)
			_, err := db.Exec(
				`INSERT INTO "HorarioAtencion" ("clinicaId", "profesionalId", "diaSemana", "horaInicio", "horaFin", "vigenteDesde", "activo")
				 VALUES ($1::uuid, $2::uuid, 1, $3::time, $4::time, current_date, true)`,
				clinicaID, profesionalID, hi, hf)
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("insert concurrente falló: %v", err)
	}

	if v := versionDeAgenda(t, db, clinicaID, profesionalID); v != v0+n {
		t.Fatalf("versión final = %d, want %d", v, v0+n)
	}
}

func TestAgendaVersionCitaInsertIncrementa(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	clinicaID, profesionalID, servicioID, usuarioID := fixturesAgendaVersion(t, db)
	citaBase, pacienteID := fixtureHorarioYPaciente(t, db, clinicaID, profesionalID)
	v0 := versionDeAgenda(t, db, clinicaID, profesionalID)

	insertarCitaReservada(t, db, clinicaID, profesionalID, servicioID, pacienteID, citaBase, 0)

	if v := versionDeAgenda(t, db, clinicaID, profesionalID); v != v0+1 {
		t.Fatalf("tras INSERT cita reservada: versión = %d, want %d", v, v0+1)
	}
	_ = usuarioID
}

func TestAgendaVersionCitaCancelacionIncrementa(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	clinicaID, profesionalID, servicioID, usuarioID := fixturesAgendaVersion(t, db)
	citaBase, pacienteID := fixtureHorarioYPaciente(t, db, clinicaID, profesionalID)
	citaID := insertarCitaReservada(t, db, clinicaID, profesionalID, servicioID, pacienteID, citaBase, 0)
	v0 := versionDeAgenda(t, db, clinicaID, profesionalID)

	mustExec(context.Background(), t, db,
		`UPDATE "Cita" SET "estado" = 'cancelada', "canceladaEn" = now(), "canceladaPorUsuarioId" = $1::uuid, "motivoCancelacion" = 'paciente canceló' WHERE "id" = $2::uuid`,
		usuarioID, citaID)

	if v := versionDeAgenda(t, db, clinicaID, profesionalID); v != v0+1 {
		t.Fatalf("tras cancelar cita: versión = %d, want %d", v, v0+1)
	}
}

func TestAgendaVersionCitaAtendidaIncrementa(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	clinicaID, profesionalID, servicioID, usuarioID := fixturesAgendaVersion(t, db)
	citaBase, pacienteID := fixtureHorarioYPaciente(t, db, clinicaID, profesionalID)
	// Cita del lunes pasado para que el desenlace sea permitido por validarTransicionCita.
	citaID := insertarCitaReservada(t, db, clinicaID, profesionalID, servicioID, pacienteID, citaBase.AddDate(0, 0, -7), 0)
	v0 := versionDeAgenda(t, db, clinicaID, profesionalID)

	mustExec(context.Background(), t, db,
		`UPDATE "Cita" SET "estado" = 'atendida' WHERE "id" = $1::uuid`,
		citaID)

	if v := versionDeAgenda(t, db, clinicaID, profesionalID); v != v0+1 {
		t.Fatalf("tras atender cita: versión = %d, want %d", v, v0+1)
	}
	_ = usuarioID
}

func TestAgendaVersionCitaReprogramacionIncrementa(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	clinicaID, profesionalID, servicioID, usuarioID := fixturesAgendaVersion(t, db)
	citaBase, pacienteID := fixtureHorarioYPaciente(t, db, clinicaID, profesionalID)
	citaID := insertarCitaReservada(t, db, clinicaID, profesionalID, servicioID, pacienteID, citaBase, 0)
	v0 := versionDeAgenda(t, db, clinicaID, profesionalID)

	// Reprogramar 30 minutos después, mismo profesional.
	nuevoInicio := citaBase.Add(30 * time.Minute).UTC()
	mustExec(context.Background(), t, db,
		`SELECT app."reprogramarCita"($1::uuid, $2::uuid, $3::timestamptz)`,
		citaID, clinicaID, nuevoInicio)

	// UPDATE a 'reprogramada' + INSERT de la nueva cita reservada.
	if v := versionDeAgenda(t, db, clinicaID, profesionalID); v != v0+2 {
		t.Fatalf("tras reprogramar cita: versión = %d, want %d", v, v0+2)
	}
	_ = usuarioID
}

func TestAgendaVersionCitaReprogramacionEntreProfesionales(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	clinicaID, profesionalA, servicioID, _ := fixturesAgendaVersion(t, db)
	citaBase, pacienteID := fixtureHorarioYPaciente(t, db, clinicaID, profesionalA)
	profesionalB, _ := crearSegundoProfesional(t, db, clinicaID, servicioID, citaBase)

	citaID := insertarCitaReservada(t, db, clinicaID, profesionalA, servicioID, pacienteID, citaBase, 0)
	vA0 := versionDeAgenda(t, db, clinicaID, profesionalA)
	vB0 := versionDeAgenda(t, db, clinicaID, profesionalB)

	// Reprogramar al mismo horario pero con el profesional B.
	mustExec(context.Background(), t, db,
		`SELECT app."reprogramarCita"($1::uuid, $2::uuid, $3::timestamptz, $4::uuid)`,
		citaID, clinicaID, citaBase.UTC(), profesionalB)

	// El profesional A deja de tener la cita reservada (UPDATE a reprogramada).
	if vA := versionDeAgenda(t, db, clinicaID, profesionalA); vA != vA0+1 {
		t.Fatalf("versión profesional A = %d, want %d", vA, vA0+1)
	}
	// El profesional B recibe la nueva cita reservada (INSERT).
	if vB := versionDeAgenda(t, db, clinicaID, profesionalB); vB != vB0+1 {
		t.Fatalf("versión profesional B = %d, want %d", vB, vB0+1)
	}
}

func TestAgendaVersionCitaRollbackNoIncrementa(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	clinicaID, profesionalID, servicioID, usuarioID := fixturesAgendaVersion(t, db)
	citaBase, pacienteID := fixtureHorarioYPaciente(t, db, clinicaID, profesionalID)
	v0 := versionDeAgenda(t, db, clinicaID, profesionalID)

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	inicio := citaBase.UTC()
	mustExec(context.Background(), t, tx,
		`INSERT INTO "Cita" ("id", "clinicaId", "pacienteId", "profesionalId", "servicioId", "inicio", "duracionMinutos", "fin", "estado")
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::timestamptz, 30, $6::timestamptz + interval '30 minutes', 'reservada')`,
		newTestUUID(), clinicaID, pacienteID, profesionalID, servicioID, inicio)

	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	if v := versionDeAgenda(t, db, clinicaID, profesionalID); v != v0 {
		t.Fatalf("tras rollback: versión = %d, want %d", v, v0)
	}
	_ = usuarioID
}

func TestAgendaVersionCitaOperacionRechazadaNoIncrementa(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	clinicaID, profesionalID, servicioID, usuarioID := fixturesAgendaVersion(t, db)
	citaBase, pacienteID := fixtureHorarioYPaciente(t, db, clinicaID, profesionalID)
	v0 := versionDeAgenda(t, db, clinicaID, profesionalID)

	// Insertar una cita que se solapa con ella misma: la segunda debe fallar
	// por el EXCLUDE de Cita.
	insertarCitaReservada(t, db, clinicaID, profesionalID, servicioID, pacienteID, citaBase, 0)
	_, err := db.Exec(
		`INSERT INTO "Cita" ("id", "clinicaId", "pacienteId", "profesionalId", "servicioId", "inicio", "duracionMinutos", "fin", "estado")
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::timestamptz, 30, $6::timestamptz + interval '30 minutes', 'reservada')`,
		newTestUUID(), clinicaID, pacienteID, profesionalID, servicioID, citaBase.UTC())
	if err == nil {
		t.Fatal("se esperaba error de solapamiento")
	}

	if v := versionDeAgenda(t, db, clinicaID, profesionalID); v != v0+1 {
		t.Fatalf("operación rechazada cambió la versión a %d", v)
	}
	_ = usuarioID
}

func TestAgendaVersionCitaConcurrenciaNoPierdeIncrementos(t *testing.T) {
	db := agendaVersionDB(t)
	defer db.Close()

	clinicaID, profesionalID, servicioID, _ := fixturesAgendaVersion(t, db)
	citaBase, pacienteID := fixtureHorarioYPaciente(t, db, clinicaID, profesionalID)
	v0 := versionDeAgenda(t, db, clinicaID, profesionalID)

	const n = 5
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, err := db.Exec(
				`INSERT INTO "Cita" ("id", "clinicaId", "pacienteId", "profesionalId", "servicioId", "inicio", "duracionMinutos", "fin", "estado")
				 VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::timestamptz, 30, $6::timestamptz + interval '30 minutes', 'reservada')`,
				newTestUUID(), clinicaID, pacienteID, profesionalID, servicioID, citaBase.Add(time.Duration(i*30)*time.Minute).UTC())
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("insert concurrente de cita falló: %v", err)
	}

	if v := versionDeAgenda(t, db, clinicaID, profesionalID); v != v0+n {
		t.Fatalf("versión final = %d, want %d", v, v0+n)
	}
}
