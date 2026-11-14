# Modelo de datos — MediCitas

Documento de entrega del modelo relacional. La fuente de verdad es
[`db/migrations/01_schema.sql`](../db/migrations/01_schema.sql); el diagrama
completo está en [`docs/esquema-er.md`](esquema-er.md).

Convenciones del esquema:

- Tablas en **PascalCase** y columnas en **camelCase**. En PostgreSQL los
  identificadores mixtos deben ir entre comillas dobles.
- Las tablas de negocio viven en `public`; los objetos técnicos en `ops` y las
  funciones auxiliares en `app`.
- Todas las tablas de negocio llevan `clinicaId` y usan FKs compuestas por
  clínica para impedir referencias entre tenants.
- El aislamiento de tenant se refuerza con RLS ([`db/migrations/03_rls.sql`](../db/migrations/03_rls.sql), [`docs/rls.md`](rls.md)). La columna `clinicaId` sigue siendo la estrategia principal.

## Entidades

| Entidad | Responsabilidad | Notas |
|---------|-----------------|-------|
| **Clinica** | Tenant | `identificador` único, nombre y `zonaHoraria` |
| **Rol** | Catálogo de roles de dominio | `adm_plataforma`, `adm_clinica`, `recepcion`, `profesional` |
| **Usuario** | Persona del sistema | `idpSub` de Keycloak, `clinicaId` (NULL solo para `adm_plataforma`), un solo `rolId`, baja lógica |
| **Especialidad** | Catálogo global | Código único y nombre |
| **Servicio** | Tipo de consulta | Pertenece a una clínica; duración y especialidad |
| **Profesional** | Quién atiende | **Siempre** enlazado a un `Usuario` con rol `profesional`; una especialidad |
| **ProfesionalServicio** | N:M | Qué servicios puede atender un profesional; el servicio debe ser de su especialidad |
| **HorarioAtencion** | Bloques semanales | Varios por día (jornada partida); sin solapes activos por profesional, día y vigencia |
| **Bloqueo** | Vacaciones / permisos | Rango `inicio`–`fin`; impide crear citas dentro del bloqueo |
| **Paciente** | Atendido por recepción | Identificación única por clínica |
| **ContactoPaciente** | Contactos 1:N | Teléfono, correo o dirección; puede haber varios y uno principal por tipo |
| **Cita** | Espacio reservado | Paciente + profesional + servicio + intervalo; máquina de estados |
| **NotaClinica** | Dato sensible | 1:1 con cita atendida; solo el profesional que atendió |
| **Auditoria** | Bitácora append-only | Quién, qué, paciente y cuándo; sin FKs para conservar el rastro |
| **AgendaVersion** | Versión de agenda | Una por `(clínica, profesional)`; invalida la caché de disponibilidad |
| **ops.LogEvento** | Log técnico | Sin datos clínicos ni de contacto |
| **ops.JobEjecucion** | Ejecución de jobs | Estado, inicio, fin y error |
| **ops.Migracion** | Control de migraciones | Versión, checksum y resultado |
| **ops.Semilla** | Control de la semilla | Versión aplicada; evita duplicar datos demo |

## Cita: estados, cancelación y reprogramación

```
reservada → atendida | ausente | cancelada | reprogramada
```

- Los cuatro estados finales no admiten salida.
- Una cita nueva **siempre nace `reservada`**; el trigger `CitaPreparar` rechaza cualquier otro estado en INSERT.
- La cancelación se guarda en la misma fila: `canceladaEn`,
  `canceladaPorUsuarioId` y `motivoCancelacion`.
- La reprogramación no cambia la fila original. La cita original pasa a
  `reprogramada` y se crea otra cita con `citaOrigenId` apuntando a ella. La
  función `app."reprogramarCita"` hace ambos pasos en una sola transacción.
- La duración la fija el **servicio** y se copia a `Cita.duracionMinutos`; `fin`
  se calcula al insertar. Es una instantánea deliberada para conservar el
  histórico si el servicio cambia de duración.
- Una cita nueva debe caer dentro de un `HorarioAtencion` activo y fuera de
  cualquier `Bloqueo` del profesional.

## Normalización (1FN, 2FN, 3FN)

