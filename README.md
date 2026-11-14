# MediCitas — Etapa 1 (OLTP)

Plataforma SaaS multi-tenant de gestión de citas médicas (Arquitectura e Ingeniería de Datos, TEC).

Núcleo transaccional: API REST **sin interfaz gráfica**, clínicas aisladas, agenda, ciclo de vida de la cita, identidad en Keycloak y caché de disponibilidad en Redis.

Enunciado del curso: [`spec.md`](spec.md).

## Servicios en Etapa 1

El enunciado pide **varios servicios** (no un solo proceso que haga login + BD + caché). En Etapa 1 eso es el Compose, no un microservicio por entidad de negocio:

| Servicio | Rol |
|----------|-----|
| `api` | Dominio HTTP (JWT, permisos, rutas) |
| `db` | Fuente de verdad relacional (Postgres) |
| `keycloak` | Identidad OIDC |
| `redis` | Caché de disponibilidad |
| `seed` | Job one-shot de datos demo |

La API orquesta; la clínica de dominio se resuelve en Postgres vía `"Usuario"."idpSub"` = JWT `sub` (ver [`docs/keycloak.md`](docs/keycloak.md)). Partir citas/pacientes en servicios separados no es requisito de Etapa 1; la distribución fuerte es Etapa 2.

## Estado actual

