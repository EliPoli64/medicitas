# Keycloak en MediCitas

Guía del IdP (RF17 / I5). Responsable: **Matías**. Redis lo lleva Cristian; la semilla canónica de usuarios, Elías.

## Mapa mental (TC1 → MediCitas)

| Concepto | Qué es | TC1 | MediCitas |
|----------|--------|-----|-----------|
| **Realm** | Espacio aislado de usuarios/roles/clients | `tareauno` | `medicitas` |
| **Client** | App que pide tokens | `api` (público + password grant) | igual: `api` |
| **Realm role** | Rol en el JWT (`realm_access.roles`) | `reserva-writer` | `adm-plataforma`, `adm-clinica`, `recepcion`, `profesional` |
| **Claim clínica** | De dónde sale el tenant | no existía | atributo de usuario `clinica_id` → claim homónimo en el access token |
| **Users de prueba** | Quién obtiene tokens | en el JSON del realm | dos base en el JSON; el resto los crea/verifica el seed (Admin API) |

Regla de diseño: la API **no** confía un `clinica_id` del body/query para autorizar. Lee identidad del token (`sub` + roles + `clinica_id`) y **resuelve la fila** `"Usuario"` en Postgres por `idpSub = JWT.sub`. La clínica de dominio sale de `"Usuario"."clinicaId"` → `"Clinica"."identificador"`, no del body.

## Archivos

| Ruta | Rol |
|------|-----|
| [`id-provider/import/medicitas-realm.json`](../id-provider/import/medicitas-realm.json) | Realm importable |
| [`id-provider/Dockerfile`](../id-provider/Dockerfile) | Copia `import/` a `/opt/keycloak/data/import/` |
| [`docker-compose.yml`](../docker-compose.yml) | `keycloak` con `start-dev --import-realm` y `KC_HOSTNAME=localhost` (iss estable); `api` recibe `KC_ISSUER_URL` / `KC_JWKS_URL` |
| [`api/auth.go`](../api/auth.go) | Valida JWT (JWKS, iss, exp) y expone `authRequired` / `requireRole` |
| [`api/users.go`](../api/users.go) | `UserStore` / `loadDomainUser`: `sub` → `"Usuario"` |
| [`api/db.go`](../api/db.go) | Pool Postgres (`pgx`) y ping para `/ready` |
| [`.env.example`](../.env.example) | `KC_*`, `SEED_VERSION` y `SEED_USER_PASSWORD` |

## La API valida el token

La API **no** hace login. El cliente pide el token a Keycloak y lo manda en `Authorization: Bearer …`.

1. Middleware lee el Bearer.
2. Descarga/cachea llaves públicas desde `KC_JWKS_URL` (dentro de Compose: host `keycloak`).
3. Verifica firma RS256, `iss` (`KC_ISSUER_URL` = localhost) y expiración.
4. Lee `sub`, `preferred_username`, `realm_access.roles` y `clinica_id`.
5. Sin token / token malo → **401**. Token OK sin el rol exigido por la ruta → **403**.

`GET /me` y `GET /permisos` exigen token válido **y** fila `"Usuario"` activa (`authRequired` + `loadDomainUser`). Rutas futuras usan `requirePerm(...)` / `requireRole(...)` para la matriz RF17; `Can(...)` sigue leyendo roles del JWT.

```bash
# 1) Token
TOKEN=$(curl -s -X POST "http://localhost:6767/realms/medicitas/protocol/openid-connect/token" \
  -d "client_id=api" -d "username=recep-norte" -d "password=recep123" -d "grant_type=password" \
  | jq -r .access_token)

# 2) Identidad (JWT + fila Postgres)
curl -s http://localhost:8080/me -H "Authorization: Bearer $TOKEN"
```

Ejemplo de respuesta:

```json
{
  "sub": "…",
  "username": "recep-norte",
  "roles": ["recepcion"],
  "clinicaIdClaim": "clinica-norte",
  "usuarioId": "…",
  "idpSub": "…",
  "activo": true,
  "clinicaIdentificador": "clinica-norte",
  "rolCodigo": "recepcion"
}
```

Para `plat-admin`, `clinicaIdentificador` y `clinicaIdClaim` son `null`. Un JWT válido cuyo `sub` no exista en `"Usuario"` → **403** (`usuario no registrado`). Usuario con `activo=false` → **403**.

`GET /ready` exige config Keycloak **y** `Ping` a Postgres (503 si la BD cae).

### Primera ruta de dominio: `GET /pacientes`