La justificación se contrasta con el esquema real de
[`db/migrations/01_schema.sql`](../db/migrations/01_schema.sql).

### 1FN: atributos atómicos y grupos repetitivos en filas

Una tabla está en 1FN cuando cada columna contiene un valor atómico y no hay
grupos repetitivos.

- **Horarios semanales:** en lugar de una columna por día o un arreglo de
  bloques, `HorarioAtencion` tiene una fila por bloque (`diaSemana`,
  `horaInicio`, `horaFin`, `vigenteDesde`, `vigenteHasta`). RF5 admite jornada
  partida añadiendo más filas.
- **Contactos del paciente:** no hay columnas `telefono`, `email` ni `direccion`
  en `Paciente`; viven en `ContactoPaciente` con `tipoContactoId` (FK a
  `TipoContacto`) y `valor`.
- **Servicios de un profesional:** la relación N:M se modela en la tabla
  `ProfesionalServicio`, no como columnas múltiples en `Profesional`.
- **Instantes de cita:** `Cita.inicio` y `Cita.fin` son un solo valor cada uno
  (`timestamptz`); la duración se deriva o se copia desde `Servicio`, pero no se
  almacena como lista de intervalos.

No hay atributos multivaluados ni compuestos que justifiquen una desnormalización.

### 2FN: dependencias funcionales de la clave completa

Una tabla está en 2FN cuando, además de 1FN, toda columna no clave depende de
toda la clave (relevante para claves compuestas).

- **N:M `ProfesionalServicio`:** la clave primaria es compuesta
  `(clinicaId, profesionalId, servicioId)`. No hay columnas no clave que
  dependan solo de una parte de ella; `creadoEn` depende de la fila completa.
- **`Cita`:** la duración y el fin de la cita dependen del servicio, pero se
  copian a `Cita` como instantánea histórica. La dependencia funcional de
  `duracionMinutos` respecto a `servicioId` se reconoce; la copia en `Cita` es
  una desnormalización deliberada y documentada para preservar el histórico.
- **`Usuario`:** `rolId` depende de toda la PK (`id`), no de una parte.
- **`NotaClinica`:** `contenido` depende de `id` y la relación con `citaId` es
  1:1 mediante `UNIQUE (clinicaId, citaId)`.

No se detectan dependencias parciales no resueltas.

### 3FN: ausencia de dependencias transitivas

Una tabla está en 3FN cuando, además de 2FN, no hay dependencias transitivas de
la clave primaria.

- **Especialidad del profesional:** `Profesional.especialidadId` es una FK a
  `Especialidad`. No se repiten datos de `Especialidad` en `Profesional`.
- **Servicio y especialidad:** `Servicio.especialidadId` es una FK a
  `Especialidad`; el nombre o código de la especialidad no se duplican.
- **Cita y datos derivados:** `Cita` almacena `clinicaId`, `pacienteId`,
  `profesionalId` y `servicioId`. Propagar `clinicaId` a los hechos no es una
  dependencia transitiva: es el discriminador de tenant necesario para I5.
- **Datos de cancelación:** `canceladaEn`, `canceladaPorUsuarioId` y
  `motivoCancelacion` dependen directamente del hecho de cancelación de la cita,
  no de otra entidad.
- **Auditoría:** no tiene FKs a `Usuario` ni `Paciente` a propósito, para que el
  rastro no dependa de que esas tablas conserven sus filas.

### Desnormalizaciones deliberadas documentadas

| Desnormalización | Tabla/Campo | Motivo | Costo |
|------------------|-------------|--------|-------|
| Copia de duración del servicio | `Cita.duracionMinutos`, `Cita.fin` | Preservar el histórico si cambia la duración del servicio | Requiere trigger `CitaPreparar` para mantener coherencia en INSERT |
| Propagación del tenant | `clinicaId` en hechos (`Cita`, `NotaClinica`, `Auditoria`, …) | Aislamiento multi-tenant e I5 sin depender solo de joins largos | Cada tabla de negocio repite `clinicaId`; se controla con FKs compuestas y RLS |
| `Auditoria` sin FKs | Ninguna FK en `Auditoria` | Conservar rastro aunque se eliminen usuarios o pacientes | Integridad referencial relajada; compensada por IDs almacenados |

