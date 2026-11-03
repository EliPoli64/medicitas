# Keycloak en MediCitas

Guía del IdP (RF17 / I5). Responsable: **Matías**. Redis lo lleva Cristian; la semilla canónica de usuarios, Elías.

## Mapa mental (TC1 → MediCitas)

| Concepto | Qué es | TC1 | MediCitas |
|----------|--------|-----|-----------|
| **Realm** | Espacio aislado de usuarios/roles/clients | `tareauno` | `medicitas` |
| **Client** | App que pide tokens | `api` (público + password grant) | igual: `api` |
| **Realm role** | Rol en el JWT (`realm_access.roles`) | `reserva-writer` | `adm-plataforma`, `adm-clinica`, `recepcion`, `profesional` |
| **Claim clínica** | De dónde sale el tenant | no existía | atributo de usuario `clinica_id` → claim homónimo en el access token |
| **Users de prueba** | Quién obtiene tokens | en el JSON del realm | **temporales** en el JSON; **canónicos** los crea el seeder (Admin API) |

Regla de diseño: la API **no** confía un `clinica_id` del body/query para autorizar. Lee identidad del token (`sub` + roles + `clinica_id`) y cruza con `usuario` en Postgres.

## Archivos

| Ruta | Rol |
|------|-----|
| [`id-provider/import/medicitas-realm.json`](../id-provider/import/medicitas-realm.json) | Realm importable |
| [`id-provider/Dockerfile`](../id-provider/Dockerfile) | Copia `import/` a `/opt/keycloak/data/import/` |
| [`docker-compose.yml`](../docker-compose.yml) | `keycloak` con `start-dev --import-realm` |
| [`.env.example`](../.env.example) | `KC_ISSUER_URL` (host externo) y `KC_JWKS_URL` (DNS interno `keycloak`) |

## Users temporales (hasta el seeder)

Documentados también en [`.env.example`](../.env.example) (`SEED_*`). Hoy el realm JSON trae los mismos valores; el seeder debería leer las vars, no duplicar passwords en código.

| Variable user | Variable pass | Rol | `clinica_id` |
|---------------|---------------|-----|--------------|
| `SEED_USER_PLAT_ADMIN` (`plat-admin`) | `SEED_PASS_PLAT_ADMIN` | `adm-plataforma` | *(ausente)* |
| `SEED_USER_RECEP_NORTE` (`recep-norte`) | `SEED_PASS_RECEP_NORTE` | `recepcion` | `SEED_RECEP_NORTE_CLINICA_ID` |

Cuando exista el seeder de Elías, esa lista canónica manda; estos dos sirven para probar tokens ya.

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

## Contrato con Elías (seeder)

1. **Nosotros (realm JSON):** realm, 4 roles, client `api`, mapper `clinica_id`.
2. **Seeder:** crea usuarios en Keycloak (Admin API) alineados con filas `usuario` / clínicas en Postgres; lee `SEED_*` del entorno (local: `.env`; CI: variables de GitLab; prod: secret manager).
3. Fuente documentada de users/pass demo: `.env.example`. Canónica en runtime = seeder + env.
4. Roles y `client_id=api` no los reinventar en el seeder: deben coincidir con este realm.

### Checklist para el seeder (Admin API)

Al crear un usuario en Keycloak:

| Campo | Valor esperado |
|-------|----------------|
| Realm | `medicitas` |
| `realmRoles` | uno de: `adm-plataforma`, `adm-clinica`, `recepcion`, `profesional` |
| Atributo `clinica_id` | id de clínica en Postgres; **omitir** si el rol es `adm-plataforma` |
| Client para tokens de prueba | `api` (public + direct access grants ya en el realm) |
| Admin del contenedor | `KC_BOOTSTRAP_ADMIN_USERNAME` / `KC_BOOTSTRAP_ADMIN_PASSWORD` (token de admin master o del realm) |

Verified 2026-11-02: import OK; token `recep-norte` trae `realm_access.roles=["recepcion"]` y `clinica_id="clinica-norte"`; `plat-admin` trae `adm-plataforma` sin `clinica_id`.