Lista pacientes **activos** de la clínica del Usuario de dominio (`requirePerm(PacientesLectura)` + `loadDomainUser`). El tenant sale de Postgres; un `?clinicaId=` en el query **se ignora** (I5).

```bash
curl -s "http://localhost:8080/pacientes?clinicaId=clinica-sur" \
  -H "Authorization: Bearer $TOKEN"
# Con recep-norte → solo pacientes de clinica-norte
```

`plat-admin` → **403** (sin permiso de lectura de pacientes). Archivo: [`api/patients.go`](../api/patients.go).

## Asociación IdP ↔ Usuario

| Capa | Qué guarda | Cómo se une |
|------|------------|-------------|
| Keycloak | `sub` del access token | identidad OIDC |
| Postgres `"Usuario"."idpSub"` | mismo UUID que el `sub` | el seed lo escribe al crear/verificar el user |
| Postgres `"Usuario"."clinicaId"` | FK a `"Clinica"` | tenant de dominio (preferido para autorizar/aislar) |
| Claim JWT `clinica_id` | atributo Keycloak | útil al cliente; la API no lo usa como única verdad |

Archivos: [`api/db.go`](../api/db.go) (pool/pgx), [`api/users.go`](../api/users.go) (`UserStore.FindByIdpSub`, `loadDomainUser`).

Códigos de rol en BD (`adm_plataforma`, `adm_clinica`, …) vs realm (`adm-plataforma`, …): el seed los alinea; `Can` / `requirePerm` usan el rol **JWT**.

## Autorización (matriz RF17)

Tras validar el token y resolver el usuario de dominio, la API decide **qué operaciones** puede intentar el rol (`api/permissions.go`).

- `Can(roles, op)` — permiso base de la matriz del enunciado (roles JWT).
- `requirePerm(op)` — middleware en rutas de dominio (401 / 403); ya usado en `GET /pacientes`.
- `GET /permisos` — lista las operaciones permitidas para el token actual (demo) e incluye `usuarioId` / `rolCodigo` de BD.

401 = identidad inválida. 403 = no registrado / inactivo, o identidad OK pero la operación no está permitida para ese rol.

Filas “solo propios” (horarios, notas): el rol `profesional` pasa la matriz; el handler del recurso debe comprobar ownership.

```bash
TOKEN=$(curl -s -X POST "http://localhost:6767/realms/medicitas/protocol/openid-connect/token" \
  -d "client_id=api" -d "username=recep-norte" -d "password=recep123" -d "grant_type=password" \
  | jq -r .access_token)

curl -s http://localhost:8080/permisos -H "Authorization: Bearer $TOKEN"
```

Ejemplo recepción: incluye `ReservarReprogramarCancelar`, no `AltaClinica` ni `Bitacora`.

## Users de la semilla

La fuente canónica de usuarios demo está en [`seed/catalog.py`](../seed/catalog.py), pero el realm importado conserva dos users base: `plat-admin` y `recep-norte`. El seed crea/verifica el resto usando la Admin API y no reescribe passwords de users existentes.

| Usuario | Password | Rol | `clinica_id` |
|---------|----------|-----|--------------|
| `plat-admin` | `plat123` | `adm-plataforma` | *(ausente — OK para plataforma)* |
| `recep-norte` | `recep123` | `recepcion` | `clinica-norte` |
| `adm-norte`, `adm-centro`, `adm-sur` | `SEED_USER_PASSWORD` | `adm-clinica` | su clínica |
| `recep-centro`, `recep-sur` | `SEED_USER_PASSWORD` | `recepcion` | su clínica |
| `dra-garcia`, `dr-rojas` | `SEED_USER_PASSWORD` | `profesional` | `clinica-norte` |
| `dra-jimenez`, `dr-mora` | `SEED_USER_PASSWORD` | `profesional` | `clinica-centro` |
| `dra-vargas`, `dr-solano` | `SEED_USER_PASSWORD` | `profesional` | `clinica-sur` |

Los dos users base se importan con el realm. El seed los actualiza sin enviar credenciales, así que **no cambia sus passwords**. Los users nuevos reciben `SEED_USER_PASSWORD` la primera vez.

## Cómo pedir un token

Con Keycloak arriba (`http://localhost:6767`):

```bash
curl -s -X POST "http://localhost:6767/realms/medicitas/protocol/openid-connect/token" \
  -d "client_id=api" \
  -d "username=recep-norte" \
  -d "password=recep123" \
  -d "grant_type=password"
```


El `access_token` debe incluir:

