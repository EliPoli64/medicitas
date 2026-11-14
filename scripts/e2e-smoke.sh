#!/usr/bin/env bash
# =============================================================================
# e2e-smoke.sh — prueba E2E de todo el sistema MediCitas con un solo comando
# (§6.4/§7.7: verificación ejecutable sin pasos intermedios ni datos manuales).
#
# Cubre de punta a punta: identidad (RF17), aislamiento I5, catálogo RF2–RF4,
# horarios/bloqueos RF5–RF6, disponibilidad RF7–RF8, ciclo de cita RF9–RF13 con
# los invariantes I1–I4 e I7, carrera por el último espacio (§5.3), I8
# (consultar-reservar-consultar con caché Redis), pacientes/notas/bitácora
# RF14–RF15/RF18–RF19 con I6 (append-only vía psql), conteos RF20–RF21 y la
# matriz de roles SQL (§5.5). Termina con un resumen PASS/FAIL (exit 0/1).
#
# Uso (desde cualquier directorio, con la pila arriba):
#   ./scripts/e2e-smoke.sh              # requiere docker compose up
#   ./scripts/e2e-smoke.sh --up         # además levanta/reconstruye la pila
#   ./scripts/e2e-smoke.sh --with-go    # añade go test unit + integración
#   ./scripts/e2e-smoke.sh --keep       # no limpia la clínica E2E al final
#   ./scripts/e2e-smoke.sh --help
#
# Cada corrida crea clínicas con sufijo único (timestamp), así que las corridas
# son idempotentes entre sí. Requiere: bash, curl, jq, docker compose (GNU date).
# =============================================================================
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

API="${API_BASE:-http://localhost:8080}"
KC="${KC_BASE:-http://localhost:6767}"
TOKEN_URL="$KC/realms/medicitas/protocol/openid-connect/token"
TZCLINICA="America/Costa_Rica"

UP=0 WITH_GO=0 KEEP=0
for arg in "$@"; do
  case "$arg" in
    --up) UP=1 ;;
    --with-go) WITH_GO=1 ;;
    --keep) KEEP=1 ;;
    -h|--help)
      sed -n '2,22p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "opción desconocida: $arg (usa --help)"; exit 3 ;;
  esac
done

PASS=0 FAIL=0
if [ -t 1 ]; then
  C_OK=$'\033[32m'; C_BAD=$'\033[31m'; C_SEC=$'\033[1m'; C_OFF=$'\033[0m'
else
  C_OK=""; C_BAD=""; C_SEC=""; C_OFF=""
fi

