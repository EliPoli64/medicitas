# Caché de disponibilidad en Redis

Documento de diseño del módulo Redis de MediCitas (RF7, I7, I8).

**Estado:** el paquete [`api/internal/availabilitycache`](../api/internal/availabilitycache/) está implementado, conectado a RF7 a través de [`api/availability.go`](../api/availability.go) e integrado con PostgreSQL mediante [`api/internal/availabilityversion`](../api/internal/availabilityversion/). La fuente de verdad sigue siendo PostgreSQL; Redis solo guarda snapshots derivados de la disponibilidad. I8 se demuestra con una prueba de integración que reserva una cita vía SQL y verifica que la siguiente consulta RF7 ya no ofrece el espacio, validando el snapshot con `AgendaVersion`.

Este documento describe la arquitectura actual, las claves, la doble barrera de invalidación (epochs + `AgendaVersion`), el fallback, las pruebas, un benchmark comparativo y las limitaciones pendientes.

---

## 1. Arquitectura

```text
Cliente HTTP
    │
    ▼
API / handler de disponibilidad (RF7/RF8)
    │
    ├── loaderDisponibilidad.Load(ctx, Request) ──► PostgreSQL (fuente de verdad)
    │                                                  ▲
    │                                                  │ horarios, bloqueos,
    │                                                  │ duración y citas vigentes
    ▼                                                  │
availabilitycache.Cache ──── Store ──── RedisStore ──── Redis
    │
    ├── AgendaVersionResolver ────────────────────► PostgreSQL (AgendaVersion)
    │
    └── InvalidateAgenda(ctx, clinicaID, profesionalID)
```

El paquete `availabilitycache` no conoce HTTP, PostgreSQL ni Keycloak. Recibe:

- un `LoaderFunc` ([`api/availability_loader.go`](../api/availability_loader.go)) que calcula la disponibilidad desde PostgreSQL;
- un `Store` ([`api/internal/availabilitycache/redis.go`](../api/internal/availabilitycache/redis.go)) que abstrae Redis;
- un `AgendaVersionResolver` opcional ([`api/internal/availabilityversion/postgres.go`](../api/internal/availabilityversion/postgres.go)) que consulta la versión autoritativa de la agenda.

Esa separación permite:

- probar la política de caché con un `Store` falso, sin levantar Redis;
- cambiar la implementación de Redis sin tocar la lógica;
- mantener la fuente de verdad en PostgreSQL mientras Redis acelera lecturas repetidas.

### Componentes

| Componente | Archivo | Responsabilidad |
|---|---|---|
| `Cache` | [`cache.go`](../api/internal/availabilitycache/cache.go) | `GetOrLoad` e `InvalidateAgenda`; validación por `AgendaVersion` |
| Configuración | [`config.go`](../api/internal/availabilitycache/config.go) | Validación de `Config` |
| `keyBuilder` | [`keys.go`](../api/internal/availabilitycache/keys.go) | Construir y validar claves (formato v2 incluye `pasoMinutos`) |
| `RedisStore` | [`redis.go`](../api/internal/availabilitycache/redis.go) | `Get`, `Set`, `SetNX`, `Ping`, `Close` sobre go-redis |
| Epochs | [`epoch.go`](../api/internal/availabilitycache/epoch.go) | Tokens aleatorios, lectura e inicialización concurrente |
| Códec | [`codec.go`](../api/internal/availabilitycache/codec.go) | Serializar, normalizar y validar `Snapshot` v3 |
| Contratos | [`interfaces.go`](../api/internal/availabilitycache/interfaces.go), [`types.go`](../api/internal/availabilitycache/types.go) | `Store`, `LoaderFunc`, `AgendaVersionResolver`, `Request`, `Snapshot`, `Result`, `Config` |
| Errores | [`errors.go`](../api/internal/availabilitycache/errors.go) | Errores centinela para distinguir fallos |
| Loader RF7 | [`availability_loader.go`](../api/availability_loader.go) | Adapta el cálculo de RF7 al contrato `LoaderFunc` |
| Resolver `AgendaVersion` | [`availabilityversion/postgres.go`](../api/internal/availabilityversion/postgres.go) | Consulta `AgendaVersion` en PostgreSQL |
| Handler RF7 | [`availability.go`](../api/availability.go) | Expone el endpoint y decide caché vs cálculo directo |

### Flujo de lectura

1. El handler RF7 valida la petición y construye un `Request` con la clínica tomada del token, nunca de un parámetro libre.
2. `GetOrLoad` obtiene el epoch vigente de `(clínica, profesional)`.
3. Construye la clave de datos con ese epoch.
4. Intenta leer Redis.
5. Si hay un `Snapshot` v3, consulta `AgendaVersion` en PostgreSQL.
6. Si la versión del snapshot coincide con la versión autoritativa, responde con `Hit: true`.
7. Si la versión difiere, el epoch cambió, Redis falla, no hay entrada o el valor es inválido, recurre al `Loader`.
8. Cuando corresponde, guarda el resultado bajo el epoch leído y etiqueta el snapshot con la `AgendaVersion` actual.

### Flujo de escritura (al reservar/cancelar/reprogramar)

1. La API ejecuta la mutación en PostgreSQL dentro de una transacción.
2. Los triggers de `Cita`, `Bloqueo` y `HorarioAtencion` incrementan `AgendaVersion` del profesional afectado.
3. Si el COMMIT es exitoso, llama a `InvalidateAgenda(ctx, clínica, profesional)` para publicar un epoch nuevo.
4. La próxima consulta encuentra un epoch nuevo o una `AgendaVersion` distinta y recalcula la agenda.
5. Si la invalidación del epoch falla, `AgendaVersion` sigue actuando como segunda barrera: el snapshot cacheado se descarta al no coincidir con la versión autoritativa.

