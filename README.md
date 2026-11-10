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
- **Base de datos implementada:** [`db/migrations/01_schema.sql`](db/migrations/01_schema.sql) crea las tablas de negocio, el esquema técnico `ops`, restricciones, índices y triggers. Incluye FKs compuestas por clínica, exclusiones de solapamiento, validación de horarios/bloqueos y la función transaccional `app."reprogramarCita"`.
- **Pendiente de base de datos:** políticas RLS, roles de base de datos y `GRANT`s.
- **Semilla implementada:** job Python one-shot en [`seed/`](seed/) que crea/verifica usuarios en Keycloak y carga clínicas, profesionales, pacientes y citas con fechas relativas a la ejecución. Corre automáticamente en `docker compose up`.
- **Keycloak:** realm `medicitas` versionado en [`id-provider/import/medicitas-realm.json`](id-provider/import/medicitas-realm.json) (roles de la matriz, client `api`, claim `clinica_id`). Guía: [`docs/keycloak.md`](docs/keycloak.md).
- **API (identidad + matriz RF17 + Usuario BD):** Gin con `GET /health`, `GET /ready` (Ping Postgres), `GET /me`, `GET /permisos` y `GET /pacientes` (tenant desde BD, I5). JWT contra JWKS (`api/auth.go`); resolución `sub` → `"Usuario"."idpSub"` (`api/users.go`); permisos por operación en `api/permissions.go` (`requirePerm`). Resto del CRUD de dominio: pendiente.
- **Redis:** paquete [`api/internal/availabilitycache`](api/internal/availabilitycache/) implementado y probado de forma aislada y contra Redis real (claves por agenda, epochs, TTL, fallback al `Loader`). **Integración con RF7/API pendiente.** Diseño y pruebas: [`docs/cache-disponibilidad.md`](docs/cache-disponibilidad.md). Responsable: Cristian.

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

- Esquema: [`db/migrations/01_schema.sql`](db/migrations/01_schema.sql).
- Diagrama ER: [`docs/esquema-er.md`](docs/esquema-er.md).
- La imagen `db` ejecuta los archivos de `db/migrations/` al inicializar el volumen de PostgreSQL.
- RLS, roles de base de datos y `GRANT`s quedan para una migración posterior.

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

## División del trabajo

| Quién | Encargo |
|-------|---------|
| **Matías** | Keycloak (realm, roles, `clinica_id`) |
| **Cristian** | Redis / I8 |
| **Elías** | Schema + seeder Python |

Más detalle: [`docs/division-trabajo.md`](docs/division-trabajo.md).

Módulo extra (grupos de 3): elegir **uno**. Candidato propuesto: **RLS como segunda estrategia**.

## Reuso de la Tarea Corta 1

Compose, imagen Keycloak e idea de JWT ya están adaptados. Pendiente útil de TC1: rellenar `api/auth.go` (validación JWKS) cuando la API implemente RF17. No traer dominio `Reserva` ni `k8s/`.

## Documentación de trabajo

| Documento | Contenido |
|-----------|-----------|
| [`docs/modelo-datos.md`](docs/modelo-datos.md) | Entidades, notas 3FN |
| [`docs/esquema-er.md`](docs/esquema-er.md) | Diagrama ER del esquema SQL |
| [`docs/multi-tenancy.md`](docs/multi-tenancy.md) | Estrategia `clinica_id` |
| [`docs/cache-disponibilidad.md`](docs/cache-disponibilidad.md) | Diseño, claves, invalidación y pruebas de la caché Redis |
| [`docs/keycloak.md`](docs/keycloak.md) | Realm, roles, tokens, contrato seeder |
| [`docs/division-trabajo.md`](docs/division-trabajo.md) | Matías / Cristian / Elías |

## Pregunta rectora (defensa)

> Dos recepcionistas reservan el último espacio de la agenda exactamente al mismo tiempo. ¿Qué hace el sistema, por qué, y cómo se demuestra?