ok()  { PASS=$((PASS + 1)); printf '  %sok%s   %s\n' "$C_OK" "$C_OFF" "$1"; }
nok() {
  FAIL=$((FAIL + 1))
  printf '  %sFAIL%s %s\n' "$C_BAD" "$C_OFF" "$1"
  [ $# -gt 1 ] && printf '        %s\n' "$2"
  return 0
}
section() { printf '\n%s== %s ==%s\n' "$C_SEC" "$1" "$C_OFF"; }
die()     { printf '%sERROR:%s %s\n' "$C_BAD" "$C_OFF" "$1"; exit "${2:-2}"; }

command -v jq  >/dev/null 2>&1 || die "falta jq en el host" 3
command -v curl >/dev/null 2>&1 || die "falta curl en el host" 3

TMP="$(mktemp -d /tmp/opencode/e2e-smoke.XXXXXX)"
BODY_FILE="$TMP/respuesta.json"
trap 'rm -rf "$TMP"' EXIT

# --- helpers HTTP -----------------------------------------------------------
STATUS="000" RESP=""
req() { # req METODO /ruta [token] [body-json]
  local method=$1 path=$2 token=${3:-} body=${4:-}
  local args=(-sS -X "$method" -o "$BODY_FILE" -w '%{http_code}' --max-time 30
              -H 'Content-Type: application/json')
  [ -n "$token" ] && args+=(-H "Authorization: Bearer $token")
  [ -n "$body" ] && args+=(-d "$body")
  STATUS=$(curl "${args[@]}" "$API$path" 2>/dev/null) || STATUS=000
  RESP=$(cat "$BODY_FILE" 2>/dev/null || true)
}

want_status() { # want_status "descripción" código-esperado
  if [ "$STATUS" = "$2" ]; then ok "$1 → $2"
  else nok "$1" "status=$STATUS want=$2 body=${RESP:0:180}"; fi
}
want_jq() { # want_jq "descripción" expr-jq valor-esperado
  local got
  got=$(printf '%s' "$RESP" | jq -r "$2" 2>/dev/null) || got="<jq-error>"
  if [ "$got" = "$3" ]; then ok "$1"
  else nok "$1" "jq: $2 => '$got' want '$3' | body=${RESP:0:180}"; fi
}
want_ge() { # want_ge "descripción" expr-jq mínimo-numérico
  local v
  v=$(printf '%s' "$RESP" | jq -r "$2" 2>/dev/null) || v=""
  if [[ "$v" =~ ^[0-9]+$ ]] && [ "$v" -ge "$3" ]; then ok "$1 ($v ≥ $3)"
  else nok "$1" "jq: $2 => '$v' want ≥ $3 | body=${RESP:0:180}"; fi
}
want_true() { # want_true "descripción" expr-jq-booleana
  local v
  v=$(printf '%s' "$RESP" | jq -r "$2" 2>/dev/null) || v="<jq-error>"
  if [ "$v" = "true" ]; then ok "$1"
  else nok "$1" "jq: $2 => '$v' want true | body=${RESP:0:180}"; fi
}

declare -A TOKCACHE
tok() { # tok usuario contraseña → imprime access_token (cacheado)
  local u=$1 p=$2
  if [ -z "${TOKCACHE[$u]:-}" ]; then
    TOKCACHE[$u]=$(curl -sS --max-time 20 -X POST "$TOKEN_URL" \
      -d client_id=api -d grant_type=password -d "username=$u" -d "password=$p" \
      | jq -r '.access_token // empty' 2>/dev/null)
  fi
  printf '%s' "${TOKCACHE[$u]}"
}
need_tok() { # need_tok var usuario contraseña
  local v
  v=$(tok "$2" "$3")
  [ -n "$v" ] || die "no se pudo obtener token para $2" 2
  printf -v "$1" '%s' "$v"
}

psqlq() { docker compose exec -T db psql -v ON_ERROR_STOP=1 -U medicitas -d medicitas -Atc "$1" 2>&1; }
redisq() { docker compose exec -T redis redis-cli "$@" 2>/dev/null; }

# --- pila -------------------------------------------------------------------
if [ "$UP" = 1 ]; then
  section "Levantando la pila (docker compose up -d --build)"
  docker compose up -d --build || die "docker compose up falló"
fi

section "Fase 0 — preflight"
printf '  esperando %s/ready ...\n' "$API"
ready=0
for _ in $(seq 1 90); do
  code=$(curl -s -m 3 -o /dev/null -w '%{http_code}' "$API/ready" || true)
  [ "$code" = "200" ] && { ready=1; break; }
  sleep 2
done
[ "$ready" = 1 ] || die "la API no llegó a /ready; levántala con: docker compose up -d --build"
ok "/ready → 200"

need_tok TOK_PLAT plat-admin plat123
ok "token plat-admin"

psqlq 'SELECT 1;' | grep -qx '1' && ok "psql (Postgres accesible)" \
  || die "psql no responde; ¿docker compose up?" 2
redisq PING | grep -qx 'PONG' && ok "redis-cli PING → PONG" \
  || die "Redis no responde" 2
MIGS=$(psqlq 'SELECT count(*) FROM ops."Migracion" WHERE "exito";')
[ "${MIGS:-0}" -ge 5 ] 2>/dev/null \
  && ok "migraciones registradas: $MIGS" \
  || die "solo $MIGS migraciones registradas; reconstruye con: docker compose up -d --build" 2

# --- datos de la corrida ----------------------------------------------------
SUF=$(date +%s)
CLIN_A="e2e-a-$SUF"
CLIN_B="e2e-b-$SUF"
PASS_ADM="E2ePass123"
U_ADM_A="adme2e${SUF}"
U_ADM_B="adme2eb${SUF}"
U_REC="rece2e${SUF}"
U_DR="dre2e${SUF}"
U_DR2="dre2eb${SUF}"
LUNES=$(TZ=$TZCLINICA date -d 'next monday' +%F)
HOY=$(TZ=$TZCLINICA date +%F)
ts() { TZ=$TZCLINICA date -d "$LUNES $1" +%Y-%m-%dT%H:%M:%S%:z; }

# --- fase 1: identidad RF17 -------------------------------------------------
section "Fase 1 — identidad y permisos (RF17)"
req GET /pacientes
want_status "sin token" 401
need_tok TOK_REC recep-norte recep123
need_tok TOK_ADM_DEMO adm-oeste oeste12345

req GET /pacientes "$TOK_PLAT"
want_status "adm-plataforma sobre /pacientes (rol equivocado)" 403

req GET /pacientes "$TOK_REC"
want_status "recepción sobre /pacientes" 200
want_ge "recepción ve pacientes de su clínica" '.pacientes | length' 1

req GET /me "$TOK_REC"
want_status "/me" 200
want_jq "/me responde clínica" '.clinicaIdentificador' 'clinica-norte'

# --- fase 2: aislamiento I5 -------------------------------------------------
section "Fase 2 — aislamiento entre clínicas (I5 / RF19)"
req POST /clinicas "$TOK_PLAT" "$(jq -nc --arg id "$CLIN_B" --arg u "$U_ADM_B" '{
  nombre: ("Clínica E2E B " + $id), identificador: $id, zonaHoraria: "America/Costa_Rica",
  administrador: {username: $u, password: "E2ePass123", email: ($u + "@e2e.local"),
                  nombre: "Admin", apellido: "E2E-B"}}')"
want_status "alta de clínica B (adm-plataforma)" 201
ID_B=$(printf '%s' "$RESP" | jq -r '.clinica.id // empty')
[ -n "$ID_B" ] || die "sin clinica.id en la respuesta: ${RESP:0:200}" 2
need_tok TOK_ADM_B "$U_ADM_B" "$PASS_ADM"

req POST /clinicas "$TOK_PLAT" "$(jq -nc --arg id "$CLIN_A" --arg u "$U_ADM_A" '{
  nombre: ("Clínica E2E A " + $id), identificador: $id, zonaHoraria: "America/Costa_Rica",
  administrador: {username: $u, password: "E2ePass123", email: ($u + "@e2e.local"),
                  nombre: "Admin", apellido: "E2E-A"}}')"