---

## 2. Claves

### Estructura

```text
{prefix}:avail:{format}:epoch:{clinicaId}:{profesionalId}
{prefix}:avail:{format}:data:{clinicaId}:{profesionalId}:{servicioId}:{fecha}:{pasoMinutos}:{epoch}
```

Con la configuración por defecto:

```text
medicitas:avail:v2:epoch:clinica-norte:prof-0001
medicitas:avail:v2:data:clinica-norte:prof-0001:serv-001:2026-11-10:30:3f2a9c...e1
```

### Por qué cada segmento

| Segmento | Motivo |
|---|---|
| `medicitas` | Namespace del proyecto dentro de una instancia Redis compartida |
| `avail` | Separar la caché de disponibilidad de otros usos futuros |
| `v2` | Versión del formato de claves; incluye `pasoMinutos` porque distintas rejillas generan espacios distintos |
| `clinicaId` | Aislamiento por tenant (I5). La clínica viene del contexto autenticado |
| `profesionalId` | La agenda y el epoch se manejan por profesional |
| `servicioId` | La duración del servicio determina los espacios disponibles (RF7) |
| `fecha` | Fecha local de la clínica, en formato estricto `YYYY-MM-DD` |
| `pasoMinutos` | Rejilla de inicios; un mismo servicio y fecha con pasos distintos no comparten snapshot |
| `epoch` | Barrera de invalidación; una entrada vieja queda inalcanzable |

El alcance de una entrada es **una combinación de clínica, profesional, servicio, fecha, paso y epoch**. Una reserva publica un epoch nuevo para todo el profesional, por lo que invalida **todas sus fechas, servicios y pasos**. Es una invalidación conservadora: puede reducir la tasa de aciertos, pero evita tener que enumerar claves o mantener un índice de entradas.

### Validación

Antes de construir una clave se valida:

- IDs con el patrón `[A-Za-z0-9_-]{1,128}`; se rechazan vacíos, espacios, `:` y caracteres fuera del patrón;
- fecha en formato `YYYY-MM-DD`, sin variantes como `2026-1-1`;
- epoch de exactamente 32 caracteres hexadecimales en minúscula;
- prefijo y versión de formato con caracteres seguros.

Estas reglas evitan colisiones por concatenación, claves ambiguas y valores inesperados en Redis.

### Versionado

Hay dos versiones independientes:

- la versión de **claves** (`v2` en el prefijo) permite cambiar la estructura de claves sin leer datos incompatibles; `v2` agrega `pasoMinutos` a la clave de datos;
- `SnapshotSchemaVersion` (`3` en el valor) permite cambiar la estructura del JSON. Un valor con otra versión se considera inválido, se recalcula y se reemplaza. `v3` agrega `AgendaVersion`.

---

## 3. Doble barrera: epochs + `AgendaVersion`

### Epochs

Cada agenda `(clínica, profesional)` tiene un token llamado **epoch**:

- se genera con `crypto/rand` (128 bits, 32 caracteres hexadecimales);
- se guarda en la clave de epoch;
- forma parte de todas las claves de datos.

`InvalidateAgenda` genera un token nuevo y lo publica con `SET`. No borra las claves de datos anteriores.

```text
Antes:   epoch = E1
         data:...:E1  → snapshot viejo

Invalidar: SET epoch = E2

Después: epoch = E2
         data:...:E2  → no existe todavía (próxima consulta recalcula)
         data:...:E1  → sigue en Redis, pero es inalcanzable
```

Las claves huérfanas se eliminan solas cuando vence su TTL de datos.

### `AgendaVersion` en PostgreSQL

El esquema incluye la tabla `AgendaVersion (clinicaId, profesionalId, version)`, mantenida por triggers sobre `HorarioAtencion`, `Bloqueo` y `Cita`. Cada cambio relevante en la agenda incrementa la versión del profesional afectado.

El `Snapshot` v3 guarda la versión observada al momento de calcular. En un cache hit, `GetOrLoad` consulta la versión autoritativa y solo sirve el snapshot si coinciden. Esto actúa como una **segunda barrera de invalidación** independiente de Redis:

- si `InvalidateAgenda` falla y el epoch antiguo sigue vigente, el snapshot cacheado se descarta porque su `AgendaVersion` ya no coincide;
- si Redis conserva un snapshot huérfano bajo un epoch que se regeneró, la versión lo detecta;
- si un snapshot antiguo sin `AgendaVersion` (schema v2 o menor) aparece en Redis, se recalcula.

### Por qué no solo `DEL`

Borrar las entradas de una agenda requeriría:

- conocer todas las fechas y servicios posibles, o recorrer Redis con `SCAN`;
- coordinar el borrado con lectores que pueden volver a escribir datos viejos;
- ejecutar múltiples `DEL` sin atomicidad entre ellos;
- arriesgar borrar claves de otra clínica si el patrón está mal construido.

Un `SCAN` es costoso y no ofrece una barrera clara: entre el listado y el borrado puede aparecer una entrada nueva. El epoch convierte la invalidación en **una sola escritura**.

### Por qué no solo TTL

El TTL es una red de seguridad, no una política de invalidación. Si la única forma de reflejar una reserva fuera esperar la expiración:

- la siguiente consulta podría mostrar el horario como libre durante varios minutos;
- se violaría I8 de forma directa;
- la consigna penaliza explícitamente una caché que depende solo de expiración por tiempo con ventanas de incoherencia admitidas.

Por eso el TTL de datos existe, pero no es el mecanismo principal.