## Diccionario de datos

A continuación se listan las columnas principales del esquema. El tipo SQL se
muestra tal como aparece en [`01_schema.sql`](../db/migrations/01_schema.sql).

### `Rol`

| Columna | Tipo | Nulo | FK | Descripción | Invariante |
|---------|------|------|----|-------------|------------|
| `id` | uuid | NO | PK | Identificador interno | — |
| `codigo` | text | NO | UK | `adm_plataforma`, `adm_clinica`, `recepcion`, `profesional` | RF17 |
| `nombre` | text | NO | — | Nombre legible | — |
| `descripcion` | text | SÍ | — | Descripción del rol | — |
| `creadoEn` | timestamptz | NO | — | Timestamp de creación | — |
| `actualizadoEn` | timestamptz | NO | — | Timestamp de última actualización | — |

### `TipoContacto`

| Columna | Tipo | Nulo | FK | Descripción | Invariante |
|---------|------|------|----|-------------|------------|
| `id` | uuid | NO | PK | Identificador interno | — |
| `codigo` | text | NO | UK | `telefono`, `email`, `direccion` | RF14 |
| `nombre` | text | NO | — | Nombre legible | — |

### `Especialidad`

| Columna | Tipo | Nulo | FK | Descripción | Invariante |
|---------|------|------|----|-------------|------------|
| `id` | uuid | NO | PK | Identificador interno | — |
| `codigo` | text | NO | UK | Código único (p. ej. `medicina_general`) | RF3/RF4 |
| `nombre` | text | NO | — | Nombre legible | — |
| `activo` | boolean | NO | — | Baja lógica | — |
| `creadoEn` | timestamptz | NO | — | Timestamp de creación | — |
| `actualizadoEn` | timestamptz | NO | — | Timestamp de última actualización | — |

### `Clinica`

| Columna | Tipo | Nulo | FK | Descripción | Invariante |
|---------|------|------|----|-------------|------------|
| `id` | uuid | NO | PK | Identificador interno | — |
| `identificador` | text | NO | UK | Slug único; formato `[a-z0-9]+(-[a-z0-9]+)*` | RF1 |
| `nombre` | text | NO | — | Nombre de la clínica | RF1 |
| `zonaHoraria` | text | NO | — | Zona IANA (default `America/Costa_Rica`) | I3 |
| `activo` | boolean | NO | — | Baja lógica | — |
| `creadoEn` | timestamptz | NO | — | Timestamp de creación | — |
| `actualizadoEn` | timestamptz | NO | — | Timestamp de última actualización | — |

### `Usuario`

| Columna | Tipo | Nulo | FK | Descripción | Invariante |
|---------|------|------|----|-------------|------------|
| `id` | uuid | NO | PK | Identificador interno | — |
| `idpSub` | text | NO | UK | Subject del JWT de Keycloak | RF17/I5 |
| `clinicaId` | uuid | SÍ | `Clinica` | NULL solo para `adm_plataforma` | I5 |
| `rolId` | uuid | NO | `Rol` | Rol del usuario | RF17 |
| `activo` | boolean | NO | — | Baja lógica | RF2 |
| `creadoEn` | timestamptz | NO | — | Timestamp de creación | — |
| `actualizadoEn` | timestamptz | NO | — | Timestamp de última actualización | — |

### `Profesional`

| Columna | Tipo | Nulo | FK | Descripción | Invariante |
|---------|------|------|----|-------------|------------|
| `id` | uuid | NO | PK | Identificador interno | — |
| `clinicaId` | uuid | NO | `Clinica` | Tenant | I5 |
| `usuarioId` | uuid | NO | `Usuario` | Usuario con rol `profesional` activo | RF4 |
| `especialidadId` | uuid | NO | `Especialidad` | Especialidad principal | RF4 |
| `activo` | boolean | NO | — | Baja lógica | RF4 |
| `creadoEn` | timestamptz | NO | — | Timestamp de creación | — |
| `actualizadoEn` | timestamptz | NO | — | Timestamp de última actualización | — |

### `Servicio`