want_status "alta de clínica A (adm-plataforma)" 201
ID_A=$(printf '%s' "$RESP" | jq -r '.clinica.id // empty')
[ -n "$ID_A" ] || die "sin clinica.id en la respuesta: ${RESP:0:200}" 2
need_tok TOK_ADM_A "$U_ADM_A" "$PASS_ADM"

req POST /pacientes "$TOK_ADM_B" '{"identificacion":"8-888-'"$SUF"'","nombre":"Paciente E2E B",
  "fechaNacimiento":"1992-04-04","contactos":[]}'
want_status "paciente en clínica B" 201
PAC_XB=$(printf '%s' "$RESP" | jq -r '.paciente.id // empty')

req GET "/pacientes/$PAC_XB" "$TOK_ADM_A"
want_status "GET paciente ajeno con token de A → 404 (I5)" 404

req GET "/pacientes?clinicaId=$CLIN_B" "$TOK_ADM_A"
want_status "GET pacientes con ?clinicaId ajeno" 200
want_jq "la clínica la fija el token, no el query (I5)" '.clinicaIdentificador' "$CLIN_A"

req GET "/bitacora?clinicaId=$CLIN_B" "$TOK_ADM_A"
want_status "GET bitácora con ?clinicaId ajeno" 200
want_jq "bitácora siempre de la clínica del token (I5)" '.clinicaIdentificador' "$CLIN_A"

# --- fase 3: catálogo RF2–RF4 ----------------------------------------------
section "Fase 3 — catálogo de servicios y profesionales (RF2–RF4)"
req GET /especialidades "$TOK_ADM_A"
want_status "listar especialidades" 200
ESP=$(printf '%s' "$RESP" | jq -r '.especialidades[0].id // empty')
[ -n "$ESP" ] || die "sin especialidades en el catálogo" 2

req POST /servicios "$TOK_ADM_A" '{"nombre":"Consulta E2E '"$SUF"'","duracionMinutos":30,"especialidadId":"'"$ESP"'"}'
want_status "crear servicio (adm-clinica)" 201
SVC=$(printf '%s' "$RESP" | jq -r '.servicio.id // empty')
[ -n "$SVC" ] || die "sin servicio.id" 2

req POST /servicios "$TOK_ADM_A" '{"nombre":"","duracionMinutos":0,"especialidadId":""}'
want_status "crear servicio inválido → 400" 400

req POST /servicios "$TOK_REC" '{"nombre":"No Debe Crear","duracionMinutos":30,"especialidadId":"'"$ESP"'"}'
want_status "recepción no crea servicios → 403" 403

req POST /usuarios "$TOK_ADM_A" '{"username":"'"$U_REC"'","password":"E2ePass123","rol":"recepcion",
  "nombre":"Recepcion","apellido":"E2E"}'
want_status "crear usuario recepción" 201
req POST /usuarios "$TOK_ADM_A" '{"username":"'"$U_DR"'","password":"E2ePass123","rol":"profesional",
  "nombre":"Doctora","apellido":"E2E"}'
want_status "crear usuario profesional" 201
USR_DR=$(printf '%s' "$RESP" | jq -r '.usuario.id // empty')
req POST /usuarios "$TOK_ADM_A" '{"username":"'"$U_DR2"'","password":"E2ePass123","rol":"profesional",
  "nombre":"Doctora","apellido":"E2E-B"}'
want_status "crear segundo usuario profesional" 201
USR_DR2=$(printf '%s' "$RESP" | jq -r '.usuario.id // empty')
need_tok TOK_REC_A "$U_REC" "$PASS_ADM"
need_tok TOK_DR "$U_DR" "$PASS_ADM"
need_tok TOK_DR2 "$U_DR2" "$PASS_ADM"

req POST /profesionales "$TOK_ADM_A" '{"usuarioId":"'"$USR_DR"'","especialidadId":"'"$ESP"'"}'
want_status "crear profesional" 201
PROF1=$(printf '%s' "$RESP" | jq -r '.profesional.id // empty')
req POST /profesionales "$TOK_ADM_A" '{"usuarioId":"'"$USR_DR2"'","especialidadId":"'"$ESP"'"}'
want_status "crear segundo profesional" 201
PROF2=$(printf '%s' "$RESP" | jq -r '.profesional.id // empty')
[ -n "$PROF1" ] && [ -n "$PROF2" ] || die "sin profesional.id" 2

req PUT "/profesionales/$PROF1/servicios" "$TOK_ADM_A" '{"servicioIds":["'"$SVC"'"]}'
want_status "habilitar servicio en profesional 1" 200
req PUT "/profesionales/$PROF2/servicios" "$TOK_ADM_A" '{"servicioIds":["'"$SVC"'"]}'
want_status "habilitar servicio en profesional 2" 200

# --- fase 4: horarios y bloqueos RF5–RF6 ------------------------------------
section "Fase 4 — horarios y bloqueos (RF5 / RF6)"
req POST "/profesionales/$PROF1/horarios" "$TOK_ADM_A" \
  '{"diaSemana":1,"horaInicio":"08:00","horaFin":"12:00","vigenteDesde":"2020-01-01"}'
want_status "crear horario lunes 08:00–12:00" 201
req POST "/profesionales/$PROF2/horarios" "$TOK_ADM_A" \
  '{"diaSemana":1,"horaInicio":"08:00","horaFin":"12:00","vigenteDesde":"2020-01-01"}'
want_status "horario del segundo profesional" 201
req POST "/profesionales/$PROF1/horarios" "$TOK_ADM_A" \
  '{"diaSemana":1,"horaInicio":"10:00","horaFin":"11:00","vigenteDesde":"2020-01-01"}'