### Por qué un token aleatorio y no un contador `INCR`

Un contador incremental parece más simple, pero tiene un problema: si el epoch se pierde o expira y se reinicia el contador, un valor viejo podría volver a coincidir y hacer alcanzable una entrada anterior. Un token aleatorio de 128 bits hace que reutilizar un epoch sea inviable en la práctica, incluso si la clave desaparece y se regenera.

### Qué garantiza y qué no

**Garantiza**, cuando el `SET` del epoch tiene éxito:

- toda lectura que comience después usa el epoch nuevo;
- las entradas anteriores dejan de ser alcanzables;
- no hace falta borrar ni enumerar claves.

**No garantiza:**

- que una lectura ya en curso termine con datos nuevos;
- que el `SET` del epoch no falle (aunque `AgendaVersion` mitiga el riesgo de servir datos obsoletos);
- que un lector que ya obtuvo el epoch viejo no escriba bajo ese epoch (la escritura queda inalcanzable, pero ocupa memoria hasta el TTL);
- consistencia estricta bajo particiones de red prolongadas sin intervención operativa.

---

## 4. Funcionamiento

### `GetOrLoad`

```go
func (c *Cache) GetOrLoad(ctx context.Context, req Request, load LoaderFunc) (Result, error)
```

Secuencia:

1. Valida `Request` y `Loader`.
2. Obtiene el epoch vigente con `currentEpoch`.
3. Construye la clave de datos.
4. Lee Redis con un timeout de operación.
5. Si el valor existe y es un `Snapshot` v3, consulta `AgendaVersion` en PostgreSQL.
6. Si la versión coincide, devuelve `Hit: true`.
7. Si el valor existe pero es inválido o la versión difiere, recalcula, intenta reemplazarlo y devuelve `CacheErr` con `ErrInvalidPayload`.
8. Si no existe, recalcula, guarda y devuelve `Hit: false`.
9. Si Redis falla, recalcula y devuelve `Hit: false` con `ErrCacheRead` o `ErrCacheWrite` en `CacheErr`.

Cuando se recalcula, `GetOrLoad` lee `AgendaVersion` antes y después del `Loader`. Si cambia durante el cálculo, devuelve el snapshot con `ErrStaleSnapshot` (el handler RF7 responde HTTP 503 en ese caso).

| Situación | `Hit` | `CacheErr` | ¿Se guarda? |
|---|---|---|---|
| Entrada válida | `true` | `nil` | No hace falta |
| Sin entrada | `false` | `nil` | Sí |
| Payload inválido | `false` | `ErrInvalidPayload` | Sí, si la escritura funciona |
| Fallo de lectura de datos | `false` | `ErrCacheRead` | No |
| Fallo de inicialización del epoch | `false` | `ErrCacheWrite` | No |
| Fallo de escritura de datos | `false` | `ErrCacheWrite` | No |
| Fallo del `Loader` | — | — | No; se devuelve error |

Un `CacheErr` no fatal significa: **la respuesta es correcta porque vino del `Loader`; la caché no pudo usarse o actualizarse**.

### `currentEpoch`

1. Lee la clave de epoch.
2. Si el valor es un token válido, lo devuelve.
3. Si el valor existe pero está corrupto, publica un token nuevo y lo devuelve.
4. Si la clave no existe, genera un token y lo publica con `SETNX`.
5. Si `SETNX` no crea la clave porque otro lector ganó, relee el token ganador.
6. Reintenta hasta 5 veces; si no lo logra, devuelve `ErrCacheRead`.

`SETNX` es importante: evita que dos instancias de la API creen epochs distintos al mismo tiempo. Solo una gana; la otra adopta el token publicado.

### `InvalidateAgenda`

```go
func (c *Cache) InvalidateAgenda(ctx context.Context, clinicaID, profesionalID string) error
```

1. Valida los IDs.
2. Construye la clave de epoch.
3. Genera un token nuevo.
4. Lo publica con `SET` y el TTL de epoch.
5. Devuelve `ErrCacheInvalidation` si la publicación falla.

No borra claves de datos y no recorre patrones.

### El `Loader`

```go
type LoaderFunc func(ctx context.Context, req Request) (Snapshot, error)
```

Es la fuente de verdad. Debe:

- calcular la disponibilidad desde PostgreSQL (RF7);
- devolver un `Snapshot` completo con clínica, profesional, servicio, fecha y zona horaria;
- usar la fecha local de la clínica;
- devolver espacios con inicio y fin en un instante válido;
- propagar errores de la base sin intentar “arreglarlos” con datos vacíos.

La caché normaliza los instantes a UTC y valida el snapshot contra el `Request` antes de guardarlo o servirlo. Además, copia la lista de espacios antes de modificarla: un `Loader` puede devolver un valor reutilizado y la caché no debe escribir en esa memoria.

### `Snapshot`

| Campo | Uso |
|---|---|
| `SchemaVersion` | Versión del JSON (`3`) |
| `ClinicaID` | Validar tenant |
| `ProfesionalID` | Validar agenda |
| `ServicioID` | Validar servicio |
| `Fecha` | Validar fecha |
| `ZonaHoraria` | Zona de la clínica usada para calcular |
| `DuracionMinutos` | Duración del servicio |
| `PasoMinutos` | Rejilla de inicios usada |
| `AgendaVersion` | Versión autoritativa de PostgreSQL; validación en HIT |
| `Espacios` | Lista de intervalos disponibles |
| `GeneradoEn` | Momento de generación; trazabilidad y diagnóstico |

---

## 5. TTL y configuración

### `Config`

