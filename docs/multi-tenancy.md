# Multi-tenancy — comparación y decisión (borrador)

El tenant es la **clínica**. El invariante **I5** exige que ningún dato de una clínica sea alcanzable desde otra (rutas, parámetros manipulados, listados).

## Estrategias comparadas

### 1. Columna discriminadora (`clinica_id`)

Una sola base y un solo esquema. Casi todas las tablas de hechos llevan `clinica_id`. La API deriva la clínica de la **identidad** (token / usuario), no del body libre.

| Ventajas | Costos |
|----------|--------|
| Un solo pipeline de migraciones | Disciplina: todo query debe filtrar |
| Semilla y pruebas simples | Un bug de filtro = fuga (hay que probar I5) |
| Encaja con Redis (clave por clínica/profesional) | Menos aislamiento “físico” |

### 2. Esquema por tenant (`clinica_norte.*`)

Un schema PostgreSQL por clínica (o una BD por clínica).

| Ventajas | Costos |
|----------|--------|
| Aislamiento más fuerte a nivel de namespace | Migraciones N veces / tooling más pesado |
| Menos riesgo de olvido de `WHERE` | Adm. plataforma y reportes cross-tenant complicados |
| | Connection routing / `search_path` |

### 3. Row-Level Security (RLS)

Políticas en el motor: `SET app.clinica_id` y políticas `USING (clinica_id = …)`.

| Ventajas | Costos |
|----------|--------|
| Defensa en profundidad aunque la app falle | Hay que cablear el contexto en cada sesión |
| Buena narrativa de seguridad | Más complejo de depurar |

## Decisión propuesta (Etapa 1)

**Estrategia principal: columna `clinica_id`.**

Motivos:

1. Un solo Compose/Postgres encaja con el reuso de la tarea corta.
2. Las pruebas de fuga (manipular `clinica_id` en query params) son directas.
3. Redis y la prueba de carrera se razonan sobre un solo modelo físico.
4. Deja abierta la puerta al módulo extra: **RLS en paralelo** y comparar.

**No elegida como principal:** esquema por tenant — sobrecosto operativo alto para el tamaño del curso, sin ganar tanto en la pregunta rectora (concurrencia dentro de una clínica).

## Cómo se asocia la identidad a la clínica

Pendiente de detalle en tercio B; regla de diseño:

- Keycloak emite el token (roles de realm).
- La app valida firma/JWKS y obtiene `sub` + roles.
- `usuario.idp_sub` ↔ `sub`; de ahí se lee `clinica_id` (NULL solo para `adm-plataforma`).
- **La clínica no se confía desde el cliente** como fuente de autorización.

## Demostración de I5 (cuando exista código)

1. Autenticar un usuario de `clinica-norte`.
2. Intentar leer/escribir recursos de `clinica-sur` (path, query, body).
3. Esperar 403/404/lista vacía — nunca datos del otro tenant.