want_status "horario solapado → 409 (RF5)" 409

req POST "/profesionales/$PROF1/bloqueos" "$TOK_ADM_A" "$(jq -nc \
  --arg i "$(ts 10:00)" --arg f "$(ts 11:00)" '{inicio: $i, fin: $f, motivo: "permiso E2E"}')"
want_status "crear bloqueo lunes 10:00–11:00" 201

# --- fase 5: disponibilidad RF7/RF8 -----------------------------------------
section "Fase 5 — disponibilidad (RF7 / RF8)"
req GET "/profesionales/$PROF1/disponibilidad?fecha=$LUNES&servicioId=$SVC" "$TOK_ADM_A"
want_status "RF7 disponibilidad por fecha" 200
want_ge "RF7 ofrece espacios" '.espacios | length' 1
want_jq "hay espacio a las 08:00" '[.espacios[].horaLocal] | index("08:00") != null' 'true'
want_jq "el bloqueo 10:00–11:00 oculta el espacio 10:00" '[.espacios[].horaLocal] | index("10:00") != null' 'false'

S08=$(printf '%s' "$RESP" | jq -r '[.espacios[] | select(.horaLocal=="08:00")][0].inicio // empty')
S09=$(printf '%s' "$RESP" | jq -r '[.espacios[] | select(.horaLocal=="09:00")][0].inicio // empty')
S0930=$(printf '%s' "$RESP" | jq -r '[.espacios[] | select(.horaLocal=="09:30")][0].inicio // empty')
S11=$(printf '%s' "$RESP" | jq -r '[.espacios[] | select(.horaLocal=="11:00")][0].inicio // empty')
for v in S08 S09 S0930 S11; do
  [ -n "${!v}" ] && ok "instante de $v disponible" || nok "instante de $v no aparece en RF7"
done

req GET "/profesionales/$PROF1/disponibilidad?fecha=2026-13-99&servicioId=$SVC" "$TOK_ADM_A"
want_status "fecha inválida → 400" 400

req GET "/servicios/$SVC/disponibilidad?desde=$LUNES&hasta=$LUNES" "$TOK_ADM_A"
want_status "RF8 disponibilidad por servicio" 200
want_ge "RF8 devuelve fechas con espacios" '.disponibilidad | length' 1
req GET "/servicios/$SVC/disponibilidad?desde=$LUNES&hasta=2020-01-01" "$TOK_ADM_A"
want_status "RF8 hasta < desde → 400" 400

# --- fase 6: ciclo de la cita RF9–RF13 (I1, I2, I3, I4, I7) -----------------
section "Fase 6 — ciclo de cita: I1, I2, I3, I4, I7 (RF9–RF13)"
req POST /pacientes "$TOK_ADM_A" '{"identificacion":"1-111-'"$SUF"'","nombre":"Paciente A E2E",
  "fechaNacimiento":"1990-01-01","contactos":[{"tipoCodigo":"telefono","valor":"+506 7000-0001","esPrincipal":true}]}'
want_status "crear paciente A" 201
PAC_A=$(printf '%s' "$RESP" | jq -r '.paciente.id // empty')
req POST /pacientes "$TOK_ADM_A" '{"identificacion":"2-222-'"$SUF"'","nombre":"Paciente B E2E",
  "fechaNacimiento":"1990-06-06","contactos":[]}'
want_status "crear paciente B" 201
PAC_B=$(printf '%s' "$RESP" | jq -r '.paciente.id // empty')
req POST /pacientes "$TOK_ADM_A" '{"identificacion":"3-333-'"$SUF"'","nombre":"Paciente C E2E",
  "fechaNacimiento":"1991-02-02","contactos":[]}'
want_status "crear paciente C" 201
PAC_C=$(printf '%s' "$RESP" | jq -r '.paciente.id // empty')
req POST /pacientes "$TOK_ADM_A" '{"identificacion":"4-444-'"$SUF"'","nombre":"Paciente D E2E",
  "fechaNacimiento":"1993-03-03","contactos":[]}'
want_status "crear paciente D" 201
PAC_D=$(printf '%s' "$RESP" | jq -r '.paciente.id // empty')

cita_body() { # cita_body paciente profesional inicio
  jq -nc --arg p "$1" --arg pr "$2" --arg i "$3" \
    '{pacienteId: $p, profesionalId: $pr, servicioId: "'"$SVC"'", inicio: $i}'
}

req POST /citas "$TOK_REC_A" "$(cita_body "$PAC_A" "$PROF1" "$S08")"
want_status "RF9 reservar 08:00 → 201" 201
want_jq "I4: la duración la fija el servicio (30)" '.cita.duracionMinutos' '30'
want_jq "I4: fin − inicio = 30 min" \
  '((.cita.fin | fromdateiso8601) - (.cita.inicio | fromdateiso8601)) / 60 | floor' '30'
CITA1=$(printf '%s' "$RESP" | jq -r '.cita.id // empty')

req POST /citas "$TOK_REC_A" "$(cita_body "$PAC_B" "$PROF1" "$S08")"
want_status "I1: mismo profesional, mismo hueco → 409" 409
req POST /citas "$TOK_REC_A" '{}'
want_status "cita sin datos → 400" 400