- Repo del grupo (`EliPoli64/medicitas`) con la spec, la documentación de trabajo y el esqueleto del proyecto.
- **Base de datos implementada:** [`db/migrations/01_schema.sql`](db/migrations/01_schema.sql) crea las tablas de negocio, el esquema técnico `ops`, restricciones, índices y triggers. Incluye FKs compuestas por clínica, exclusiones de solapamiento, validación de horarios/bloqueos y la función transaccional `app."reprogramarCita"`. [`03_rls.sql`](db/migrations/03_rls.sql) añade RLS y el rol de conexión `medicitas_app`. [`04_db_roles_matrix.sql`](db/migrations/04_db_roles_matrix.sql) parte ese acceso en perfiles SQL de menor privilegio alineados con la matriz.
- **Semilla implementada:** job Python one-shot en [`seed/`](seed/) que crea/verifica usuarios en Keycloak y carga clínicas, profesionales, pacientes y citas con fechas relativas a la ejecución. Corre automáticamente en `docker compose up`.
- **Keycloak:** realm `medicitas` versionado en [`id-provider/import/medicitas-realm.json`](id-provider/import/medicitas-realm.json) (roles de la matriz, client `api`, claim `clinica_id`). Guía: [`docs/keycloak.md`](docs/keycloak.md).
- **Identidad (Matías) — cerrada:** Keycloak realm + JWT/JWKS + matriz RF17 + `sub`→`"Usuario"` + alineación claim/BD (I5). Rutas: `/health`, `/ready`, `/me`, `/permisos`. Colección [`docs/http/identidad.http`](docs/http/identidad.http); smoke [`scripts/smoke-identidad.ps1`](scripts/smoke-identidad.ps1); integración `go test -tags=integration`. Checklist en [`docs/keycloak.md`](docs/keycloak.md).
- **RF1 (alta de clínicas) — implementado:** `POST /clinicas` crea la clínica y su primer administrador (`adm_clinica`) en una sola transacción, provisionando la identidad en Keycloak vía **Admin API** (mismo mecanismo que el seed) con rol de realm y atributo `clinica_id`. Si la BD falla, se compensa eliminando la identidad. Colección [`docs/http/clinicas.http`](docs/http/clinicas.http); prueba de integración `TestIntegrationRF1AltaClinica` (`go test -tags=integration ./...`).
- **RF2–RF4 (catálogo de la clínica) — implementado:** gestión de usuarios (`GET|POST /usuarios`, `PATCH /usuarios/:id` con asignación de rol y baja lógica reflejadas en Keycloak, con compensación), catálogo de servicios (`GET /especialidades`, `GET|POST /servicios`, `PATCH /servicios/:id`) y profesionales con servicios habilitados (`GET|POST /profesionales`, `PATCH /profesionales/:id`, `PUT /profesionales/:id/servicios` con reemplazo transaccional y validación de especialidad). Todo bajo `secure(OpGestionCatalogo)` (matriz: solo `adm-clinica`) con tenant del token (I5). Colección [`docs/http/catalogo.http`](docs/http/catalogo.http); prueba de integración `TestIntegrationRF2aRF4Catalogo`.
- **RF5–RF6 (agenda) — implementado:** horarios semanales (`GET|POST /profesionales/:id/horarios`, `PATCH /horarios/:id`) y bloqueos (`GET|POST /profesionales/:id/bloqueos`, `DELETE /bloqueos/:id`) bajo `secure(OpHorariosBloqueos)`. `adm-clinica` gestiona toda la clínica; `profesional` solo la propia. POST bloqueo lista citas `reservada` en conflicto sin cancelarlas. Tenant desde BD (I5). Colección [`docs/http/agenda.http`](docs/http/agenda.http).
- **RF14 + RF16 (pacientes) — implementado:** listado/detalle/alta/actualización de pacientes y contactos (`OpPacientesLectura` / `OpPacientesEscritura`) e historial de citas `GET /pacientes/:id/citas` (`OpHistorialPaciente`). Colección [`docs/http/pacientes.http`](docs/http/pacientes.http).
- **RF7–RF8 (disponibilidad) — implementado:** `GET /profesionales/:id/disponibilidad` (espacios del día: horario − bloqueos − citas vigentes, rejilla configurable, espejo de los triggers del motor) y `GET /servicios/:id/disponibilidad` (por servicio en un rango, con profesionales y sus espacios) bajo `OpConsultarDisponibilidad`. Índices justificados con `EXPLAIN ANALYZE` sobre dataset sintético ([`docs/bench/disponibilidad_bench.sql`](docs/bench/disponibilidad_bench.sql)) en [`docs/disponibilidad.md`](docs/disponibilidad.md). Colección [`docs/http/disponibilidad.http`](docs/http/disponibilidad.http); prueba de integración `TestIntegrationRF7RF8Disponibilidad`. RF7 se sirve desde Redis cuando está disponible (I8).
- **RF9–RF13 (ciclo de la cita) — implementado:** `POST /citas` (reservar), `POST /citas/:id/reprogramar` (`app.reprogramarCita` atómica), `POST /citas/:id/cancelar`, `POST /citas/:id/desenlace` (`atendida`|`ausente`). Matriz: reservar/reprogramar/cancelar = adm+recepción; desenlace también profesional (solo propias). Duración I4; EXCLUDE I1/I2 → `409`; transiciones ilegales → `400` (RF13). Concurrencia RF9: **READ COMMITTED** + EXCLUDE. Invalidación Redis tras cada mutación (I7/I8). Colección [`docs/http/citas.http`](docs/http/citas.http); integración `TestIntegrationRF9*` / `TestIntegrationRF10a13CicloCita`.
- **RF15 + RF18/RF19 (notas clínicas y bitácora) — implementado:** nota clínica `POST|GET /citas/:id/nota` (`OpNotaClinica`, solo el profesional que atendió la cita, solo cita atendida, sin duplicados) y bitácora append-only de toda lectura/escritura de notas y contactos (`api/audit.go`): las escrituras se registran en la misma transacción que el hecho, las lecturas se registran antes de responder y sin rastro no se sirve el dato. `GET /bitacora` (`OpBitacora`, solo `adm-clinica`) con filtros por usuario, paciente y rango de fechas. Append-only protegido a nivel de motor (I6) y verificado por la prueba de integración. Colecciones [`docs/http/notas.http`](docs/http/notas.http) y [`docs/http/bitacora.http`](docs/http/bitacora.http); prueba de integración `TestIntegrationRF15RF18RF19NotasBitacora`.
- **RF20 + RF21 (operación diaria) — implementado:** agenda del día `GET /agenda?fecha=` (`OpAgendaDia`, día en `zonaHoraria` de la clínica) y conteo por estado `GET /citas/resumen?desde=&hasta=` (`OpConteoCitas`). Profesional solo ve lo propio. Colección [`docs/http/operacion.http`](docs/http/operacion.http). Mapa vivo: [`docs/arquitectura-etapa1.md`](docs/arquitectura-etapa1.md).
- **Redis / I8 — implementado:** RF7 vía `GetOrLoad`, invalidación post-mutación (horarios, bloqueos, catálogo, **reserva RF9**), validación de snapshots contra `AgendaVersion`. Diseño: [`docs/cache-disponibilidad.md`](docs/cache-disponibilidad.md).

