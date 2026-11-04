# Caché de disponibilidad en Redis

Documento de diseño del módulo Redis de MediCitas (RF7, I7, I8).

**Estado:** el paquete [`api/internal/availabilitycache`](../api/internal/availabilitycache/) está implementado y probado de forma aislada y contra Redis real. **Todavía no está conectado a la API, a RF7 ni a PostgreSQL.** La fuente de verdad sigue siendo PostgreSQL; Redis solo guarda copias derivadas de la disponibilidad.

Este documento describe lo que existe, lo que se probó y lo que queda pendiente de integrar. No afirma que I8 esté demostrado a nivel de sistema: esa prueba requiere la API y la base de datos reales.

---

## 1. Arquitectura

```text
Cliente HTTP
    │
    ▼
API / handler de disponibilidad (futuro, RF7/RF8)
    │
    ├── load(ctx, Request) ──────────────► PostgreSQL (fuente de verdad)
    │                                           ▲
    │                                           │ RF7: horarios, bloqueos,
    │                                           │ duración y citas vigentes
    ▼                                           │
availabilitycache.Cache ──── Store ──── RedisStore ──── Redis
    │
    └── InvalidateAgenda(ctx, clinicaID, profesionalID)
```

El paquete no conoce HTTP, PostgreSQL ni Keycloak. Recibe un `LoaderFunc` que calcula la disponibilidad y un `Store` que abstrae Redis. Esa separación permite:

- probar la política de caché con un `Store` falso, sin levantar Redis;
- cambiar la implementación de Redis sin tocar la lógica;
- conectar RF7 más adelante sin acoplar el cálculo de disponibilidad a la caché.

### Componentes

| Componente | Archivo | Responsabilidad |
|---|---|---|
| `Cache` | [`cache.go`](../api/internal/availabilitycache/cache.go) | `GetOrLoad` e `InvalidateAgenda` |
| Configuración | [`config.go`](../api/internal/availabilitycache/config.go) | Validación de `Config` |
| `keyBuilder` | [`keys.go`](../api/internal/availabilitycache/keys.go) | Construir y validar claves |
| `RedisStore` | [`redis.go`](../api/internal/availabilitycache/redis.go) | `Get`, `Set`, `SetNX`, `Ping`, `Close` sobre go-redis |
| Epochs | [`epoch.go`](../api/internal/availabilitycache/epoch.go) | Tokens aleatorios, lectura e inicialización concurrente |
| Códec | [`codec.go`](../api/internal/availabilitycache/codec.go) | Serializar, normalizar y validar `Snapshot` |
| Contratos | [`interfaces.go`](../api/internal/availabilitycache/interfaces.go), [`types.go`](../api/internal/availabilitycache/types.go) | `Store`, `LoaderFunc`, `Request`, `Snapshot`, `Result`, `Config` |
| Errores | [`errors.go`](../api/internal/availabilitycache/errors.go) | Errores centinela para distinguir fallos |

### Flujo de lectura

1. La API valida la petición y construye un `Request` con la clínica tomada del token, nunca de un parámetro libre.
2. `GetOrLoad` obtiene el epoch vigente de `(clínica, profesional)`.
3. Construye la clave de datos con ese epoch.
4. Intenta leer Redis.
5. Si hay un `Snapshot` válido, responde con `Hit: true`.
6. Si no hay entrada, el epoch falla, Redis falla o el valor es inválido, recurre al `Loader`.
7. Cuando corresponde, guarda el resultado bajo el epoch leído.

### Flujo de escritura (futuro, al reservar)

1. La API ejecuta la reserva en PostgreSQL dentro de una transacción.
2. Si el COMMIT es exitoso, llama a `InvalidateAgenda(ctx, clínica, profesional)`.
3. La próxima consulta obtiene un epoch nuevo y recalcula la agenda.
4. Si la invalidación falla, la API debe tratarlo explícitamente. Ese caso se analiza en la sección 11.

---

## 2. Claves

### Estructura

```text
{prefix}:avail:{format}:epoch:{clinicaId}:{profesionalId}
{prefix}:avail:{format}:data:{clinicaId}:{profesionalId}:{servicioId}:{fecha}:{epoch}
```

Con la configuración por defecto:

```text
medicitas:avail:v1:epoch:clinica-norte:prof-0001
medicitas:avail:v1:data:clinica-norte:prof-0001:serv-001:2026-11-10:3f2a9c...e1
```