| Campo | Valor inicial | Motivo | ¿Se puede cambiar? |
|---|---|---|---|
| `Prefix` | `medicitas` | Namespace | Sí, por despliegue |
| `FormatVersion` | `v2` | Versión de claves (incluye `pasoMinutos`) | Solo con migración coordinada |
| `DataTTL` | `5m` | Acota datos huérfanos y limita la ventana residual si fallan epoch y `AgendaVersion` | Sí; afecta frescura y tasa de aciertos |
| `EpochTTL` | `24h` | El epoch debe sobrevivir al TTL de datos | Sí; debe ser mayor que `DataTTL` |
| `OpTimeout` | `100ms` | Acota cada operación de Redis | Sí; ajustar según latencia observada |

`DataTTL` **no es el mecanismo de consistencia**. Es un respaldo para claves huérfanas y para el caso excepcional en que una invalidación no se publica. No se debe presentar como solución a I8.

`EpochTTL` puede ser mayor sin afectar la corrección: si el epoch expira, la próxima lectura crea uno nuevo y la caché queda fría. Un TTL de epoch menor que el de datos también es seguro, pero genera más recálculos.

`OpTimeout` se aplica a cada operación de Redis, no al `Loader`. El tiempo del `Loader` queda bajo el `context` del llamador.

### `RedisConfig`

| Campo | Uso |
|---|---|
| `Addr` | `host:puerto` |
| `Password` | Autenticación, si se configura |
| `DB` | Base lógica; las pruebas usan `15` |
| `DialTimeout` | Timeout de conexión |
| `ReadTimeout` | Timeout de lectura |
| `WriteTimeout` | Timeout de escritura |

`NewRedisStore` no hace `Ping` automáticamente. La decisión de arrancar, degradar o fallar si Redis no responde queda en la capa de integración.

La configuración se puede leer de variables de entorno (`REDIS_ADDR`, `REDIS_PASSWORD`, `REDIS_DB`) o pasarse explícitamente al construir `RedisStore`. Ver [`api/availability_cache.go`](../api/availability_cache.go) para el cableado actual.

### Migraciones requeridas

La caché asume que PostgreSQL tiene aplicadas las migraciones:

- [`db/migrations/01_schema.sql`](../db/migrations/01_schema.sql): tablas base, índices y triggers de `Cita`.
- [`db/migrations/02_agenda_version.sql`](../db/migrations/02_agenda_version.sql): tabla `AgendaVersion`, funciones `app.incrementarAgendaVersion*`, triggers que mantienen la versión al cambiar `Profesional`, `ProfesionalServicio`, `Servicio`, `HorarioAtencion`, `Bloqueo` y `Cita`.

Sin la migración 02, `availabilityversion.PostgresResolver` no encuentra la tabla y cada HIT se descarta, forzando recálculos continuos.

---

## 6. Manejo de fallos

| Operación que falla | Efecto | `Result.CacheErr` / error | ¿Se sirve el `Loader`? |
|---|---|---|---|
| Lectura del epoch | No se puede conocer la barrera vigente | `ErrCacheRead` | Sí |
| Inicialización del epoch (`SETNX`) | No hay epoch | `ErrCacheWrite` | Sí; no se escribe nada |
| Lectura de datos | No se puede comprobar la entrada | `ErrCacheRead` | Sí; no se sobrescribe lo existente |
| Escritura de datos | El resultado no queda cacheado | `ErrCacheWrite` | Sí |
| Invalidación | El epoch anterior sigue vigente | `ErrCacheInvalidation` | No aplica; el llamador decide |
| Payload inválido | El valor no es confiable | `ErrInvalidPayload` | Sí; se recalcula y se intenta reemplazar |
| Datos de otra clínica | Se rechaza el valor | `ErrInvalidPayload` | Sí; nunca se sirve |
| `Loader` con error | No hay respuesta de disponibilidad | Error del `Loader` | No; se propaga |
| `AgendaVersion` cambia durante el cálculo | El snapshot puede ser inconsistente | `ErrStaleSnapshot` | Sí; el handler responde 503 |

Puntos importantes:

- **Las lecturas son fail-open hacia PostgreSQL.** Si Redis no responde, la consulta sigue siendo correcta, solo más lenta.
- **La invalidación es fail-explicit.** No se oculta: si no se pudo publicar el epoch, el llamador recibe `ErrCacheInvalidation` y debe decidir.
- **`AgendaVersion` mitiga fallos de invalidación.** Incluso si el epoch no se publicó, un snapshot obsoleto se descarta al no coincidir con la versión autoritativa.
- **Un valor inválido nunca se sirve.** Se recalcula y se reemplaza.
- **Un fallo del `Loader` no se tapa con datos viejos.** La caché no devuelve una entrada anterior si el cálculo actual falla; el error se propaga.
- Los errores se pueden inspeccionar con `errors.Is` (`ErrCacheRead`, `ErrCacheWrite`, `ErrCacheInvalidation`, `ErrInvalidPayload`, `ErrStaleSnapshot`).

No hay circuit breaker ni métricas integradas. `CacheErr`, `ErrCacheInvalidation` y `ErrStaleSnapshot` existen para que la API los registre y decida.

---

## 7. Concurrencia

### Carreras probadas

| Escenario | Comportamiento verificado |
|---|---|
| Dos lectores inicializan el epoch | `SETNX` crea el epoch una sola vez; el otro relee el token ganador |
| Lectura en curso durante una invalidación | La lectura puede devolver datos viejos, pero escribe bajo el epoch viejo; no contamina el nuevo |
| Epoch expira durante una carga | La escritura queda bajo el epoch viejo y es inalcanzable; la próxima lectura inicializa otro epoch |
| Dos escritores con resultados distintos bajo el mismo epoch | La última escritura gana; no hay ordenamiento por frescura dentro de un epoch |
| Invalidación de una agenda | Afecta todas las fechas y servicios de ese profesional |
| Invalidación de una agenda | No afecta a otro profesional ni a otra clínica |
| Epoch corrupto | Se reemplaza por un token válido |