## Estructura del repositorio

```text
.
├── api/                  # Esqueleto de la API (main, auth, Dockerfile, go.mod)
├── db/                   # Imagen de PostgreSQL y migraciones del esquema
├── seed/                 # Job one-shot de datos demo y users de Keycloak
├── id-provider/          # Keycloak + import/medicitas-realm.json
├── docs/                 # Modelo, ER, multi-tenancy, Keycloak, caché, división
├── docker-compose.yml    # api + db + keycloak + redis + seed
├── .env.example          # Variables de entorno de ejemplo
├── .gitignore
└── spec.md               # Enunciado del curso
```

## Base de datos

- Esquema: [`db/migrations/01_schema.sql`](db/migrations/01_schema.sql). RLS: [`db/migrations/03_rls.sql`](db/migrations/03_rls.sql). Perfiles SQL: [`db/migrations/04_db_roles_matrix.sql`](db/migrations/04_db_roles_matrix.sql) y [`docs/rls.md`](docs/rls.md).
- Diagrama ER: [`docs/esquema-er.md`](docs/esquema-er.md).
- La imagen `db` ejecuta los archivos de `db/migrations/` al inicializar el volumen de PostgreSQL. El job `seed` aplica las que falten en un volumen ya creado.
- La API entra como `medicitas_app` (RLS, sin heredar privilegios) y hace `SET ROLE` al perfil de la matriz (`medicitas_recepcion`, `medicitas_profesional`, `medicitas_adm_clinica`, `medicitas_adm_plataforma`). Seed y Keycloak siguen como superusuario.

### Semilla

El servicio `seed` corre una sola vez después de que PostgreSQL y Keycloak estén sanos, y termina. La API espera a que el job finalice correctamente (`service_completed_successfully`).

```bash
cp .env.example .env
docker compose up --build
```

- Crea/verifica 13 usuarios en Keycloak. Los nuevos usan `SEED_USER_PASSWORD`; los existentes (`plat-admin`, `recep-norte`) conservan su password del realm.
- Carga 3 clínicas, 6 profesionales, 12 pacientes y 30 citas con fechas relativas al día de ejecución (pasadas, futuras, canceladas, ausentes y una reprogramación por clínica).
- Es idempotente: si `ops."Semilla"` ya tiene la versión `SEED_VERSION`, sale con 0 sin duplicar datos.
- Para volver a sembrar desde cero: `docker compose down -v` y luego `docker compose up --build`.
- Detalle de users, datos y verificación: [`seed/README.md`](seed/README.md).

## Identidad (Keycloak + API)

```bash
cp .env.example .env
docker compose up --build
```

```bash
# Token
TOKEN=$(curl -s -X POST "http://localhost:6767/realms/medicitas/protocol/openid-connect/token" \
  -d "client_id=api" -d "username=recep-norte" -d "password=recep123" -d "grant_type=password" \
  | jq -r .access_token)

# Identidad (JWT + fila Usuario en Postgres), permisos y pacientes
curl -s http://localhost:8080/me -H "Authorization: Bearer $TOKEN"
curl -s http://localhost:8080/permisos -H "Authorization: Bearer $TOKEN"
curl -s http://localhost:8080/pacientes -H "Authorization: Bearer $TOKEN"
curl -s http://localhost:8080/health
curl -s http://localhost:8080/ready
```

`/me` incluye `usuarioId`, `clinicaIdentificador` y `rolCodigo` desde Postgres (vía `idpSub`). `/pacientes` lista solo la clínica del usuario (ignora `?clinicaId=`). `/ready` falla si Postgres no responde. Detalle: [`docs/keycloak.md`](docs/keycloak.md).

## Contrato de rutas