req POST /citas "$TOK_REC_A" "$(cita_body "$PAC_A" "$PROF1" "$(ts 10:00)")"
want_status "I3: cita dentro del bloqueo → 400" 400
req POST /citas "$TOK_REC_A" "$(cita_body "$PAC_A" "$PROF1" "$(ts 20:00)")"
want_status "I3: cita fuera del horario → 400" 400

req POST "/citas/$CITA1/reprogramar" "$TOK_REC_A" "$(jq -nc --arg i "$S09" '{inicio: $i}')"
want_status "RF10 reprogramar 08:00 → 09:00" 200
want_jq "RF10 conserva el id de la cita anterior" '.citaAnteriorId' "$CITA1"
want_jq "I4: reprogramar no altera la duración (30)" '.cita.duracionMinutos' '30'
CITA_VIG=$(printf '%s' "$RESP" | jq -r '.cita.id // empty')
[ -n "$CITA_VIG" ] && [ "$CITA_VIG" != "$CITA1" ] \
  && ok "RF10 la reprogramación crea una cita nueva" \
  || nok "RF10 sin cita nueva" "CITA_VIG='$CITA_VIG'"

req GET "/profesionales/$PROF1/disponibilidad?fecha=$LUNES&servicioId=$SVC" "$TOK_ADM_A"
want_status "RF7 tras reprogramar" 200
want_jq "I7: el hueco liberado 08:00 vuelve al instante" \
  '[.espacios[].horaLocal] | index("08:00") != null' 'true'
want_jq "I7: el nuevo hueco 09:00 desaparece de inmediato" \
  '[.espacios[].horaLocal] | index("09:00") != null' 'false'

req POST /citas "$TOK_REC_A" "$(cita_body "$PAC_A" "$PROF2" "$S09")"
want_status "I2: mismo paciente, profesional distinto, solapado → 409" 409
req POST /citas "$TOK_REC_A" "$(cita_body "$PAC_B" "$PROF2" "$S09")"
want_status "control: otro paciente con el profesional 2 → 201" 201
CITA2=$(printf '%s' "$RESP" | jq -r '.cita.id // empty')

req POST "/citas/$CITA_VIG/cancelar" "$TOK_REC_A" '{"motivo":"ajuste E2E"}'
want_status "RF11 cancelar" 200
want_jq "estado cancelada" '.cita.estado' 'cancelada'
req GET "/profesionales/$PROF1/disponibilidad?fecha=$LUNES&servicioId=$SVC" "$TOK_ADM_A"
want_status "RF7 tras cancelar" 200
want_jq "I7: cancelar libera 09:00 de inmediato" \
  '[.espacios[].horaLocal] | index("09:00") != null' 'true'

req POST "/citas/$CITA_VIG/desenlace" "$TOK_REC_A" '{"estado":"atendida"}'
want_status "RF13 desenlace sobre cita cancelada → 400" 400
req POST "/citas/$CITA2/desenlace" "$TOK_REC_A" '{"estado":"atendida"}'
want_status "desenlace antes de la hora → 400 (trigger)" 400

# --- fase 7: carrera por el último espacio (§5.3) ---------------------------
section "Fase 7 — carrera concurrente por un espacio (§5.3 / §7.6)"
mkdir -p "$TMP/carrera"
i=0
for p in "$PAC_A" "$PAC_C" "$PAC_D" "$PAC_A" "$PAC_C"; do
  (
    curl -sS -o "$TMP/carrera/body.$i" -w '%{http_code}' --max-time 30 \
      -X POST -H "Authorization: Bearer $TOK_REC_A" -H 'Content-Type: application/json' \
      -d "$(cita_body "$p" "$PROF1" "$S11")" "$API/citas" > "$TMP/carrera/code.$i" 2>/dev/null
  ) &
  i=$((i + 1))
done
wait
n201=0; malos=""
for j in 0 1 2 3 4; do
  c=$(cat "$TMP/carrera/code.$j" 2>/dev/null || echo 000)
  if [ "$c" = 201 ]; then n201=$((n201 + 1)); fi
  if [ "$c" != 201 ] && [ "$c" != 409 ]; then malos="$malos $j:$c"; fi
done
[ "$n201" = 1 ] && ok "5 peticiones concurrentes → exactamente 1×201" \
  || nok "5 peticiones concurrentes → $n201×201, want 1"
[ -z "$malos" ] && ok "las otras 4 respondieron 409 (espacio tomado)" \
  || nok "respuestas inesperadas:$malos"
GANA=""
for j in 0 1 2 3 4; do
  [ "$(cat "$TMP/carrera/code.$j" 2>/dev/null)" = 201 ] && GANA=$(jq -r '.cita.id // empty' "$TMP/carrera/body.$j")
done
[ -n "$GANA" ] && ok "cita ganadora registrada" || nok "no se identificó la cita ganadora"

RC=$(psqlq "SELECT count(*) FROM \"Cita\" WHERE \"profesionalId\" = '$PROF1'
  AND \"inicio\" = '$S11'::timestamptz AND \"estado\" = 'reservada';")
[ "$RC" = "1" ] && ok "SQL: exactamente 1 fila para el espacio en disputa" \
  || nok "SQL: filas en el espacio = '$RC', want 1"

