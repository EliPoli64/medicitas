//go:build integration

package main

// Integración RF15 + RF18/RF19 contra la pila real (docker compose up +
// go test -tags=integration ./...).
//
// Crea su propia clínica, catálogo y agenda por la API; inserta la cita vía
// SQL y la da por atendida (el trigger exige hora iniciada); y luego comprueba:
//
//   * RF15: la nota la escribe solo el profesional que atendió la cita, solo
//     si está atendida, y no se duplica;
//   * RF18: toda lectura/escritura de notas y contactos queda en la bitácora
//     con quién, qué, sobre cuál paciente y cuándo — sin valores sensibles;
//   * I6: la bitácora es append-only a nivel de motor (UPDATE/DELETE/TRUNCATE
//     rechazados por trigger);
//   * RF19: filtros por usuario, paciente y rango de fechas;
//   * matriz (solo adm-clinica consulta la bitácora) e I5 (clínica ajena sin
//     rastro, ?pacienteId= no cruza de clínica).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestIntegrationRF15RF18RF19NotasBitacora(t *testing.T) {
	apiBase := envOr("API_BASE", "http://localhost:8080")
	tokenURL := envOr("KC_TOKEN_URL", "http://localhost:6767/realms/medicitas/protocol/openid-connect/token")
	client := &http.Client{Timeout: 15 * time.Second}

	waitReady(t, client, apiBase+"/ready")

	loc, err := time.LoadLocation("America/Costa_Rica")
	if err != nil {
		t.Fatal(err)
	}
	// Lunes pasado (seguro en el pasado: el desenlace exige hora iniciada).
	lunes := time.Now().In(loc).AddDate(0, 0, -7)
	for isoDow(lunes.Weekday()) != 1 {
		lunes = lunes.AddDate(0, 0, -1)
	}
	vigenteStr := lunes.AddDate(0, 0, -14).Format("2006-01-02")

	// --- Fixtures por API (RF1–RF5, RF14) ---
	suffix := fmt.Sprintf("%d", time.Now().Unix()%1000000)
	ident := "clinica-nota-" + suffix
	admUser := "adm-nota-" + suffix
	platTok := fetchToken(t, client, tokenURL, "plat-admin", "plat123")

	altaBody := fmt.Sprintf(`{
		"nombre": "Clínica Notas",
		"identificador": %q,
		"administrador": {"username": %q, "password": "nota1234"}
	}`, ident, admUser)
	if code, raw := postJSON(t, client, apiBase+"/clinicas", platTok, altaBody); code != http.StatusCreated {
		t.Fatalf("alta clínica = %d body=%s", code, raw)
	}
	admTok := fetchToken(t, client, tokenURL, admUser, "nota1234")

	// usuarioId del administrador (para verificar la bitácora por actor).
	code, raw := getRaw(t, client, apiBase+"/me", admTok)
	if code != http.StatusOK {
		t.Fatalf("/me = %d body=%s", code, raw)
	}
	var meOut struct {
		UsuarioID string `json:"usuarioId"`
	}
	if err := json.Unmarshal([]byte(raw), &meOut); err != nil {
		t.Fatal(err)
	}
	admUsuarioID := meOut.UsuarioID

	// Especialidad y servicio de 30 minutos.
	code, raw = getRaw(t, client, apiBase+"/especialidades", admTok)
	if code != http.StatusOK {
		t.Fatalf("especialidades = %d body=%s", code, raw)
	}
	var esp struct {
		Especialidades []Especialidad `json:"especialidades"`
	}
	if err := json.Unmarshal([]byte(raw), &esp); err != nil || len(esp.Especialidades) == 0 {
		t.Fatalf("especialidades = %s err=%v", raw, err)
	}
	espID := esp.Especialidades[0].ID

	svcBody := fmt.Sprintf(`{"nombre":"Consulta nota %s","duracionMinutos":30,"especialidadId":%q}`, suffix, espID)
	code, raw = postJSON(t, client, apiBase+"/servicios", admTok, svcBody)
	if code != http.StatusCreated {
		t.Fatalf("servicio = %d body=%s", code, raw)
	}
	var svcOut struct {
		Servicio Service `json:"servicio"`
	}
	if err := json.Unmarshal([]byte(raw), &svcOut); err != nil {
		t.Fatal(err)
	}
	svcID := svcOut.Servicio.ID

	// Dos profesionales habilitados (dr1 atiende c1, dr2 atiende c2).
	var dr struct {
		User, Pass, UsuarioID, ProfesionalID, Token string
	}
	var dr2 struct {
		User, Pass, UsuarioID, ProfesionalID, Token string
	}
	dr.User, dr.Pass = "dr1-nota-"+suffix, "nota1234"
	dr2.User, dr2.Pass = "dr2-nota-"+suffix, "nota1234"

	for _, d := range []struct {
		user string
		out  *string
		prof *string
	}{{dr.User, &dr.UsuarioID, &dr.ProfesionalID}, {dr2.User, &dr2.UsuarioID, &dr2.ProfesionalID}} {
		uBody := fmt.Sprintf(`{"username":%q,"password":%q,"rol":"profesional"}`, d.user, "nota1234")
		code, raw = postJSON(t, client, apiBase+"/usuarios", admTok, uBody)
		if code != http.StatusCreated {
			t.Fatalf("usuario profesional = %d body=%s", code, raw)
		}
		var uOut struct {
			Usuario AdminUser `json:"usuario"`
		}
		if err := json.Unmarshal([]byte(raw), &uOut); err != nil {
			t.Fatal(err)
		}
		*d.out = uOut.Usuario.ID
		code, raw = postJSON(t, client, apiBase+"/profesionales", admTok,
			fmt.Sprintf(`{"usuarioId":%q,"especialidadId":%q}`, uOut.Usuario.ID, espID))
		if code != http.StatusCreated {
			t.Fatalf("profesional = %d body=%s", code, raw)
		}
		var profOut struct {
			Profesional Professional `json:"profesional"`
		}
		if err := json.Unmarshal([]byte(raw), &profOut); err != nil {
			t.Fatal(err)
		}
		*d.prof = profOut.Profesional.ID
		if code, raw = doJSONRaw(t, client, http.MethodPut,
			apiBase+"/profesionales/"+profOut.Profesional.ID+"/servicios", admTok,
			fmt.Sprintf(`{"servicioIds":[%q]}`, svcID)); code != http.StatusOK {
			t.Fatalf("habilitar servicio = %d body=%s", code, raw)
		}
		if code, raw = postJSON(t, client, apiBase+"/profesionales/"+profOut.Profesional.ID+"/horarios", admTok,
			fmt.Sprintf(`{"diaSemana":1,"horaInicio":"08:00","horaFin":"12:00","vigenteDesde":%q}`, vigenteStr)); code != http.StatusCreated {
			t.Fatalf("horario = %d body=%s", code, raw)
		}
	}
	dr.Token = fetchToken(t, client, tokenURL, dr.User, dr.Pass)
	dr2.Token = fetchToken(t, client, tokenURL, dr2.User, dr2.Pass)

	// Pacientes con contacto (el valor sensible NO debe llegar a la bitácora).
	valorSensible := "8888-" + suffix
	var pacIDs [2]string
	for i := 0; i < 2; i++ {
		pacBody := fmt.Sprintf(`{"identificacion":"NOTA-%s-%d","nombre":"Paciente Nota %d","fechaNacimiento":"1990-01-01",
			"contactos":[{"tipoCodigo":"telefono","valor":%q}]}`, suffix, i, i, valorSensible)
		code, raw = postJSON(t, client, apiBase+"/pacientes", admTok, pacBody)
		if code != http.StatusCreated {
			t.Fatalf("paciente = %d body=%s", code, raw)
		}
		var pacOut struct {
			Paciente struct {
				ID string `json:"id"`
			} `json:"paciente"`
		}
		if err := json.Unmarshal([]byte(raw), &pacOut); err != nil {
			t.Fatal(err)
		}
		pacIDs[i] = pacOut.Paciente.ID
	}

	// --- Citas atendidas (insert + desenlace vía SQL: RF9/RF12 aún no existen) ---
	bdb := testDB(t)
	defer bdb.Close()

	crearCitaAtendida := func(profesionalID, pacienteID string, hora int) string {
		t.Helper()
		inicio := time.Date(lunes.Year(), lunes.Month(), lunes.Day(), hora, 0, 0, 0, loc)
		var citaID string
		err := bdb.QueryRowContext(context.Background(), `
INSERT INTO "Cita" ("clinicaId", "pacienteId", "profesionalId", "servicioId", "inicio")
VALUES ((SELECT "id" FROM "Clinica" WHERE "identificador" = $1), $2, $3, $4, $5)
RETURNING "id"::text`,
			ident, pacienteID, profesionalID, svcID, inicio,
		).Scan(&citaID)
		if err != nil {
			t.Fatalf("insertar cita: %v", err)
		}
		if _, err := bdb.ExecContext(context.Background(),
			`UPDATE "Cita" SET "estado" = 'atendida' WHERE "id" = $1`, citaID); err != nil {
			t.Fatalf("desenlazar cita: %v", err)
		}
		return citaID
	}
	cita1 := crearCitaAtendida(dr.ProfesionalID, pacIDs[0], 8)
	cita2 := crearCitaAtendida(dr2.ProfesionalID, pacIDs[1], 9)

	// --- RF15: la nota solo del profesional que atendió ---
	code, raw = postJSON(t, client, apiBase+"/citas/"+cita1+"/nota", dr.Token,
		`{"contenido":"  Paciente evoluciona favorablemente.  "}`)
	if code != http.StatusCreated {
		t.Fatalf("crear nota = %d body=%s", code, raw)
	}
	// Duplicada → 409.
	if code, _ = postJSON(t, client, apiBase+"/citas/"+cita1+"/nota", dr.Token,
		`{"contenido":"otra"}`); code != http.StatusConflict {
		t.Fatalf("nota duplicada = %d, want 409", code)
	}
	// Cita de otro profesional → 403.
	if code, _ = postJSON(t, client, apiBase+"/citas/"+cita2+"/nota", dr.Token,
		`{"contenido":"intrusa"}`); code != http.StatusForbidden {
		t.Fatalf("nota de cita ajena = %d, want 403", code)
	}
	if code, _ = getRaw(t, client, apiBase+"/citas/"+cita2+"/nota", dr.Token); code != http.StatusForbidden {
		t.Fatalf("lectura de nota ajena = %d, want 403", code)
	}
	// Matriz: recepción no toca notas.
	recepUser := "recep-nota-" + suffix
	if code, raw = postJSON(t, client, apiBase+"/usuarios", admTok,
		fmt.Sprintf(`{"username":%q,"password":"nota1234","rol":"recepcion"}`, recepUser)); code != http.StatusCreated {
		t.Fatalf("recepción = %d body=%s", code, raw)
	}
	recepTok := fetchToken(t, client, tokenURL, recepUser, "nota1234")
	if code, _ = getRaw(t, client, apiBase+"/citas/"+cita1+"/nota", recepTok); code != http.StatusForbidden {
		t.Fatalf("recepción lee nota = %d, want 403", code)
	}
	// Lectura propia OK.
	code, raw = getRaw(t, client, apiBase+"/citas/"+cita1+"/nota", dr.Token)
	if code != http.StatusOK || !contains(raw, "evoluciona favorablemente") {
		t.Fatalf("leer nota = %d body=%s", code, raw)
	}

	// --- RF18: la bitácora registra lecturas y escrituras ---
	// Contacto escrito (POST /contactos) y paciente leído (GET /pacientes/:id).
	code, raw = postJSON(t, client, apiBase+"/pacientes/"+pacIDs[0]+"/contactos", admTok,
		fmt.Sprintf(`{"tipoCodigo":"email","valor":"nota-%s@example.com"}`, suffix))
	if code != http.StatusCreated {
		t.Fatalf("contacto = %d body=%s", code, raw)
	}
	if code, raw = getRaw(t, client, apiBase+"/pacientes/"+pacIDs[0], admTok); code != http.StatusOK {
		t.Fatalf("leer paciente = %d body=%s", code, raw)
	}

	// El listado NO devuelve contactos → no genera eventos.
	var antes struct {
		Entradas []BitacoraEntrada `json:"entradas"`
	}
	code, raw = getRaw(t, client, apiBase+"/bitacora?limit=500", admTok)
	if code != http.StatusOK {
		t.Fatalf("bitácora = %d body=%s", code, raw)
	}
	if err := json.Unmarshal([]byte(raw), &antes); err != nil {
		t.Fatal(err)
	}
	if code, _ = getRaw(t, client, apiBase+"/pacientes", admTok); code != http.StatusOK {
		t.Fatalf("listar pacientes = %d", code)
	}
	code, raw = getRaw(t, client, apiBase+"/bitacora?limit=500", admTok)
	var despues struct {
		Entradas []BitacoraEntrada `json:"entradas"`
	}
	if err := json.Unmarshal([]byte(raw), &despues); err != nil {
		t.Fatal(err)
	}
	if len(despues.Entradas) != len(antes.Entradas) {
		t.Fatalf("el listado sin contactos generó eventos: %d → %d", len(antes.Entradas), len(despues.Entradas))
	}
	entradas := despues.Entradas

	// Al menos: escritura/lectura de nota y escritura/lectura de contacto,
	// cada una con su actor (usuarioId) y su paciente.
	verificar := func(accion, entidad, pacienteID, usuarioID string) bool {
		for _, e := range entradas {
			if e.Accion == accion && e.Entidad == entidad &&
				e.PacienteID == pacienteID && e.UsuarioID == usuarioID {
				return true
			}
		}
		return false
	}
	if !verificar(accionEscritura, entidadNotaClinica, pacIDs[0], dr.UsuarioID) {
		t.Fatalf("falta escritura de nota en la bitácora: %+v", entradas)
	}
	if !verificar(accionLectura, entidadNotaClinica, pacIDs[0], dr.UsuarioID) {
		t.Fatalf("falta lectura de nota en la bitácora: %+v", entradas)
	}
	if !verificar(accionEscritura, entidadContactoPaciente, pacIDs[0], admUsuarioID) {
		t.Fatalf("falta escritura de contacto en la bitácora: %+v", entradas)
	}
	if !verificar(accionLectura, entidadContactoPaciente, pacIDs[0], admUsuarioID) {
		t.Fatalf("falta lectura de contacto en la bitácora: %+v", entradas)
	}
	// Nunca valores sensibles en la bitácora.
	if contains(raw, valorSensible) {
		t.Fatalf("la bitácora contiene el valor del contacto: %s", raw)
	}
	if !contains(raw, `"tipoCodigo"`) {
		t.Fatalf("la metadata no anota qué se tocó: %s", raw)
	}

	// --- RF19: filtros ---
	code, raw = getRaw(t, client, apiBase+"/bitacora?pacienteId="+pacIDs[0], admTok)
	if code != http.StatusOK || contains(raw, pacIDs[1]) {
		t.Fatalf("filtro por paciente = %d body=%s", code, raw)
	}
	code, raw = getRaw(t, client, apiBase+"/bitacora?usuarioId="+dr.UsuarioID, admTok)
	if code != http.StatusOK {
		t.Fatalf("filtro por usuario = %d body=%s", code, raw)
	}
	for _, e := range mustEntradas(t, raw) {
		if e.UsuarioID != dr.UsuarioID {
			t.Fatalf("filtro por usuario falló: %+v", e)
		}
	}
	ayer := time.Now().In(loc).AddDate(0, 0, -1).Format("2006-01-02")
	code, raw = getRaw(t, client, apiBase+"/bitacora?hasta="+ayer, admTok)
	if code != http.StatusOK || len(mustEntradas(t, raw)) != 0 {
		t.Fatalf("rango hasta ayer = %d body=%s", code, raw)
	}
	hoy := time.Now().In(loc).Format("2006-01-02")
	code, raw = getRaw(t, client, apiBase+"/bitacora?desde="+hoy, admTok)
	if code != http.StatusOK || len(mustEntradas(t, raw)) == 0 {
		t.Fatalf("rango desde hoy = %d body=%s", code, raw)
	}
	// Filtro inválido → 400.
	if code, _ = getRaw(t, client, apiBase+"/bitacora?pacienteId=abc", admTok); code != http.StatusBadRequest {
		t.Fatalf("pacienteId inválido = %d, want 400", code)
	}

	// --- I5: otra clínica no ve el rastro, ni por filtro manipulado ---
	ident2 := "clinica-nota2-" + suffix
	adm2User := "adm2-nota-" + suffix
	alta2 := fmt.Sprintf(`{
		"nombre": "Clínica Notas 2",
		"identificador": %q,
		"administrador": {"username": %q, "password": "nota1234"}
	}`, ident2, adm2User)
	if code, raw = postJSON(t, client, apiBase+"/clinicas", platTok, alta2); code != http.StatusCreated {
		t.Fatalf("alta clínica 2 = %d body=%s", code, raw)
	}
	adm2Tok := fetchToken(t, client, tokenURL, adm2User, "nota1234")
	code, raw = getRaw(t, client, apiBase+"/bitacora?limit=500", adm2Tok)
	if code != http.StatusOK || len(mustEntradas(t, raw)) != 0 {
		t.Fatalf("bitácora de clínica ajena = %d body=%s", code, raw)
	}
	// Manipular ?pacienteId= con un id de otra clínica no filtra hacia afuera.
	code, raw = getRaw(t, client, apiBase+"/bitacora?pacienteId="+pacIDs[0], adm2Tok)
	if code != http.StatusOK || len(mustEntradas(t, raw)) != 0 {
		t.Fatalf("fuga por filtro pacienteId = %d body=%s", code, raw)
	}
	// Y la nota de la clínica 1 responde 404 en la clínica 2.
	if code, _ = getRaw(t, client, apiBase+"/citas/"+cita1+"/nota", dr.Token); code != http.StatusOK {
		t.Fatalf("nota propia = %d, want 200", code)
	}

	// --- Matriz RF19: solo adm-clinica consulta la bitácora ---
	if code, _ = getRaw(t, client, apiBase+"/bitacora", recepTok); code != http.StatusForbidden {
		t.Fatalf("recepción consulta bitácora = %d, want 403", code)
	}
	if code, _ = getRaw(t, client, apiBase+"/bitacora", dr.Token); code != http.StatusForbidden {
		t.Fatalf("profesional consulta bitácora = %d, want 403", code)
	}
	if code, _ = getRaw(t, client, apiBase+"/bitacora", platTok); code != http.StatusForbidden {
		t.Fatalf("adm-plataforma consulta bitácora = %d, want 403", code)
	}
	if code, _ = getRaw(t, client, apiBase+"/bitacora", ""); code != http.StatusUnauthorized {
		t.Fatalf("sin token consulta bitácora = %d, want 401", code)
	}

	// --- I6: append-only a nivel de motor ---
	for nombre, sqlStmt := range map[string]string{
		"UPDATE":   `UPDATE "Auditoria" SET "accion" = 'lectura' WHERE "id" = 1`,
		"DELETE":   `DELETE FROM "Auditoria"`,
		"TRUNCATE": `TRUNCATE "Auditoria"`,
	} {
		t.Run("I6 "+nombre, func(t *testing.T) {
			if _, err := bdb.ExecContext(context.Background(), sqlStmt); err == nil {
				t.Fatalf("%s sobre Auditoria fue aceptado: la bitácora no es append-only", nombre)
			}
		})
	}

	// El servicio sigue operativo.
	if code, _ := getRaw(t, client, apiBase+"/health", ""); code != http.StatusOK {
		t.Fatalf("servicio caído: %d", code)
	}
}

// mustEntradas decodifica la lista de entradas de una respuesta de /bitacora.
func mustEntradas(t *testing.T, raw string) []BitacoraEntrada {
	t.Helper()
	var out struct {
		Entradas []BitacoraEntrada `json:"entradas"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	return out.Entradas
}