### `POST /clinicas` — RF1: alta de clínica + primer administrador

Permiso (matriz RF17): **solo `adm-plataforma`**. Crea la clínica y su primer usuario `adm_clinica` en una sola transacción, y provisiona la identidad en Keycloak (rol de realm `adm-clinica`, atributo `clinica_id`).

Entrada:

```json
{
  "nombre": "Clínica Este",
  "identificador": "clinica-este",
  "zonaHoraria": "America/Costa_Rica",
  "administrador": {
    "username": "adm-este",
    "password": "secret123",
    "email": "opcional@dominio",
    "nombre": "opcional",
    "apellido": "opcional"
  }
}
```

- `identificador`: `^[a-z0-9]+(-[a-z0-9]+)*$` (mismo CHECK que la BD).
- `zonaHoraria`: zona IANA; si se omite, `America/Costa_Rica` (default del esquema).
- `administrador.password`: mínimo 8 caracteres.

Salida `201`:

```json
{
  "clinica":       {"id": "…", "identificador": "clinica-este", "nombre": "…", "zonaHoraria": "…", "activo": true, "creadoEn": "…"},
  "administrador": {"usuarioId": "…", "idpSub": "…", "username": "adm-este", "rolCodigo": "adm_clinica"}
}
```

| Código | Cuándo |
|--------|--------|
| `201` | Clínica y administrador creados |
| `400` | JSON mal formado o campos inválidos (incluye `detalles`) |
| `401` | Sin token o token inválido |
| `403` | Token válido sin el rol de la matriz |
| `409` | `identificador` ya existe, o `username` ya existe en Keycloak |
| `502` | Keycloak no provisiona la identidad |
| `500` | Falla la transacción (se compensa eliminando la identidad creada) |

Notas de diseño (defensa): la identidad se crea **antes** que la fila porque `"Usuario"."idpSub"` es `NOT NULL UNIQUE`; si la transacción falla se elimina la identidad (saga con compensación, sin XA). Una caída entre ambos pasos deja una identidad huérfana en Keycloak que no alcanza dato alguno (sin fila `"Usuario"` no hay tenant: I5 se sostiene), y queda en logs.

Colección de peticiones: [`docs/http/clinicas.http`](docs/http/clinicas.http).

### `GET|POST /usuarios`, `PATCH /usuarios/:id` — RF2: gestión de usuarios

Permiso (matriz RF17): **solo `adm-clinica`**. Un usuario pertenece a exactamente una clínica: la del token. `adm_plataforma` no se crea por esta vía.

- `POST /usuarios` — alta. Body: `username`, `password` (≥8), `rol` (`adm_clinica`|`recepcion`|`profesional`), opcional `email`, `nombre`, `apellido`. Crea la identidad en Keycloak (rol de realm + `clinica_id`) y la fila `"Usuario"`; si la BD falla se compensa eliminando la identidad. → `201` / `400` / `409` (username duplicado) / `502` (IdP) / `500`.
- `GET /usuarios` — lista los usuarios de la clínica del token (ignora `?clinicaId=` manipulado: I5).
- `PATCH /usuarios/:id` — asignación de rol (`rol`) y/o baja lógica (`activo`). El cambio se aplica primero en Keycloak y luego en la BD, revirtiendo el paso de Keycloak si la BD falla. Un id de otra clínica responde `404`. Cambiar el rol de un profesional activo responde `409`. → `200` / `400` / `404` / `409` / `502` / `500`.

Nota de modelo: el dominio no guarda username ni credenciales (spec §2); las salidas exponen `idpSub`, no el login.

### `GET /especialidades`, `GET|POST /servicios`, `PATCH /servicios/:id` — RF3: catálogo

Permiso (matriz RF17): **solo `adm-clinica`**. El servicio lleva `nombre`, `duracionMinutos` (1–1440, define el espacio en la agenda: I4) y la `especialidadId` que requiere.

- `POST /servicios` → `201` / `400` (campos o especialidad inválida) / `409` (nombre duplicado en la clínica).
- `GET /servicios` — catálogo de la clínica del token.
- `PATCH /servicios/:id` — `nombre`, `duracionMinutos` y/o `activo` (baja lógica). `404` si no pertenece a la clínica.
- `GET /especialidades` — catálogo global de especialidades.