# --- fase 8: I8 consultar-reservar-consultar --------------------------------
section "Fase 8 — I8: la caché no contradice la BD (RF7 + Redis)"
req GET "/profesionales/$PROF1/disponibilidad?fecha=$LUNES&servicioId=$SVC" "$TOK_ADM_A"
want_status "consultar (1ª)" 200
want_jq "09:30 libre antes de reservar" '[.espacios[].horaLocal] | index("09:30") != null' 'true'
EP_KEY=$(redisq --scan --pattern "medicitas:avail:*:epoch:${CLIN_A}:${PROF1}" | head -1)
[ -n "$EP_KEY" ] && ok "clave de epoch en Redis (caché activa)" || nok "sin clave epoch en Redis"
EP_ANTES=$(redisq GET "$EP_KEY")

req POST /citas "$TOK_REC_A" "$(cita_body "$PAC_A" "$PROF1" "$S0930")"
want_status "reservar 09:30" 201

req GET "/profesionales/$PROF1/disponibilidad?fecha=$LUNES&servicioId=$SVC" "$TOK_ADM_A"
want_status "consultar (2ª)" 200
want_jq "I8: 09:30 ya no aparece tras reservar" '[.espacios[].horaLocal] | index("09:30") != null' 'false'
EP_DESPUES=$(redisq GET "$EP_KEY")
[ -n "$EP_ANTES" ] && [ -n "$EP_DESPUES" ] && [ "$EP_ANTES" != "$EP_DESPUES" ] \
  && ok "I7/I8: la invalidación rotó el epoch en Redis" \
  || nok "epoch sin rotar (antes='$EP_ANTES' después='$EP_DESPUES')"

# --- fase 9: pacientes, notas y bitácora RF14/RF15/RF18/RF19 + I6 -----------
section "Fase 9 — pacientes, notas clínicas y bitácora (RF14/15/18/19, I6)"
req GET "/pacientes/$PAC_A" "$TOK_ADM_A"
want_status "RF14 leer paciente (lectura auditada)" 200
req POST "/pacientes/$PAC_A/contactos" "$TOK_ADM_A" \
  '{"tipoCodigo":"email","valor":"paciente@e2e.local","esPrincipal":true}'
want_status "RF14 agregar contacto (escritura auditada)" 201

# RF15 exige una cita atendida: se inserta una cita en un lunes pasado (tal
# como el test de integración) y se registra el desenlace vía API.
DOW=$(TZ=$TZCLINICA date +%u)
BACK=$((DOW - 1)); [ "$BACK" -eq 0 ] && BACK=7
PASADO=$(TZ=$TZCLINICA date -d "$BACK days ago" +%F)
[ "$(TZ=$TZCLINICA date -d "$PASADO" +%u)" = 1 ] && ok "PASADO cae en lunes ($PASADO)" \
  || nok "PASADO no es lunes" "$PASADO"
PASTA_RAW=$(psqlq "INSERT INTO \"Cita\" (\"clinicaId\", \"pacienteId\", \"profesionalId\", \"servicioId\", \"inicio\")
  SELECT c.\"id\", '$PAC_A', '$PROF1', '$SVC', ('$PASADO 09:00'::timestamp AT TIME ZONE c.\"zonaHoraria\")
  FROM \"Clinica\" c WHERE c.\"identificador\" = '$CLIN_A'
  RETURNING \"id\";")
CITA_AT=$(printf '%s' "$PASTA_RAW" | grep -o '[0-9a-f-]\{36\}' | head -1)
[ -n "$CITA_AT" ] && ok "cita en lunes pasado creada ($PASADO)" \
  || nok "no se pudo insertar la cita pasada ($PASADO)" "$PASTA_RAW"
req POST "/citas/$CITA_AT/desenlace" "$TOK_REC_A" '{"estado":"atendida"}'
want_status "RF12 registrar desenlace (atendida)" 200
want_jq "la cita queda atendida" '.cita.estado' 'atendida'

req POST "/citas/$CITA_AT/nota" "$TOK_DR" '{"contenido":""}'
want_status "nota vacía → 400" 400
req POST "/citas/$CITA_AT/nota" "$TOK_DR" '{"contenido":"Evolución E2E sin datos sensibles."}'
want_status "RF15 crear nota (profesional)" 201
req POST "/citas/$CITA_AT/nota" "$TOK_DR" '{"contenido":"segunda nota"}'
want_status "RF15 una sola nota por cita → 409" 409
req GET "/citas/$CITA_AT/nota" "$TOK_DR"
want_status "RF15 leer nota" 200
want_jq "RF15 el contenido es el guardado" '.nota.contenido' 'Evolución E2E sin datos sensibles.'
req GET "/citas/$CITA_AT/nota" "$TOK_REC_A"
want_status "RF15 recepción no lee notas → 403" 403
req POST "/citas/$CITA_AT/nota" "$TOK_REC_A" '{"contenido":"intruso"}'
want_status "RF15 recepción no escribe notas → 403" 403

req GET "/bitacora?limit=100" "$TOK_ADM_A"
want_status "RF18 leer bitácora" 200
want_ge "RF18 hay entradas" '.entradas | length' 1
want_ge "RF18 lectura de paciente registrada" \
  '[.entradas[] | select(.accion=="lectura" and .entidad=="contacto_paciente")] | length' 1
want_ge "RF18 escritura con pacienteId registrada" \
  '[.entradas[] | select(.accion=="escritura" and .pacienteId != null)] | length' 1
want_true "RF18 metadata sin valores sensibles (sin clave contenido)" \
  '[.entradas[] | select(.metadata | has("contenido"))] | length == 0'

