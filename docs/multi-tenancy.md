# Multi-tenancy — comparación y decisión

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

## Decisión (Etapa 1)

**Estrategia principal: columna `clinicaId`.** **Segunda estrategia, ya implementada: RLS** ([`rls.md`](rls.md), migración [`03_rls.sql`](../db/migrations/03_rls.sql)).

La columna se queda como camino principal porque un solo esquema encaja con Compose, con Redis y con la prueba de carrera. RLS no la reemplaza: las consultas de la API siguen filtrando por la clínica del usuario. El motor es la red por si ese filtro falta.

**No elegida como principal:** esquema por tenant. Migrar N veces y enrutar `search_path` no aporta a la pregunta de la etapa (concurrencia dentro de una clínica).

## Cómo se asocia la identidad a la clínica

- Keycloak emite el token (roles de realm).
- La API valida firma/JWKS y obtiene `sub` + roles.
- `"Usuario"."idpSub"` = `sub`. De ahí salen `clinicaId` (NULL solo para `adm_plataforma`) y el rol de dominio.
- Esa clínica se copia a `app.clinica_id` en la conexión de la petición. La política RLS usa ese valor, no el query string.
- **La clínica no se confía desde el cliente** como fuente de autorización.

## Demostración de I5

1. Autenticar recepción de `clinica-norte`.
2. `GET /pacientes?clinicaId=clinica-sur` responde la lista de norte (el parámetro se ignora).
3. La misma consulta `SELECT count(*) FROM "Paciente" WHERE "clinicaId" = <sur>`, como `medicitas_app` con el GUC de norte, devuelve 0. Como superusuario (RLS no aplica) devuelve las filas. Un `INSERT` hacia sur con el GUC de norte lo rechaza el motor (`42501`).
4. Sin GUC, el conteo de `"Paciente"` es 0. Con `app.rol = adm_plataforma`, se ven varias clínicas.

La prueba es `TestIntegrationRLS`. La medición de 100 repeticiones queda en [`rls.md`](rls.md).