### `GET|POST /profesionales`, `PATCH /profesionales/:id`, `PUT /profesionales/:id/servicios` — RF4

Permiso (matriz RF17): **solo `adm-clinica`**. Un profesional es un usuario con rol `profesional` de la clínica; atiende solo los servicios asociados, y cada servicio debe corresponder a su especialidad.

- `POST /profesionales` — body: `usuarioId`, `especialidadId`. → `201` / `400` (usuario sin rol profesional activo, o especialidad inválida) / `409` (ya registrado).
- `PUT /profesionales/:id/servicios` — body: `servicioIds` (reemplaza el conjunto, en una transacción). → `200` / `400` (servicio inexistente o de otra especialidad) / `404`.
- `PATCH /profesionales/:id` — `activo` (baja lógica) → `200` / `404`.
- `GET /profesionales` — lista con sus servicios habilitados.

Colección de peticiones: [`docs/http/catalogo.http`](docs/http/catalogo.http).

### Horarios y bloqueos — RF5 / RF6

Permiso (matriz RF17): **`adm-clinica`** (toda la clínica) o **`profesional`** (solo su propio `"Profesional"`). Tenant desde BD (I5); `?clinicaId=` se ignora.

- `GET|POST /profesionales/:id/horarios` — bloques semanales (`diaSemana` 1=lunes…7=domingo). Solape con otro activo → `409`.
- `PATCH /horarios/:id` — actualizar rango/vigencia o `activo` (baja lógica).
- `GET|POST /profesionales/:id/bloqueos` — POST body: `inicio`, `fin`, `motivo`. Responde `bloqueo` + `conflictos` (citas `reservada` solapadas; **no** las cancela).
- `DELETE /bloqueos/:id` → `204`.

Colección: [`docs/http/agenda.http`](docs/http/agenda.http).

### Disponibilidad — RF7 / RF8

Permiso (matriz RF17): **`adm-clinica`, `recepcion`, `profesional`** (`OpConsultarDisponibilidad`). Tenant desde BD (I5); `?clinicaId=` se ignora. La duración de la cita la fija el servicio (I4).

- `GET /profesionales/:id/disponibilidad` — RF7. Query: `fecha=YYYY-MM-DD`, `servicioId`, `pasoMinutos` opcional (5–120, por defecto 30). Devuelve los `espacios` del día: intervalos futuros alineados a la rejilla que caben enteros en un bloque de horario vigente y no solapan bloqueos ni citas `reservada`. → `200` / `400` (parámetros, servicio o profesional inactivo, profesional sin el servicio) / `404` (no es de la clínica).
- `GET /servicios/:id/disponibilidad` — RF8. Query: `desde`, `hasta` (máx. 62 días), `pasoMinutos` opcional. Devuelve por fecha los profesionales habilitados con espacio y cuáles son esos espacios (solo fechas con disponibilidad).

Modelo de cálculo, contrato completo e **justificación de índices con planes de ejecución**: [`docs/disponibilidad.md`](docs/disponibilidad.md). Colección: [`docs/http/disponibilidad.http`](docs/http/disponibilidad.http).

### Pacientes e historial — RF14 / RF16

- `GET /pacientes`, `GET /pacientes/:id` — lectura (`adm-clinica`, `recepcion`, `profesional`).
- `POST /pacientes`, `PATCH /pacientes/:id` — escritura (`adm-clinica`, `recepcion`); body con `identificacion`, `nombre`, `fechaNacimiento` y `contactos` opcionales.
- `POST /pacientes/:id/contactos`, `PATCH /contactos/:id` — contactos (`telefono` / `email` / `direccion`).
- `GET /pacientes/:id/citas` — historial con `estado` (RF16). Paciente ajeno a la clínica → `404`.

Colección: [`docs/http/pacientes.http`](docs/http/pacientes.http).

### Notas clínicas — RF15

Permiso (matriz RF17): **solo `profesional`** (`OpNotaClinica`) y solo sobre la cita que él atendió. Dato más sensible del sistema: el historial RF16 no lo expone.