req GET "/bitacora?pacienteId=$PAC_A&limit=100" "$TOK_ADM_A"
want_status "RF19 filtro por paciente" 200
want_ge "RF19 el filtro devuelve entradas" '.entradas | length' 1
want_true "RF19 todas las entradas son del paciente" "all(.entradas[]; .pacienteId == \"$PAC_A\")"

ADM_A_ID=$(curl -sS -m 20 -H "Authorization: Bearer $TOK_ADM_A" "$API/me" | jq -r '.usuarioId // empty')
req GET "/bitacora?usuarioId=$ADM_A_ID&limit=100" "$TOK_ADM_A"
want_status "RF19 filtro por usuario" 200
want_ge "RF19 el filtro devuelve entradas" '.entradas | length' 1
want_true "RF19 todas las entradas son del usuario" "all(.entradas[]; .usuarioId == \"$ADM_A_ID\")"

req GET "/bitacora?desde=$HOY&limit=100" "$TOK_ADM_A"
want_status "RF19 filtro por rango" 200
want_ge "RF19 el rango devuelve entradas" '.entradas | length' 1
want_true "RF19 todas las entradas caen en el rango" "all(.entradas[]; .ocurridoEn[0:10] >= \"$HOY\")"

req GET "/bitacora" "$TOK_REC_A"
want_status "RF19 recepción no consulta la bitácora → 403" 403

# I6: la bitácora es append-only a nivel motor (§5/I6), incluso para superusuario.
I6U=$(psqlq "UPDATE \"Auditoria\" SET \"accion\" = 'lectura' WHERE \"id\" = (SELECT min(\"id\") FROM \"Auditoria\");")
printf '%s' "$I6U" | grep -qi 'append-only' && ok "I6 UPDATE rechazado por el motor" \
  || nok "I6 UPDATE no fue rechazado" "$I6U"
I6D=$(psqlq "DELETE FROM \"Auditoria\" WHERE \"id\" = (SELECT min(\"id\") FROM \"Auditoria\");")
printf '%s' "$I6D" | grep -qi 'append-only' && ok "I6 DELETE rechazado por el motor" \
  || nok "I6 DELETE no fue rechazado" "$I6D"
I6T=$(psqlq 'TRUNCATE "Auditoria";')
printf '%s' "$I6T" | grep -qi 'append-only' && ok "I6 TRUNCATE rechazado por el motor" \
  || nok "I6 TRUNCATE no fue rechazado" "$I6T"

# --- fase 10: agenda y conteo RF20/RF21 -------------------------------------
section "Fase 10 — agenda del día y conteo (RF20 / RF21)"
req GET "/agenda?fecha=$LUNES" "$TOK_REC_A"
want_status "RF20 agenda del día" 200
want_ge "RF20 devuelve citas" '.citas | length' 1
AG_JSON=$(printf '%s' "$RESP" | jq '.citas | length')
AG_SQL=$(psqlq "SELECT count(*) FROM \"Cita\" ci JOIN \"Clinica\" c ON c.\"id\" = ci.\"clinicaId\"
  WHERE c.\"identificador\" = '$CLIN_A'
    AND ci.\"inicio\" >= ('$LUNES'::timestamp AT TIME ZONE c.\"zonaHoraria\")
    AND ci.\"inicio\" < (('$LUNES'::date + 1)::timestamp AT TIME ZONE c.\"zonaHoraria\");")
[ "$AG_JSON" = "$AG_SQL" ] && ok "RF20 el JSON coincide con SQL ($AG_SQL citas)" \
  || nok "RF20 JSON=$AG_JSON vs SQL=$AG_SQL"

req GET "/citas/resumen?desde=$LUNES&hasta=$LUNES" "$TOK_REC_A"
want_status "RF21 resumen por estado" 200
want_jq "RF21 total = suma de porEstado" '.total == ([.porEstado[]] | add)' 'true'
RS_JSON=$(printf '%s' "$RESP" | jq -r '.total')
RS_SQL=$(psqlq "SELECT count(*) FROM \"Cita\" ci JOIN \"Clinica\" c ON c.\"id\" = ci.\"clinicaId\"
  WHERE c.\"identificador\" = '$CLIN_A'
    AND ci.\"inicio\" >= ('$LUNES'::timestamp AT TIME ZONE c.\"zonaHoraria\")
    AND ci.\"inicio\" < (('$LUNES'::date + 1)::timestamp AT TIME ZONE c.\"zonaHoraria\");")
[ "$RS_JSON" = "$RS_SQL" ] && ok "RF21 el total coincide con SQL ($RS_SQL)" \
  || nok "RF21 JSON=$RS_JSON vs SQL=$RS_SQL"

# --- fase 11: matriz de roles SQL (§5.5) ------------------------------------
section "Fase 11 — matriz de privilegios SQL (§5.5)"
OUT=$(psqlq 'SET ROLE medicitas_adm_plataforma; SELECT count(*) FROM "Paciente";')
printf '%s' "$OUT" | grep -qi 'permission denied' \
  && ok "adm_plataforma no puede leer Paciente" \
  || nok "adm_plataforma leyó Paciente" "$OUT"

OUT=$(psqlq 'SET ROLE medicitas_recepcion; SELECT count(*) FROM "Paciente";')
printf '%s' "$OUT" | grep -qx '0' \
  && ok "recepción sin GUC de clínica ve 0 pacientes (RLS)" \
  || nok "recepción sin GUC no vio 0" "$OUT"