### Por qué cada segmento

| Segmento | Motivo |
|---|---|
| `medicitas` | Namespace del proyecto dentro de una instancia Redis compartida |
| `avail` | Separar la caché de disponibilidad de otros usos futuros |
| `v1` | Versión del formato de claves; permite convivencia durante una migración |
| `clinicaId` | Aislamiento por tenant (I5). La clínica viene del contexto autenticado |
| `profesionalId` | La agenda y el epoch se manejan por profesional |
| `servicioId` | La duración del servicio determina los espacios disponibles (RF7) |
| `fecha` | Fecha local de la clínica, en formato estricto `YYYY-MM-DD` |
| `epoch` | Barrera de invalidación; una entrada vieja queda inalcanzable |

El alcance de una entrada es **una combinación de clínica, profesional, servicio y fecha, bajo un epoch**. Una reserva publica un epoch nuevo para todo el profesional, por lo que invalida **todas sus fechas y servicios**. Es una invalidación conservadora: puede reducir la tasa de aciertos, pero evita tener que enumerar claves o mantener un índice de entradas.

### Validación

Antes de construir una clave se valida:

- IDs con el patrón `[A-Za-z0-9_-]{1,128}`; se rechazan vacíos, espacios, `:` y caracteres fuera del patrón;
- fecha en formato `YYYY-MM-DD`, sin variantes como `2026-1-1`;
- epoch de exactamente 32 caracteres hexadecimales en minúscula;
- prefijo y versión de formato con caracteres seguros.

Estas reglas evitan colisiones por concatenación, claves ambiguas y valores inesperados en Redis.

### Versionado

Hay dos versiones independientes:

- la versión de **claves** (`v1` en el prefijo) permite cambiar la estructura de claves sin leer datos incompatibles;
- `SnapshotSchemaVersion` (`1` en el valor) permite cambiar la estructura del JSON. Un valor con otra versión se considera inválido, se recalcula y se reemplaza.

---

## 3. Invalidación mediante epochs

### Mecanismo

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
- que el `SET` no falle;
- que un lector que ya obtuvo el epoch viejo no escriba bajo ese epoch (la escritura queda inalcanzable, pero ocupa memoria hasta el TTL);
- que PostgreSQL y Redis estén coordinados sin una política de integración.

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
5. Si el valor existe y es válido, devuelve `Hit: true`.
6. Si el valor existe pero es inválido, recalcula, intenta reemplazarlo y devuelve `CacheErr` con `ErrInvalidPayload`.
7. Si no existe, recalcula, guarda y devuelve `Hit: false`.
8. Si Redis falla, recalcula y devuelve `Hit: false` con `ErrCacheRead` o `ErrCacheWrite` en `CacheErr`.

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
| `SchemaVersion` | Versión del JSON |
| `ClinicaID` | Validar tenant |
| `ProfesionalID` | Validar agenda |
| `ServicioID` | Validar servicio |
| `Fecha` | Validar fecha |
| `ZonaHoraria` | Zona de la clínica usada para calcular |
| `DuracionMinutos` | Duración del servicio |
| `Espacios` | Lista de intervalos disponibles |
| `GeneradoEn` | Momento de generación; trazabilidad y diagnóstico |

---

## 5. TTL y configuración

### `Config`

| Campo | Valor inicial | Motivo | ¿Se puede cambiar? |
|---|---|---|---|
| `Prefix` | `medicitas` | Namespace | Sí, por despliegue |
| `FormatVersion` | `v1` | Versión de claves | Solo con migración coordinada |
| `DataTTL` | `5m` | Acota datos huérfanos y limita la ventana si una invalidación falla | Sí; afecta frescura y tasa de aciertos |
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

La configuración todavía no se lee de variables de entorno. El `docker-compose.yml` ya define `REDIS_HOST` y `REDIS_PORT` para la API, pero el cableado corresponde a la integración futura.

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

Puntos importantes:

- **Las lecturas son fail-open hacia PostgreSQL.** Si Redis no responde, la consulta sigue siendo correcta, solo más lenta.
- **La invalidación es fail-explicit.** No se oculta: si no se pudo publicar el epoch, el llamador recibe `ErrCacheInvalidation` y debe decidir.
- **Un valor inválido nunca se sirve.** Se recalcula y se reemplaza.
- **Un fallo del `Loader` no se tapa con datos viejos.** La caché no devuelve una entrada anterior si el cálculo actual falla; el error se propaga.
- Los errores se pueden inspeccionar con `errors.Is` (`ErrCacheRead`, `ErrCacheWrite`, `ErrCacheInvalidation`, `ErrInvalidPayload`).