| Columna | Tipo | Nulo | FK | Descripción | Invariante |
|---------|------|------|----|-------------|------------|
| `id` | uuid | NO | PK | Identificador interno | — |
| `clinicaId` | uuid | NO | `Clinica` | Tenant | I5 |
| `nombre` | text | NO | UK(clínica) | Nombre del servicio | RF3 |
| `duracionMinutos` | integer | NO | — | Duración del espacio (1–1440) | I4 |
| `especialidadId` | uuid | NO | `Especialidad` | Especialidad requerida | RF3 |
| `activo` | boolean | NO | — | Baja lógica | RF3 |
| `creadoEn` | timestamptz | NO | — | Timestamp de creación | — |
| `actualizadoEn` | timestamptz | NO | — | Timestamp de última actualización | — |

### `ProfesionalServicio`

| Columna | Tipo | Nulo | FK | Descripción | Invariante |
|---------|------|------|----|-------------|------------|
| `clinicaId` | uuid | NO | PK, `Clinica` | Tenant | I5 |
| `profesionalId` | uuid | NO | PK, `Profesional` | Profesional habilitado | RF4 |
| `servicioId` | uuid | NO | PK, `Servicio` | Servicio habilitado | RF4 |
| `creadoEn` | timestamptz | NO | — | Timestamp de creación | — |

### `HorarioAtencion`

| Columna | Tipo | Nulo | FK | Descripción | Invariante |
|---------|------|------|----|-------------|------------|
| `id` | uuid | NO | PK | Identificador interno | — |
| `clinicaId` | uuid | NO | `Clinica` | Tenant | I5 |
| `profesionalId` | uuid | NO | `Profesional` | Profesional al que aplica | RF5 |
| `diaSemana` | smallint | NO | — | 1=lunes … 7=domingo | I3 |
| `horaInicio` | time | NO | — | Inicio del bloque | I3 |
| `horaFin` | time | NO | — | Fin del bloque | I3 |
| `vigenteDesde` | date | NO | — | Inicio de vigencia | RF5 |
| `vigenteHasta` | date | SÍ | — | Fin de vigencia (NULL = indefinido) | RF5 |
| `activo` | boolean | NO | — | Baja lógica | RF5 |
| `creadoEn` | timestamptz | NO | — | Timestamp de creación | — |
| `actualizadoEn` | timestamptz | NO | — | Timestamp de última actualización | — |

### `Bloqueo`

| Columna | Tipo | Nulo | FK | Descripción | Invariante |
|---------|------|------|----|-------------|------------|
| `id` | uuid | NO | PK | Identificador interno | — |
| `clinicaId` | uuid | NO | `Clinica` | Tenant | I5 |
| `profesionalId` | uuid | NO | `Profesional` | Profesional bloqueado | RF6 |
| `inicio` | timestamptz | NO | — | Inicio del bloqueo | I3 |
| `fin` | timestamptz | NO | — | Fin del bloqueo | I3 |
| `motivo` | text | NO | — | Motivo del bloqueo | RF6 |
| `creadoPorUsuarioId` | uuid | NO | `Usuario` | Usuario que creó el bloqueo | RF6 |
| `creadoEn` | timestamptz | NO | — | Timestamp de creación | — |

### `Paciente`

| Columna | Tipo | Nulo | FK | Descripción | Invariante |
|---------|------|------|----|-------------|------------|
| `id` | uuid | NO | PK | Identificador interno | — |
| `clinicaId` | uuid | NO | `Clinica` | Tenant | I5 |
| `identificacion` | text | NO | UK(clínica) | Identificación del paciente | RF14 |
| `nombre` | text | NO | — | Nombre completo | RF14 |
| `fechaNacimiento` | date | NO | — | Fecha de nacimiento | RF14 |
| `activo` | boolean | NO | — | Baja lógica | RF14 |
| `creadoEn` | timestamptz | NO | — | Timestamp de creación | — |
| `actualizadoEn` | timestamptz | NO | — | Timestamp de última actualización | — |

### `ContactoPaciente`