- `iss`: `http://localhost:6767/realms/medicitas`
- `realm_access.roles`: p. ej. `recepcion`
- `clinica_id`: `clinica-norte` (en `recep-norte`)

Admin de consola Keycloak: usuario/contraseña de `KC_BOOTSTRAP_ADMIN_*` en `.env`.

## Reimportar el realm

Keycloak solo importa en un volumen **nuevo**. Si cambias el JSON y no ves el cambio:

```bash
docker compose down -v
docker compose up --build db keycloak
```

(`-v` borra datos de Postgres también.)

## Contrato con el seed

1. **Realm JSON:** realm, 4 roles, client `api`, mapper `clinica_id` y los users base.
2. **Seed (`seed/keycloak_admin.py`):** crea/verifica usuarios con la Admin API y asigna exactamente un rol de la matriz.
3. La lista canónica de users vive en [`seed/catalog.py`](../seed/catalog.py). El realm solo conserva `plat-admin` y `recep-norte` como users base.
4. Roles y `client_id=api` no se reinventan en el seed: se leen del realm importado.
5. Keycloak 26 descarta atributos que no estén declarados en el perfil de usuario. El seed agrega `clinica_id` al perfil de forma idempotente antes de crear users.

### Checklist del seed (Admin API)

| Campo | Valor esperado |
|-------|----------------|
| Realm | `medicitas` |
| `realmRoles` | uno de: `adm-plataforma`, `adm-clinica`, `recepcion`, `profesional` |
| Atributo `clinica_id` | `identificador` de la clínica en Postgres; **omitir** si el rol es `adm-plataforma` |
| Client para tokens de prueba | `api` (public + direct access grants ya en el realm) |
| Admin del contenedor | `KC_BOOTSTRAP_ADMIN_USERNAME` / `KC_BOOTSTRAP_ADMIN_PASSWORD` (token de admin master) |

Verificado 2026-11-02: import OK; `recep-norte` mantiene `recep123`; `dra-garcia` usa `SEED_USER_PASSWORD`; ambos tokens traen `realm_access.roles` y `clinica_id="clinica-norte"`; `plat-admin` trae `adm-plataforma` sin `clinica_id`.

## Checklist de aceptación — identidad (cerrado)

Criterio del enunciado (*Identidad delegada*): realm/client al levantar, firma contra JWKS, roles → matriz, clínica asociada a la identidad (I5).

| # | Criterio | Evidencia |
|---|----------|-----------|
| 1 | Keycloak en Compose con realm versionado | `id-provider/import/medicitas-realm.json` + servicio `keycloak` |
| 2 | Client `api` + 4 roles de la matriz | mismo JSON |
| 3 | Claim `clinica_id` en access token | mapper + atributo de usuario |
| 4 | API valida firma RS256 / iss / exp vía JWKS | `api/auth.go` + tests |
| 5 | Roles JWT → operaciones RF17 | `api/permissions.go`, `GET /permisos` |
| 6 | Asociación IdP ↔ clínica documentada e implementada | `"Usuario"."idpSub"` = JWT `sub`; `api/users.go` |
| 7 | Clínica de dominio desde Postgres (no body/query) | `GET /pacientes` ignora `?clinicaId=` |
| 8 | Claim JWT alineado con BD (si viene) | `assertClinicaClaimAligned` → 403 si diverge |
| 9 | Rutas protegidas con patrón `secure(op)` | `requirePerm` + `loadDomainUser` |
| 10 | `/ready` refleja dependencia real de Postgres | Ping en `api/db.go` |
| 11 | Colección de peticiones versionada | [`docs/http/identidad.http`](http/identidad.http) |
| 12 | Test de integración contra Compose | `go test -tags=integration` (`identity_integration_test.go`) |

### Cómo verificar en local

```bash
docker compose up --build -d
# unitarios
docker run --rm -v "$PWD:/app" -w /app/api golang:1.25-alpine go test ./...
# integración: el token debe pedirse al MISMO host que KC_ISSUER_URL (localhost),
# si no el `iss` del JWT no coincide y la API responde 401.
docker run --rm --add-host=host.docker.internal:host-gateway \
  -e API_BASE=http://host.docker.internal:8080 \
  -e KC_TOKEN_URL=http://host.docker.internal:6767/realms/medicitas/protocol/openid-connect/token \
  -v "$PWD:/app" -w /app/api golang:1.25-alpine \
  go test -tags=integration -count=1 -run TestIntegrationIdentity ./...
```

En Windows (PowerShell), smoke: [`scripts/smoke-identidad.ps1`](../scripts/smoke-identidad.ps1).