### Garantías

- Una lectura que **comienza después** de una invalidación exitosa usa el epoch nuevo.
- Una escritura bajo un epoch viejo es inalcanzable.
- La inicialización concurrente converge a un solo epoch.
- La pérdida del epoch no produce datos incorrectos; produce caché fría.
- La invalidación no requiere borrado masivo ni `FLUSHDB`.

### Limitaciones

- **Sin linealización para lecturas en vuelo.** Una lectura que ya empezó puede terminar con datos anteriores a la invalidación. Para el llamador es aceptable: no se cachean bajo el epoch nuevo.
- **Última escritura gana dentro del mismo epoch.** Si dos lectores calculan resultados distintos bajo el mismo epoch, el último `SET` prevalece. Si la base cambió, una invalidación correcta debería haber cambiado el epoch. Si la invalidación se perdió, una escritura lenta puede volver a dejar datos viejos y reiniciar el TTL; es el escenario de la sección 11.
- **Sin single-flight.** Varias consultas simultáneas con la misma clave pueden ejecutar el `Loader` más de una vez. Es correcto, pero genera carga innecesaria en PostgreSQL.
- **Sin escritura condicional.** La escritura de datos no verifica que el epoch siga vigente; como la clave incluye el epoch, una escritura vieja queda inalcanzable.
- **Coordinación entre instancias.** No hay estado local compartido: toda la coordinación pasa por las claves de Redis. Esto es una ventaja, pero también significa que un fallo de Redis afecta a todas las instancias.
- **Réplicas con retraso.** Si el `Loader` lee de una réplica atrasada, podría escribir datos viejos bajo un epoch nuevo. La integración debe leer disponibilidad del primario o agregar una versión de base.

---

## 8. Pruebas

### Cómo ejecutarlas

Con Go instalado, desde `api/`:

```bash
go test ./...
go test -race ./...
```

Pruebas de integración (requieren PostgreSQL y Redis; las de API usan DB 15):

```bash
docker compose up -d db redis       # desde la raíz del repositorio
cd api
DB_HOST=localhost DB_PORT=5432 DB_USER=medicitas DB_PASSWORD=cambiar_en_local DB_NAME=medicitas \
REDIS_ADDR=localhost:6379 REDIS_DB=15 \
  go test -tags=integration ./...
```

En PowerShell:

```powershell
docker compose up -d db redis
cd api
$env:DB_HOST = "localhost"; $env:DB_PORT = "5432"; $env:DB_USER = "medicitas"
$env:DB_PASSWORD = "cambiar_en_local"; $env:DB_NAME = "medicitas"
$env:REDIS_ADDR = "localhost:6379"; $env:REDIS_DB = "15"
go test -tags=integration ./...
```

Con detector de carreras:

```bash
go test -race -tags=integration ./...
```

Benchmark comparativo (PostgreSQL directo vs Redis HIT, incluyendo `AgendaVersion`):

```bash
DB_HOST=localhost DB_PORT=5432 DB_USER=medicitas DB_PASSWORD=cambiar_en_local DB_NAME=medicitas \
REDIS_ADDR=localhost:6379 REDIS_DB=15 \
  go test -tags=integration -run '^$' -bench '^BenchmarkRF7' -benchmem -count=5 .
```

La última verificación se ejecutó dentro de contenedores `golang:1.25` porque el entorno de desarrollo no tenía Go instalado en el host.

Variables reconocidas por las pruebas:

| Variable | Valor por defecto |
|---|---|
| `REDIS_ADDR` | `localhost:6379` |
| `REDIS_PASSWORD` | vacío |
| `REDIS_DB` | `15` |

Las pruebas de integración usan un prefijo único por prueba y limpian solo sus propias claves con `SCAN` + `DEL`. **No usan `FLUSHDB`** y no tocan datos de otros servicios.

### Inventario

#### Claves, configuración y almacenamiento

| Prueba | Qué demuestra |
|---|---|
| `TestKeyBuilder_DataKey` | La clave de datos tiene el formato esperado |
| `TestKeyBuilder_EpochKey` | La clave de epoch tiene el formato esperado |
| `TestKeyBuilder_TenantIsolation` | Clínicas distintas no colisionan |
| `TestKeyBuilder_RejectsInvalidDate` | Se rechazan fechas fuera de `YYYY-MM-DD` |
| `TestKeyBuilder_RejectsInvalidIDs` | Se rechazan IDs vacíos o con separadores |
| `TestKeyBuilder_RejectsInvalidEpoch` | Se rechazan tokens con formato inválido |
| `TestConfig_Validate` | Se rechazan prefijos, versiones y TTL inválidos |
| `TestRedisConfig_Validate` | Se rechazan direcciones y timeouts inválidos |
| `TestRedisStore_Connection` | `Ping` contra Redis real |
| `TestRedisStore_SetGetSetNX` | `Set`, `Get`, TTL, `ErrMiss` y semántica de `SetNX` |

Las pruebas `TestRedisStore_*` requieren Redis y tienen el build tag `integration`.

#### Lógica de caché, unitarias