| Columna | Tipo | Nulo | FK | Descripción | Invariante |
|---------|------|------|----|-------------|------------|
| `id` | uuid | NO | PK | Identificador interno | — |
| `clinicaId` | uuid | NO | `Clinica` | Tenant | I5 |
| `pacienteId` | uuid | NO | `Paciente` | Paciente al que pertenece | RF14 |
| `tipoContactoId` | uuid | NO | `TipoContacto` | Tipo de contacto | RF14 |
| `valor` | text | NO | — | Valor (teléfono, email, dirección) | RF14 |
| `esPrincipal` | boolean | NO | — | Indica contacto principal del tipo | RF14 |
| `activo` | boolean | NO | — | Baja lógica | RF14 |
| `creadoEn` | timestamptz | NO | — | Timestamp de creación | — |
| `actualizadoEn` | timestamptz | NO | — | Timestamp de última actualización | — |

### `Cita`

| Columna | Tipo | Nulo | FK | Descripción | Invariante |
|---------|------|------|----|-------------|------------|
| `id` | uuid | NO | PK | Identificador interno | — |
| `clinicaId` | uuid | NO | `Clinica` | Tenant | I5 |
| `pacienteId` | uuid | NO | `Paciente` | Paciente | I2 |
| `profesionalId` | uuid | NO | `Profesional` | Profesional | I1 |
| `servicioId` | uuid | NO | `Servicio` | Servicio (fija duración) | I4 |
| `inicio` | timestamptz | NO | — | Inicio de la cita | I3 |
| `duracionMinutos` | integer | NO | — | Copia de `Servicio.duracionMinutos` | I4 |
| `fin` | timestamptz | NO | — | Fin calculado | I3/I4 |
| `estado` | EstadoCita | NO | — | `reservada`, `atendida`, `ausente`, `cancelada`, `reprogramada` | RF13 |
| `canceladaEn` | timestamptz | SÍ | — | Timestamp de cancelación | RF11 |
| `canceladaPorUsuarioId` | uuid | SÍ | `Usuario` | Usuario que canceló | RF11 |
| `motivoCancelacion` | text | SÍ | — | Motivo de cancelación | RF11 |
| `citaOrigenId` | uuid | SÍ | `Cita` | Cita anterior en reprogramación | RF10 |
| `creadoEn` | timestamptz | NO | — | Timestamp de creación | — |
| `actualizadoEn` | timestamptz | NO | — | Timestamp de última actualización | — |

### `NotaClinica`

| Columna | Tipo | Nulo | FK | Descripción | Invariante |
|---------|------|------|----|-------------|------------|
| `id` | uuid | NO | PK | Identificador interno | — |
| `clinicaId` | uuid | NO | `Clinica` | Tenant | I5 |
| `citaId` | uuid | NO | UK(clínica), `Cita` | Cita atendida | RF15 |
| `profesionalId` | uuid | NO | `Profesional` | Profesional que atendió la cita | RF15 |
| `contenido` | text | NO | — | Texto de la nota | RF15 |
| `creadoEn` | timestamptz | NO | — | Timestamp de creación | — |
| `actualizadoEn` | timestamptz | NO | — | Timestamp de última actualización | — |

### `Auditoria`

| Columna | Tipo | Nulo | FK | Descripción | Invariante |
|---------|------|------|----|-------------|------------|
| `id` | bigint | NO | PK | Identificador autoincremental | — |
| `clinicaId` | uuid | SÍ | — | Tenant (NULL para eventos sin clínica) | I5 |
| `usuarioId` | uuid | SÍ | — | Usuario que realizó la acción | RF18 |
| `idpSub` | text | SÍ | — | Subject del JWT | RF18 |
| `accion` | text | NO | — | `lectura` o `escritura` | RF18 |
| `entidad` | text | NO | — | Entidad auditada | RF18 |
| `entidadId` | uuid | SÍ | — | ID de la entidad auditada | RF18 |
| `pacienteId` | uuid | SÍ | — | Paciente relacionado | RF18 |
| `ocurridoEn` | timestamptz | NO | — | Timestamp del evento | RF18 |
| `requestId` | uuid | SÍ | — | ID de trazabilidad | RF18 |
| `metadata` | jsonb | NO | — | Metadata no sensible | RF18 |

### `AgendaVersion`