OUT=$(psqlq 'SET ROLE medicitas_app; SELECT count(*) FROM "Paciente";')
printf '%s' "$OUT" | grep -qi 'permission denied' \
  && ok "la conexión medicitas_app no lee tablas sin el perfil (least privilege)" \
  || nok "medicitas_app leyó Paciente sin SET ROLE" "$OUT"

OUT=$(psqlq "SET ROLE medicitas_adm_clinica; SET app.clinica_id = '$ID_B';
  SELECT count(*) FROM \"Paciente\";")
printf '%s' "$OUT" | grep -qx '1' \
  && ok "perfil adm_clinica con GUC de la clínica B ve exactamente 1 paciente (RLS)" \
  || nok "adm_clinica con GUC de B no vio 1" "$OUT"

# --- suites Go opcionales ---------------------------------------------------
if [ "$WITH_GO" = 1 ]; then
  section "Suites Go (--with-go)"
  if docker run --rm -v "$ROOT/api:/src:z" -w /src golang:1.25-alpine \
      go test -count=1 ./... > "$TMP/go-unit.log" 2>&1; then
    ok "go test ./... (unitarias)"
  else
    nok "go test ./... (unitarias)" "$(tail -5 "$TMP/go-unit.log")"
  fi
  if docker run --rm --network host -v "$ROOT/api:/src:z" -w /src golang:1.25-alpine \
      go test -tags=integration -count=1 ./... > "$TMP/go-int.log" 2>&1; then
    ok "go test -tags=integration ./... (integración)"
  else
    nok "go test -tags=integration ./... (integración)" "$(tail -5 "$TMP/go-int.log")"
  fi
fi

# --- limpieza ---------------------------------------------------------------
if [ "$KEEP" = 0 ]; then
  section "Limpieza de la clínica E2E (--keep para omitirla)"
  CLEAN=$(psqlq "BEGIN;
    SET session_replication_role = replica;
    DELETE FROM \"NotaClinica\"        WHERE \"clinicaId\" IN (SELECT \"id\" FROM \"Clinica\" WHERE \"identificador\" IN ('$CLIN_A','$CLIN_B'));
    DELETE FROM \"ContactoPaciente\"    WHERE \"clinicaId\" IN (SELECT \"id\" FROM \"Clinica\" WHERE \"identificador\" IN ('$CLIN_A','$CLIN_B'));
    DELETE FROM \"Cita\"                WHERE \"clinicaId\" IN (SELECT \"id\" FROM \"Clinica\" WHERE \"identificador\" IN ('$CLIN_A','$CLIN_B'));
    DELETE FROM \"AgendaVersion\"       WHERE \"clinicaId\" IN (SELECT \"id\" FROM \"Clinica\" WHERE \"identificador\" IN ('$CLIN_A','$CLIN_B'));
    DELETE FROM \"HorarioAtencion\"     WHERE \"clinicaId\" IN (SELECT \"id\" FROM \"Clinica\" WHERE \"identificador\" IN ('$CLIN_A','$CLIN_B'));
    DELETE FROM \"Bloqueo\"             WHERE \"clinicaId\" IN (SELECT \"id\" FROM \"Clinica\" WHERE \"identificador\" IN ('$CLIN_A','$CLIN_B'));
    DELETE FROM \"ProfesionalServicio\" WHERE \"clinicaId\" IN (SELECT \"id\" FROM \"Clinica\" WHERE \"identificador\" IN ('$CLIN_A','$CLIN_B'));
    DELETE FROM \"Paciente\"            WHERE \"clinicaId\" IN (SELECT \"id\" FROM \"Clinica\" WHERE \"identificador\" IN ('$CLIN_A','$CLIN_B'));
    DELETE FROM \"Profesional\"         WHERE \"clinicaId\" IN (SELECT \"id\" FROM \"Clinica\" WHERE \"identificador\" IN ('$CLIN_A','$CLIN_B'));
    DELETE FROM \"Servicio\"            WHERE \"clinicaId\" IN (SELECT \"id\" FROM \"Clinica\" WHERE \"identificador\" IN ('$CLIN_A','$CLIN_B'));
    DELETE FROM \"Usuario\"             WHERE \"clinicaId\" IN (SELECT \"id\" FROM \"Clinica\" WHERE \"identificador\" IN ('$CLIN_A','$CLIN_B'));
    DELETE FROM \"Clinica\"             WHERE \"identificador\" IN ('$CLIN_A','$CLIN_B');
    COMMIT;")
  if printf '%s' "$CLEAN" | grep -qi 'error'; then
    printf '  aviso: limpieza parcial (los datos quedan con sufijo %s, inocuos): %s\n' \
      "$SUF" "$(printf '%s' "$CLEAN" | grep -i error | head -1)"
  else
    ok "clínicas $CLIN_A / $CLIN_B eliminadas"
  fi
else
  section "Limpieza omitida (--keep): datos E2E $CLIN_A / $CLIN_B conservados"
fi

# --- resumen ----------------------------------------------------------------
section "Resumen"
printf '  %saprobadas%s: %d\n' "$C_OK" "$C_OFF" "$PASS"
printf '  %sfallidas%s:  %d\n' "$C_BAD" "$C_OFF" "$FAIL"
if [ "$FAIL" -eq 0 ]; then
  printf '%sE2E SMOKE: OK%s\n' "$C_OK" "$C_OFF"
  exit 0
fi
printf '%sE2E SMOKE: FALLOS (%d)%s\n' "$C_BAD" "$FAIL" "$C_OFF"
exit 1