| Prueba | Qué demuestra |
|---|---|
| `TestCache_FirstLoadThenHit` | Primera consulta sin caché y segunda con caché |
| `TestCache_InvalidateForcesReload` | Invalidar cambia el epoch y fuerza recálculo |
| `TestCache_InvalidateRejectsInvalidIDs` | La invalidación rechaza IDs vacíos o mal formados |
| `TestCache_InvalidationIsScopedToAgenda` | Invalidar una agenda no afecta a otras |
| `TestCache_InvalidationCoversAllDatesAndServices` | Una invalidación alcanza todas las fechas y servicios |
| `TestCache_ReadStartedBeforeInvalidation` | La lectura en vuelo no contamina el epoch nuevo |
| `TestCache_ConcurrentEpochInitialization` | `SETNX` crea el epoch una sola vez |
| `TestCache_ConcurrentWritersUnderSameEpochLastWriteWins` | Documenta la semántica de última escritura |
| `TestCache_EpochLossReinitializes` | La pérdida del epoch se recupera |
| `TestCache_CorruptEpochIsReplaced` | Un epoch corrupto se reemplaza |
| `TestCache_DataReadFailureDoesNotOverwriteEntry` | Un fallo de lectura no pisa la entrada existente |
| `TestCache_EpochInitializationFailureFallsBackToLoader` | Un fallo de `SETNX` no crea datos |
| `TestCache_RedisReadFailureFallsBackToLoader` | Un fallo de lectura usa el `Loader` |
| `TestCache_RedisWriteFailureReturnsLoaderResult` | Un fallo de escritura no impide responder |
| `TestCache_InvalidationFailureIsExplicit` | Un fallo de invalidación deja el epoch intacto y se informa |
| `TestCache_InvalidPayloadFromAnotherClinicIsRejected` | Un snapshot de otra clínica no se sirve |
| `TestCache_InvalidJSONIsRejected` | Un JSON inválido no se sirve |
| `TestCache_LoaderErrorsAreFatal` | Un error del `Loader` se propaga |
| `TestCache_LoaderSnapshotIsValidated` | Un snapshot inválido del `Loader` no se cachea |
| `TestCache_DoesNotMutateLoaderSnapshot` | La caché no modifica la memoria del `Loader` |
| `TestCache_DecodesNullSlotsAsEmptyList` | `espacios: null` se normaliza a lista vacía |
| `TestCache_RejectsInvalidRequest` | Un `Request` inválido no toca la caché |
| `TestCache_RejectsNilLoader` | Un `Loader` nulo se rechaza |
| `TestNewCache_Validation` | La construcción valida store y configuración |

#### Integración con Redis real

| Prueba | Qué demuestra |
|---|---|
| `TestCacheIntegration_FirstLoadThenHit` | Flujo completo y TTL de datos/epoch |
| `TestCacheIntegration_InvalidateAgendaRecalculates` | Invalidación real y recálculo |
| `TestCacheIntegration_ConcurrentEpochInitialization` | Concurrencia real con una sola clave de epoch |
| `TestCacheIntegration_EpochExpirationReinitializes` | Borrado/expiración del epoch |
| `TestCacheIntegration_ReadStartedBeforeInvalidation` | Carrera lectura/invalidación |
| `TestCacheIntegration_EpochExpiresDuringLoad` | Pérdida del epoch durante una carga |
| `TestCacheIntegration_InvalidPayloadFromAnotherClinicIsRejected` | Aislamiento y reparación del payload |
| `TestCacheIntegration_WriteFailureDoesNotCorruptState` | Fallo de escritura e invalidación sobre Redis real |
| `TestCacheIntegration_ReadFailureFallsBackToLoader` | Redis inaccesible con fallback al `Loader` |

#### Integración RF7 con Redis, PostgreSQL y API real

| Prueba | Qué demuestra |
|---|---|
| `TestIntegrationRF7Cache_MissThenHit` | Primera consulta calcula desde PostgreSQL; segunda devuelve HIT |
| `TestIntegrationRF7Cache_AgendaChangeInvalidatesHit` | Un bloqueo incrementa `AgendaVersion` y fuerza recálculo |
| `TestIntegrationRF7Cache_CitaReservadaInvalidaSnapshot` | Una cita reservada invalida el snapshot cacheado vía `AgendaVersion` |
| `TestIntegrationRF7Cache_ClinicIsolation` | Cambios en una clínica no invalidan el HIT de otra |
| `TestIntegrationRF7Cache_FallbackWhenRedisUnavailable` | Si Redis falla, RF7 sigue sirviendo desde PostgreSQL |
| `TestIntegrationRF7Cache_ConcurrentRequests` | 10 consultas concurrentes responden correctamente |

En total hay **~50 funciones de prueba** en el área de caché/disponibilidad, de las cuales **17 son de integración**. La última verificación ejecutada fue el benchmark `BenchmarkRF7_*`; las pruebas unitarias y de integración previas quedaron aprobadas en etapas anteriores.

El detector de carreras encontró, durante el desarrollo, una mutación del slice devuelto por el `Loader`; se corrigió copiando la lista antes de normalizarla. Esa corrección está cubierta por `TestCache_DoesNotMutateLoaderSnapshot`.

### Lo que todavía no prueban

- Cancelar/reprogramar por API (RF10–RF11) y su invalidación asociada.
- El manejo HTTP de `ErrCacheInvalidation` con una política explícita (reintento, métrica o estado de advertencia).
- Métricas de tasa de aciertos en producción bajo carga real; el benchmark muestra acierto controlado en laboratorio.

Cubierto por RF9: `TestIntegrationRF9I8ConsultarReservarConsultar` (E2E por API) y `TestIntegrationRF9CarreraUnaGanadora` (una sola ganadora).

---

## 9. Integración con la API

### `Loader` de RF7

El `Loader` ya está implementado en [`api/availability_loader.go`](../api/availability_loader.go). Calcula, para una clínica, profesional, servicio y fecha:

1. el horario de atención activo del profesional para ese día;
2. los bloqueos que se superponen;
3. las citas vigentes del profesional;
4. la duración del servicio;
5. los espacios libres resultantes, en la zona horaria de la clínica.

Reutiliza el mismo cálculo que el handler RF7, por lo que no puede divergir de la fuente de verdad.

### Cableado actual

- [`api/availability_cache.go`](../api/availability_cache.go) construye `RedisStore` desde `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD` y `REDIS_DB`.
- Si el `Ping` a Redis falla al arrancar, la API se levanta sin caché y calcula desde PostgreSQL (degradación).
- [`api/availability.go`](../api/availability.go) consulta `availabilityCache.GetOrLoad` cuando la caché está disponible; de lo contrario usa `loaderDisponibilidad.Load` directamente.
- El handler responde HTTP 503 cuando `GetOrLoad` detecta un snapshot potencialmente inconsistente (`ErrStaleSnapshot`).
- No se cachean datos de pacientes ni notas clínicas.

### Puntos de invalidación

| Operación | Cuándo invalidar | Alcance |
|---|---|---|
| Reservar (RF9) | Después del COMMIT | `(clínica, profesional)` de la cita |
| Cancelar (RF11) | Después del COMMIT | `(clínica, profesional)` de la cita |
| Reprogramar (RF10) | Después del COMMIT | Profesional de origen y de destino, si difieren |
| Cambiar horario o bloqueo (RF5/RF6) | Después del COMMIT | Profesional afectado |
| Cambiar duración de un servicio (RF3) | Después del COMMIT | Todos los profesionales que ofrecen el servicio |
| Cambiar profesionales de un servicio (RF4) | Después del COMMIT | Profesional afectado |

La invalidación debe ocurrir **después** del COMMIT. Si se publica antes, un lector puede calcular con datos previos y cachearlos bajo el epoch nuevo.

### Secuencia sugerida para una reserva

```text
BEGIN
  verificar disponibilidad y solapamientos en PostgreSQL
  insertar la cita
COMMIT

InvalidateAgenda(clínica, profesional)
  ├─ éxito  → responder 201
  └─ fallo  → aplicar la política acordada (ver sección 11)
```

La verificación definitiva de la reserva siempre es la transacción de PostgreSQL. La caché no autoriza reservas.

### RF8: rangos de fechas

La clave actual es por fecha. Una consulta de rango puede resolverse llamando a `GetOrLoad` por cada fecha o agregando una capa de caché de rango en el futuro. No se implementó una clave de rango para no duplicar la política de invalidación.

---

## 10. Requisitos del proyecto

| Requisito | Qué cubre el paquete | Qué falta | Evidencia actual |
|---|---|---|---|
| **RF7** | Cálculo de disponibilidad con índices; caché conectada al endpoint; fallback a PostgreSQL | Optimizar el cálculo si escala más allá del dataset de prueba | Pruebas unitarias, de integración y benchmark |
| **I5** | La clínica forma parte de la clave; se rechazan snapshots de otra clínica; token deriva el tenant | — | Pruebas de claves, payload y aislamiento entre clínicas |
| **I7** | Los triggers incrementan `AgendaVersion`; `InvalidateAgenda` publica epoch nuevo | Llamar a `InvalidateAgenda` desde cancelar/reprogramar (RF10–RF11) cuando existan | Pruebas de invalidación, RF9 y cambio de agenda |
| **I8** | `AgendaVersion` valida cada HIT; reserva por API invalida y el espacio desaparece | Política completa ante COMMIT exitoso + invalidación fallida (reintentos/métricas) | `TestIntegrationRF7Cache_CitaReservadaInvalidaSnapshot`, `TestIntegrationRF9I8ConsultarReservarConsultar` |

E2E “consultar → reservar → consultar” por API: `TestIntegrationRF9I8ConsultarReservarConsultar`. La variante por SQL (`TestIntegrationRF7Cache_CitaReservadaInvalidaSnapshot`) sigue como prueba de `AgendaVersion`.

---

## 11. I8: COMMIT exitoso con invalidación de epoch fallida

### El escenario

```text
1. La API consulta disponibilidad: el horario 10:00 aparece libre.
2. Un recepcionista reserva el horario 10:00.
3. PostgreSQL confirma la reserva (COMMIT exitoso); AgendaVersion incrementa.
4. La API llama a InvalidateAgenda.
5. Redis no responde o el SET del epoch falla.
6. El epoch anterior sigue vigente.
7. Otra consulta lee el snapshot viejo, pero su AgendaVersion ya no coincide.
8. GetOrLoad descarta el snapshot y recalcula desde PostgreSQL.
```

### Mitigación actual

La tabla `AgendaVersion` y el `Snapshot` v3 reducen drásticamente la ventana de inconsistencia:

- Los triggers de `Cita`, `Bloqueo` y `HorarioAtencion` incrementan `AgendaVersion` dentro de la misma transacción que la mutación.
- Un cache hit obliga a comparar la versión del snapshot con la versión autoritativa.
- Si el epoch no se publicó, el snapshot se descarta y se recalcula.
- El TTL sigue siendo una red de seguridad residual para snapshots que, por alguna razón, no lleven `AgendaVersion` (schema v2 o menor) o para ventanas en las que el resolver también falle.

### Limitaciones que quedan