| Columna | Tipo | Nulo | FK | Descripción | Invariante |
|---------|------|------|----|-------------|------------|
| `clinicaId` | uuid | NO | PK, `Clinica` | Tenant | I5 |
| `profesionalId` | uuid | NO | PK, `Profesional` | Profesional | I8 |
| `version` | bigint | NO | — | Versión monótona creciente | I8 |
| `actualizadoEn` | timestamptz | NO | — | Timestamp de última actualización | I8 |

### Esquema `ops`

| Tabla | Columnas principales | Propósito |
|-------|----------------------|-----------|
| `LogEvento` | `id`, `ocurridoEn`, `nivel`, `servicio`, `categoria`, `requestId`, `ruta`, `metodo`, `status`, `duracionMs`, `mensaje`, `metadata` | Log técnico; sin datos clínicos |
| `JobEjecucion` | `id`, `tipo`, `inicio`, `fin`, `estado`, `detalle`, `error` | Control de ejecución de jobs |
| `Migracion` | `version`, `checksum`, `aplicadaEn`, `exito`, `detalle` | Control de migraciones |
| `Semilla` | `version`, `aplicadoEn`, `detalle` | Control de la semilla demo |

## Semilla

El job `seed` carga datos demo automáticamente al levantar el sistema con
`docker compose up`. Está documentado en [`seed/README.md`](../seed/README.md).

Resumen de la semilla:

| Entidad | Cantidad | Detalle |
|---------|---------:|---------|
| Clínicas | 3 | `clinica-norte`, `clinica-centro`, `clinica-sur` |
| Usuarios Keycloak | 13 | 1 plataforma, 3 adm-clínica, 3 recepción, 6 profesionales |
| Profesionales | 6 | 2 por clínica, con especialidad y servicio |
| Servicios | 6 | 2 por clínica |
| Horarios | 60 | lunes a viernes, 09:00–12:00 y 13:00–17:00 |
| Pacientes | 12 | 4 por clínica, con teléfono y email |
| Citas | 30 | 10 por clínica, distribuidas en estados |
| Notas clínicas | 9 | Solo en citas atendidas |

La semilla es idempotente: si `ops."Semilla"` ya contiene la versión
`SEED_VERSION`, el job termina sin duplicar datos. Para reiniciar desde cero:

```bash
docker compose down -v
docker compose up --build
```

Verificación rápida:

```bash
docker compose exec db psql -U medicitas -d medicitas \
  -c 'SELECT "estado", count(*) FROM "Cita" GROUP BY 1 ORDER BY 1;'
```

Resultado esperado: 12 `reservada`, 9 `atendida`, 3 `ausente`, 3 `cancelada` y
3 `reprogramada`.

## Relación con los invariantes

| Invariante | Cómo se sostiene en el modelo |
|------------|-------------------------------|
| **I1** — sin solape de profesional | `EXCLUDE USING gist` en `Cita` sobre `(clinicaId, profesionalId, tstzrange(inicio, fin)) WHERE estado = 'reservada'` |
| **I2** — sin solape de paciente | `EXCLUDE USING gist` en `Cita` sobre `(clinicaId, pacienteId, tstzrange(inicio, fin)) WHERE estado = 'reservada'` |
| **I3** — dentro de horario y fuera de bloqueos | Trigger `CitaValidarHorario` verifica `HorarioAtencion` activo y ausencia de `Bloqueo` solapado |
| **I4** — duración fijada por servicio | Trigger `CitaPreparar` copia `Servicio.duracionMinutos` a `Cita.duracionMinutos` y calcula `fin` |
| **I5** — aislamiento entre clínicas | `clinicaId` en todas las tablas de negocio, FKs compuestas, RLS en `03_rls.sql` |
| **I6** — bitácora append-only | Triggers `AuditoriaSinUpdate/Delete/Truncate` rechazan mutaciones |
| **I7** — liberación inmediata del espacio | Estados finales (`cancelada`, `reprogramada`) quedan fuera del `EXCLUDE` y de los índices parciales; `AgendaVersion` invalida la caché |
| **I8** — caché no contradice la BD | `AgendaVersion` se incrementa en la misma transacción de la mutación; la caché valida el snapshot contra la versión autoritativa |