- `POST /citas/:id/nota` — body `{contenido}`. Solo si la cita está `atendida` (`409` si no) y no tiene nota (`409` si ya existe). → `201` / `400` (contenido vacío) / `403` (otro profesional o no es profesional) / `404` (cita no es de la clínica, I5).
- `GET /citas/:id/nota` → `200` / `403` / `404`. Su lectura queda registrada en la bitácora (RF18).

Colección: [`docs/http/notas.http`](docs/http/notas.http).

### Bitácora — RF18 / RF19

Toda **lectura y escritura** de notas clínicas y datos de contacto de pacientes queda en la bitácora append-only (`"Auditoria"`) con quién, qué, sobre cuál paciente y cuándo. Las escrituras se registran en la misma transacción que el hecho; las lecturas se registran antes de responder y **sin rastro no se sirve el dato**. Append-only protegido a nivel de motor (I6: triggers que rechazan `UPDATE`/`DELETE`/`TRUNCATE`). La metadata nunca contiene valores sensibles.

- `GET /bitacora` — solo `adm-clinica` (`OpBitacora`). Query: `usuarioId`, `pacienteId` (uuid), `desde`, `hasta` (`YYYY-MM-DD`, zona de la clínica), `limit` (1–500, por defecto 100). Tenant siempre del token: manipular `?pacienteId=` no cruza de clínica (I5). → `200` / `400` (filtros inválidos) / `401` / `403`.

Colección: [`docs/http/bitacora.http`](docs/http/bitacora.http).

### Ciclo de la cita — RF9 a RF13

Tenant desde BD (I5). **No** se acepta duración del cliente (I4).

| Ruta | Permiso | Notas |
|------|---------|--------|
| `POST /citas` | adm-clinica, recepción | Body: `pacienteId`, `profesionalId`, `servicioId`, `inicio`. → `201` |
| `POST /citas/:id/reprogramar` | adm-clinica, recepción | Body: `inicio`, opcional `profesionalId`/`servicioId`. Origen → `reprogramada`; nueva `reservada` enlazada. |
| `POST /citas/:id/cancelar` | adm-clinica, recepción | Body: `motivo`. Registra usuario y timestamp. |
| `POST /citas/:id/desenlace` | adm, recepción, **profesional** (solo propias) | Body: `estado`: `atendida` \| `ausente`. Solo si `now() >= inicio`. |

Solape vigente → `409`. Transición ilegal / fuera de horario → `400`. Cita ajena → `404`. **Concurrencia (RF9):** `READ COMMITTED` + `EXCLUDE` GiST; carrera con una sola ganadora. Tras cada mutación se invalida Redis (I7/I8).

Colección: [`docs/http/citas.http`](docs/http/citas.http).

### Agenda del día y conteo — RF20 / RF21

Permiso (extensión documentada de la matriz): **`adm-clinica`**, **`recepcion`**, **`profesional`** (solo propios). Tenant desde BD (I5); `?clinicaId=` se ignora. El día/rango usa la `zonaHoraria` de la clínica, no UTC a ciegas.

- `GET /agenda?fecha=YYYY-MM-DD[&profesionalId=]` — citas con `inicio` en ese día calendario; joins a paciente/servicio. Profesional sin filtro → se fuerza al suyo; otro id → `403`.
- `GET /citas/resumen?desde=&hasta=` — conteo por cada `"EstadoCita"` (`reservada`, `atendida`, `ausente`, `cancelada`, `reprogramada`) más `total`; claves ausentes → `0`.

Colección: [`docs/http/operacion.http`](docs/http/operacion.http). Arquitectura: [`docs/arquitectura-etapa1.md`](docs/arquitectura-etapa1.md).

## Pruebas

Un solo comando por suite, sin pasos intermedios y sin preparación manual de datos (§6.4): cada prueba crea sus propias fixtures con sufijo único, así que las suites son idempotentes y no ensucian la semilla. Los comandos se ejecutan desde [`api/`](api/) (ahí vive el módulo Go):

```bash
cd api

# Unitarias (sin Docker): aíslan la lógica con stores falsos
go test ./...

# Integración contra la pila real (requiere docker compose up ya levantado)
go test -tags=integration ./...
```