No hay circuit breaker ni métricas integradas. `CacheErr` y `ErrCacheInvalidation` existen para que la API los registre y decida.

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

Pruebas de integración (requieren Redis accesible en DB 15):

```bash
docker compose up -d redis          # desde la raíz del repositorio
cd api
REDIS_ADDR=localhost:6379 REDIS_DB=15 \
  go test -tags=integration ./internal/availabilitycache
```

En PowerShell:

```powershell
docker compose up -d redis
cd api
$env:REDIS_ADDR = "localhost:6379"
$env:REDIS_DB = "15"
go test -tags=integration ./internal/availabilitycache
```

Con detector de carreras:

```bash
go test -race -tags=integration ./internal/availabilitycache
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

En total hay **43 funciones de prueba**, de las cuales **11 son de integración**. La última verificación incluyó:

```text
go test ./...                                             → ok
go test -tags=integration -count=5 ./internal/...          → ok
go test -race -tags=integration -count=1 ./internal/...    → ok
gofmt -l internal/availabilitycache                         → sin salida
go vet ./...                                                → sin observaciones
```

El detector de carreras encontró, durante el desarrollo, una mutación del slice devuelto por el `Loader`; se corrigió copiando la lista antes de normalizarla. Esa corrección está cubierta por `TestCache_DoesNotMutateLoaderSnapshot`.

### Lo que todavía no prueban

- La prueba E2E “consultar → reservar → consultar” contra la API y PostgreSQL.
- La reserva concurrente real con una sola ganadora (corresponde al módulo de transacciones).
- El comportamiento de RF7 con horarios, bloqueos y citas reales.
- La latencia y la tasa de aciertos antes/después (medición de la Etapa 2).
- El manejo HTTP de `ErrCacheInvalidation`.

---

## 9. Integración futura

### Implementar el `Loader` de RF7

El `Loader` debe calcular, para una clínica, profesional, servicio y fecha:

1. el horario de atención activo del profesional para ese día;
2. los bloqueos que se superponen;
3. las citas vigentes del profesional;
4. la duración del servicio;
5. los espacios libres resultantes, en la zona horaria de la clínica.

El cálculo debe leer de PostgreSQL y devolver un `Snapshot` completo. Conviene que la consulta use los índices de agenda y que la fecha se interprete en la zona horaria de la clínica.

### Cablear el paquete en la API

- Construir `RedisStore` con `REDIS_HOST`, `REDIS_PORT` y, si corresponde, password.
- Construir `Cache` con `DefaultConfig()` o una configuración derivada.
- Decidir si un `Ping` fallido al arrancar impide levantar la API o solo deshabilita la caché. Para el curso, degradar es más defendible que impedir el arranque.
- Registrar `CacheErr` y `ErrCacheInvalidation` en logs/métricas.
- No cachear datos de pacientes ni notas clínicas.

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
| **RF7** | Envuelve el cálculo con una política de caché; define el contrato del `Loader`; valida snapshots | Implementar el cálculo SQL, la zona horaria y los índices; exponer el endpoint | Pruebas de la caché, no del cálculo |
| **I5** | La clínica forma parte de la clave; se rechazan snapshots de otra clínica | Derivar la clínica del token y probar fugas a nivel API | Pruebas de claves y payload |
| **I7** | Permite liberar el espacio de inmediato publicando un epoch nuevo | Llamar a `InvalidateAgenda` después del COMMIT de cancelar/reprogramar; prueba E2E | Pruebas de invalidación |
| **I8** | Con invalidación exitosa, las lecturas posteriores no ven datos previos; las escrituras viejas quedan inalcanzables | Política ante COMMIT exitoso + invalidación fallida; prueba integrada “consultar → reservar → consultar” | Pruebas de época y concurrencia, no la prueba del sistema |

La consigna exige una prueba automatizada que reserve y vuelva a consultar. Esa prueba **todavía no existe** porque requiere RF7, el endpoint de reserva y PostgreSQL. El paquete aporta el mecanismo, no la demostración completa del invariante.

Tampoco se midió todavía la diferencia de latencia antes y después de la caché; corresponde a la comparación sencilla que pide la consigna.

---

## 11. I8: COMMIT exitoso con invalidación fallida

### El escenario

```text
1. La API consulta disponibilidad: el horario 10:00 aparece libre.
2. Un recepcionista reserva el horario 10:00.
3. PostgreSQL confirma la reserva (COMMIT exitoso).
4. La API llama a InvalidateAgenda.
5. Redis no responde o el SET falla.
6. El epoch anterior sigue vigente.
7. Otra consulta lee la entrada vieja y vuelve a mostrar el horario como libre.
8. El error se descubre recién cuando expira el TTL de datos.
```

Este caso **no está resuelto** por el diseño actual. Ni los epochs, ni los reintentos, ni el TTL lo resuelven por sí solos:

- **Los epochs** solo funcionan si el `SET` se publica. Si falla, no hay barrera nueva.
- **Los reintentos** reducen la probabilidad de un fallo transitorio, pero no garantizan la publicación. Un Redis caído, una partición de red o un timeout pueden agotar los intentos.
- **El TTL** acota la ventana, pero no la elimina. Durante ese tiempo la disponibilidad puede contradecir a la base, que es exactamente lo que I8 prohíbe. Además, una lectura lenta podría volver a escribir datos viejos y reiniciar la ventana.

La consecuencia práctica es una ventana en la que la API puede mostrar un horario ya reservado. La restricción de unicidad y la transacción de PostgreSQL impiden la doble reserva, pero la respuesta de disponibilidad sigue siendo incorrecta.

### Alternativas consideradas

| Alternativa | Ventajas | Costos | ¿Resuelve el fallo? |
|---|---|---|---|
| **Invalidación síncrona post-COMMIT + reintentos + TTL** | Simple; no cambia el esquema; usa lo ya implementado; cubre el camino normal | Ventana residual si el `SET` falla; requiere política de error y observabilidad | **No por completo** |
| **Outbox transaccional en PostgreSQL** | El evento de invalidación se guarda en la misma transacción que la reserva; un worker lo publica con reintentos; sobrevive caídas | Requiere tabla, worker y operación adicional; la publicación es eventual, no instantánea | Sí, de forma eventual; no garantiza inmediatez estricta |
| **Versión de agenda en PostgreSQL** | La lectura compara la versión del snapshot con la versión confirmada y descarta datos viejos; no depende de que Redis escriba | Requiere columna/versión, cambios de consulta y un campo en el snapshot; costo extra por lectura | Sí, con cambios de esquema |
| **Escritura directa del snapshot nuevo después del COMMIT** | Evita el primer fallo de caché y puede mejorar la latencia | Si la escritura falla, el problema persiste; hay que recalcular disponibilidad tras reservar | No por sí sola |
| **Sin caché para disponibilidad** | I8 se cumple trivialmente | Se pierde el objetivo de rendimiento y el módulo de la consigna | Sí, pero no es aceptable para el proyecto |
| **Bypass local de la caché tras un fallo** | Fácil de añadir; la instancia que detectó el fallo deja de servir caché | No coordina otras instancias; no es una garantía global | Solo parcialmente |

### Decisión pendiente con el equipo

Para el alcance del curso, la opción más razonable es:

1. invalidar **después del COMMIT** y antes de responder éxito;
2. reintentar un número acotado de veces ante errores transitorios;
3. si aun así falla, **no ignorarlo**: registrar el evento, exponer una métrica y definir con el equipo si la API responde error o un estado explícito de “reserva confirmada, disponibilidad pendiente”;
4. mantener el TTL como red de seguridad, nunca como garantía;
5. agregar la prueba E2E “consultar → reservar → consultar” cuando existan RF7 y el endpoint de reserva.

Si el profesor exige I8 también bajo fallos de Redis, hay que elegir entre **outbox transaccional** y **versión de agenda en PostgreSQL**. Ambas requieren cambios de esquema y deben coordinarse con el responsable de la base antes de implementarlas. Hasta entonces, **no se debe afirmar que I8 está completamente cumplido**.

---

## 12. Referencias

- Enunciado: [`spec.md`](../spec.md) — RF7, RF8, I7, I8 y criterios de aceptación.
- Código: [`api/internal/availabilitycache/`](../api/internal/availabilitycache/).
- Modelo de datos: [`docs/modelo-datos.md`](modelo-datos.md).
- Multi-tenancy: [`docs/multi-tenancy.md`](multi-tenancy.md).
- Esquema SQL: [`db/migrations/01_schema.sql`](../db/migrations/01_schema.sql).