- **El resolver puede fallar.** Si PostgreSQL no responde al consultar `AgendaVersion` durante un HIT, `GetOrLoad` descarta el snapshot y recalcula. Esto es seguro, pero anula el beneficio de rendimiento de la caché.
- **El snapshot obsoleto puede reescribirse.** Un lector lento que obtuvo el epoch viejo antes de la reserva podría escribir bajo ese epoch y reiniciar el TTL. `AgendaVersion` evita servirlo, pero ocupa memoria hasta expirar.
- **No hay reintentos automáticos de `InvalidateAgenda`.** El handler de reserva recibe `ErrCacheInvalidation` y debe decidir; hoy no hay política definida.
- **No se midió la ventana real bajo fallo.** El benchmark mide el camino feliz; no se inyectó una caída de Redis durante `InvalidateAgenda`.

### Decisiones operativas pendientes

1. Invalidar **después del COMMIT** y antes de responder éxito.
2. Reintentar un número acotado de veces ante errores transitorios de Redis.
3. Si aun así falla, **no ignorarlo**: registrar el evento, exponer una métrica y definir si la API responde error o un estado explícito.
4. Mantener el TTL como red de seguridad residual, nunca como garantía.
5. ~~Completar la prueba E2E “consultar → reservar → consultar” por endpoint~~ — hecho en RF9 (`TestIntegrationRF9I8ConsultarReservarConsultar`). Pendiente equivalente al cancelar/reprogramar.

Si se requiere I8 también bajo fallos simultáneos de Redis y del resolver de `AgendaVersion`, habría que evaluar un **outbox transaccional** o un **bypass global de caché** hasta recuperar Redis. Eso queda fuera del alcance actual.

---

## 12. Benchmark comparativo

### Metodología

El benchmark [`api/availability_rf7_bench_test.go`](../api/availability_rf7_bench_test.go) compara dos rutas bajo las mismas condiciones:

- **DirectPG:** `loaderDisponibilidad.Load` ejecuta las consultas de RF7 en PostgreSQL y luego se consulta `AgendaVersion`.
- **RedisHit:** `availabilityCache.GetOrLoad` lee el snapshot de Redis y valida la versión contra `AgendaVersion` en PostgreSQL.

Ambos usan los mismos fixtures: una clínica, un profesional, un servicio de 30 minutos, un horario de 09:00 a 13:00 y una fecha futura. Antes de medir RedisHit se precalienta la caché con un MISS + HIT para garantizar que el benchmark mida lecturas de caché válidas.

Se ejecutó con `-count=5` en un contenedor `golang:1.25` contra los contenedores `medicitas-pg-stage5b` y `medicitas-redis-it` del entorno de desarrollo.

### Resultados

```text
BenchmarkRF7_DirectPG-12     1059    1092725 ns/op    11247 B/op    218 allocs/op
BenchmarkRF7_DirectPG-12     1021    1090046 ns/op    11247 B/op    218 allocs/op
BenchmarkRF7_DirectPG-12      980    1095800 ns/op    11248 B/op    218 allocs/op
BenchmarkRF7_DirectPG-12      993    1086482 ns/op    11248 B/op    218 allocs/op
BenchmarkRF7_DirectPG-12     1110    1188632 ns/op    11249 B/op    218 allocs/op

BenchmarkRF7_RedisHit-12     3829     331282 ns/op     5771 B/op     74 allocs/op
BenchmarkRF7_RedisHit-12     3852     326975 ns/op     5770 B/op     74 allocs/op
BenchmarkRF7_RedisHit-12     4348     354577 ns/op     5770 B/op     74 allocs/op
BenchmarkRF7_RedisHit-12     3670     313327 ns/op     5770 B/op     74 allocs/op
BenchmarkRF7_RedisHit-12     3772     309720 ns/op     5773 B/op     74 allocs/op
```

| Métrica | DirectPG | RedisHit | Diferencia |
|---|---|---|---|
| Latencia mediana | ~1.09 ms | ~0.33 ms | RedisHit ≈ 3.3× más rápido |
| Memoria por operación | ~11.2 KiB | ~5.7 KiB | RedisHit ≈ 49 % menos |
| Asignaciones por operación | 218 | 74 | RedisHit ≈ 66 % menos |

### Interpretación y limitaciones

- La mejora se debe a evitar las tres consultas SQL de RF7 (horarios, bloqueos, citas) y los joins a `Clinica`.
- La validación de `AgendaVersion` añade una consulta SQL incluso en HIT; sin ella RedisHit sería aún más rápido, pero se perdería la segunda barrera de consistencia.
- Las mediciones son de laboratorio con una base casi vacía excepto por los fixtures del benchmark. En producción, con más carga en PostgreSQL y Redis, la diferencia relativa puede ser mayor o menor.
- No se midió MISS, invalidación concurrente ni degradación bajo Redis caído.
- El entorno de contenedores comparte CPU con el host; los tiempos absolutos varían según la máquina.

## 13. Referencias

- Enunciado: [`spec.md`](../spec.md) — RF7, RF8, I7, I8 y criterios de aceptación.
- Código: [`api/internal/availabilitycache/`](../api/internal/availabilitycache/).
- Loader RF7: [`api/availability_loader.go`](../api/availability_loader.go).
- Integración API: [`api/availability.go`](../api/availability.go), [`api/availability_cache.go`](../api/availability_cache.go).
- Resolver `AgendaVersion`: [`api/internal/availabilityversion/postgres.go`](../api/internal/availabilityversion/postgres.go).
- Pruebas de integración: [`api/availability_rf7_cache_integration_test.go`](../api/availability_rf7_cache_integration_test.go).
- Benchmark: [`api/availability_rf7_bench_test.go`](../api/availability_rf7_bench_test.go).
- Modelo de datos: [`docs/modelo-datos.md`](modelo-datos.md).
- Multi-tenancy: [`docs/multi-tenancy.md`](multi-tenancy.md).
- Esquema SQL: [`db/migrations/01_schema.sql`](../db/migrations/01_schema.sql).