Sin Go instalado en el host, el mismo resultado desde la raíz del repo con el contenedor oficial:

```bash
docker run --rm -v "$PWD/api:/src:z" -w /src golang:1.25-alpine go test ./...
docker run --rm --network host -v "$PWD/api:/src:z" -w /src golang:1.25-alpine go test -tags=integration ./...
```

Al menos una prueba por cada invariante (§5.8):

| Invariante | Prueba |
|---|---|
| **I1** — sin solape de profesional | `TestIntegrationRF9CarreraUnaGanadora`, `TestIntegrationRF9ReservarCita` |
| **I2** — sin solape de paciente | `TestIntegrationI2PacienteSinSolape` |
| **I3** — dentro de horario y fuera de bloqueos | `TestIntegrationI3DentroDeHorarioYBloqueo` |
| **I4** — la duración la fija el servicio | `TestIntegrationRF10a13CicloCita` (crear y reprogramar) |
| **I5** — aislamiento entre clínicas | `TestIntegrationRLS`, `Test*IgnoresQuery*` (unitarias), `?pacienteId=` cruzado en `TestIntegrationRF15RF18RF19NotasBitacora` |
| **I6** — bitácora append-only | `TestIntegrationRF15RF18RF19NotasBitacora` (subtests I6: `UPDATE`/`DELETE`/`TRUNCATE` rechazados) |
| **I7** — liberación inmediata del espacio | `TestIntegrationRF10a13CicloCita`, `TestIntegrationRF7RF8Disponibilidad` |
| **I8** — la caché no contradice la BD | `TestIntegrationRF9I8ConsultarReservarConsultar`, `TestIntegrationRF7Cache_*` |

La prueba de carrera (§5.3 / §7.6) es `TestIntegrationRF9CarreraUnaGanadora`: clientes concurrentes compiten por el último espacio y sale **exactamente una** reserva concedida, sin duplicados ni violación de I1.

Para probar la API a mano: colecciones [`docs/http/`](docs/http/) una por requisito, con casos negativos incluidos.

## División del trabajo

| Quién | Encargo |
|-------|---------|
| **Matías** | Keycloak (realm, roles, `clinica_id`) |
| **Cristian** | Redis / I8 |
| **Elías** | Schema + seeder Python |

Más detalle: [`docs/division-trabajo.md`](docs/division-trabajo.md).

Módulo extra (grupos de 3): **RLS como segunda estrategia**, encima de la columna `clinicaId`. Ver [`docs/rls.md`](docs/rls.md).

## Reuso de la Tarea Corta 1

Compose, imagen Keycloak e idea de JWT ya están adaptados. Pendiente útil de TC1: rellenar `api/auth.go` (validación JWKS) cuando la API implemente RF17. No traer dominio `Reserva` ni `k8s/`.

## Documentación de trabajo

| Documento | Contenido |
|-----------|-----------|
| [`docs/modelo-datos.md`](docs/modelo-datos.md) | Entidades, notas 3FN |
| [`docs/esquema-er.md`](docs/esquema-er.md) | Diagrama ER del esquema SQL |
| [`docs/multi-tenancy.md`](docs/multi-tenancy.md) | Columna `clinicaId` (principal) y RLS (segunda) |
| [`docs/rls.md`](docs/rls.md) | Políticas, sesión de la API y medición |
| [`docs/cache-disponibilidad.md`](docs/cache-disponibilidad.md) | Diseño, claves, invalidación y pruebas de la caché Redis |
| [`docs/keycloak.md`](docs/keycloak.md) | Realm, roles, tokens, contrato seeder |
| [`docs/division-trabajo.md`](docs/division-trabajo.md) | Matías / Cristian / Elías |
| [`docs/arquitectura-etapa1.md`](docs/arquitectura-etapa1.md) | Mapa Compose + tabla de progreso RF |

## Pregunta rectora (defensa)

> Dos recepcionistas reservan el último espacio de la agenda exactamente al mismo tiempo. ¿Qué hace el sistema, por qué, y cómo se demuestra?
