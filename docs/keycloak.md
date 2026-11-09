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

Regla de diseño: la API **no** confía un `clinica_id` del body/query para autorizar. Lee identidad del token (`sub` + roles + `clinica_id`) y cruza con `usuario` en Postgres.

## Archivos

| Ruta | Rol |
|------|-----|
| [`id-provider/import/medicitas-realm.json`](../id-provider/import/medicitas-realm.json) | Realm importable |
| [`id-provider/Dockerfile`](../id-provider/Dockerfile) | Copia `import/` a `/opt/keycloak/data/import/` |
| [`docker-compose.yml`](../docker-compose.yml) | `keycloak` con `start-dev --import-realm`; `api` recibe `KC_ISSUER_URL` / `KC_JWKS_URL` |
| [`api/auth.go`](../api/auth.go) | Valida JWT (JWKS, iss, exp) y expone `authRequired` / `requireRole` |
| [`.env.example`](../.env.example) | `KC_*`, `SEED_VERSION` y `SEED_USER_PASSWORD` |

## La API valida el token

La API **no** hace login. El cliente pide el token a Keycloak y lo manda en `Authorization: Bearer …`.

1. Middleware lee el Bearer.
2. Descarga/cachea llaves públicas desde `KC_JWKS_URL` (dentro de Compose: host `keycloak`).
3. Verifica firma RS256, `iss` (`KC_ISSUER_URL` = localhost) y expiración.
4. Lee `sub`, `preferred_username`, `realm_access.roles` y `clinica_id`.
5. Sin token / token malo → **401**. Token OK sin el rol exigido por la ruta → **403**.

`GET /me` solo exige token válido (cualquier rol) y responde la identidad. Rutas futuras usan `requireRole("recepcion")` (etc.) para la matriz RF17.

```bash
# 1) Token
TOKEN=$(curl -s -X POST "http://localhost:6767/realms/medicitas/protocol/openid-connect/token" \
  -d "client_id=api" -d "username=recep-norte" -d "password=recep123" -d "grant_type=password" \
  | jq -r .access_token)

# 2) Identidad
curl -s http://localhost:8080/me -H "Authorization: Bearer $TOKEN"
```

Ejemplo de respuesta:

```json
{
  "sub": "…",
  "username": "recep-norte",
  "roles": ["recepcion"],
  "clinicaId": "clinica-norte"
}
```

Para `plat-admin`, `clinicaId` es `null`.

## Autorización (matriz RF17)

Tras validar el token, la API decide **qué operaciones** puede intentar el rol (`api/permissions.go`).

- `Can(roles, op)` — permiso base de la matriz del enunciado.
- `requirePerm(op)` — middleware para rutas futuras (401 / 403).
- `GET /permisos` — lista las operaciones permitidas para el token actual (demo).

401 = identidad inválida. 403 = identidad OK pero la operación no está permitida para ese rol.

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
