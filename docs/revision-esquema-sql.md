# Revisión del esquema SQL — MediCitas

Documento de revisión del modelo de datos implementado en [`db/migrations/01_schema.sql`](../db/migrations/01_schema.sql), contrastado con [`spec.md`](../spec.md), [`esquema-er.md`](esquema-er.md) y [`modelo-datos.md`](modelo-datos.md).

**Alcance:** solo hallazgos. Este documento **no** aplica cambios al DDL.

**Fecha de revisión original:** 2026-10-31
**Última actualización:** 2026-11-14 (sincronización con DDL actual)

---

## Veredicto

El esquema está **bien**. No hay errores graves de normalización ni relaciones rotas. Las FKs compuestas por `clinicaId`, el enum `EstadoCita`, las exclusiones GiST (I1/I2), la duración tomada del servicio (I4) y la auditoría append-only están bien pensadas y cubren el dominio clínico de la Etapa 1.

Lo que aparece no son fallas típicas de “entidades mal armadas”, sino:

1. Huecos de integridad que el motor ya cierra en la versión actual del DDL (ver Hallazgos actualizados).
2. Documentación desfasada respecto al SQL real, hoy en proceso de sincronización.
3. Entregables del proyecto implementados (semilla, roles DB/RLS, RLS como segunda estrategia).

Fuente de verdad del modelo: **`01_schema.sql`**. El ER en `esquema-er.md` está alineado. `modelo-datos.md` y el `README` se actualizaron para reflejar el esquema actual.

---

## Inventario breve

| Área | Contenido |
|------|-----------|
| Extensiones | `pgcrypto`, `btree_gist` |
| Esquemas | `public` (negocio), `app` (funciones de trigger), `ops` (técnico) |
| Enum | `EstadoCita`: reservada, atendida, ausente, cancelada, reprogramada |
| Tablas de negocio | Rol, TipoContacto, Especialidad, Clinica, Usuario, Profesional, Servicio, ProfesionalServicio, HorarioAtencion, Bloqueo, Paciente, ContactoPaciente, Cita, NotaClinica, Auditoria |
| Tablas ops | LogEvento, JobEjecucion, Migracion, Semilla |
| Semilla en schema | Catálogos base (roles, tipos de contacto); datos de negocio los carga el job `seed/` |

---

## Lo que está bien (no requiere corrección de modelo)

- Multi-tenancy por columna + FKs `(clinicaId, id)` en hechos: evita referencias cross-tenant en el INSERT.
- **I1 / I2:** `EXCLUDE USING gist` solo donde `estado = 'reservada'` → al cancelar o reprogramar se libera el slot (**I7**).
- **I4:** `prepararCita` fija `duracionMinutos` / `fin` desde `Servicio` y bloquea mutar el scheduling en UPDATE.
- **I3** en INSERT: horario de atención + bloqueos + zona horaria de la clínica + no cruzar medianoche.
- **RF13** parcial: estados terminales inmutables; desenlace solo después de `inicio`.
- **RF15:** nota 1:1 con cita; FK obliga al profesional de esa cita; solo si está `atendida`.
- **RF11:** CHECK de coherencia de campos de cancelación.
- Catálogos globales (`Rol`, `Especialidad`, `TipoContacto`) vs hechos por clínica: correcto.
- `Auditoria` append-only (triggers bloquean UPDATE / DELETE / TRUNCATE) y sin FKs a propósito.

---

## Hallazgos

### Estado de los hallazgos anteriores

Los puntos 1–5 de la revisión original ya están corregidos en el DDL actual de
[`01_schema.sql`](../db/migrations/01_schema.sql). Se conservan a continuación
como registro técnico, indicando en cada uno la resolución aplicada.

### 1. Máquina de estados en INSERT — corregido

**Observación original:** `CitaValidarTransicion` solo corría en UPDATE, por lo
que en INSERT se podía insertar una cita en estado distinto a `reservada`.

**Resolución:** la función `app."prepararCita"` rechaza explícitamente en su
rama INSERT cualquier `estado` distinto de `reservada`:

```sql
IF NEW."estado" IS DISTINCT FROM 'reservada' THEN
    RAISE EXCEPTION 'Una cita nueva debe crearse en estado reservada; se recibió %', NEW."estado";
END IF;
```

Esto ancla RF13 en el motor y evita saltarse la máquina de estados.

### 2. RF10 reprogramación atómica — corregido

**Observación original:** la reprogramación dependía de que la API ejecutara dos
operaciones juntas.

**Resolución:** existe la función `app."reprogramarCita"(...)` que, dentro de la
misma transacción, marca la cita origen como `reprogramada` e inserta la nueva
cita con `citaOrigenId`. Si la nueva cita viola alguna restricción, PostgreSQL
revierte también el cambio de la origen. Además, `app."validarCitaOrigen"`
exige que `citaOrigenId` apunte a una cita en estado `reprogramada`.

### 3. Anti-solape en `HorarioAtencion` — corregido

**Observación original:** faltaba impedir que dos bloques activos del mismo
profesional se solapen el mismo día.

**Resolución:** se añadió la restricción `HorarioAtencionSinSolape`:

```sql
EXCLUDE USING gist (
    "clinicaId" WITH =,
    "profesionalId" WITH =,
    "diaSemana" WITH =,
    tsrange(...) WITH &&,
    daterange("vigenteDesde", "vigenteHasta", '[]') WITH &&
) WHERE ("activo" = true);
```

### 4. Alineación de especialidad en `ProfesionalServicio` — corregido

**Observación original:** no se exigía que el servicio asociado a un profesional
fuera de su misma especialidad.

**Resolución:** el trigger `ProfesionalServicioValidarEspecialidad` ejecuta
`app."validarEspecialidadProfesionalServicio"`, que compara
`Profesional.especialidadId` con `Servicio.especialidadId` y rechaza la
asociación si no coinciden. El trigger `CitaPreparar` refuerza la misma
validación al reservar.

### 5. Validación de rol `profesional` al crear `Profesional` — corregido

**Observación original:** `Profesional.usuarioId` no validaba que el usuario
tuviera rol `profesional`.

**Resolución:** el trigger `ProfesionalValidarRol` ejecuta
`app."validarRolProfesional"`, que verifica que el usuario exista, esté activo,
pertenezca a la misma clínica y tenga rol `profesional`. El documento
[`modelo-datos.md`](modelo-datos.md) refleja que el enlace es obligatorio.

### 6. Documentación y naming — en proceso de sincronización

| Fuente | Estado |
|--------|--------|
| `modelo-datos.md` | Actualizado: diccionario completo, justificación 1FN/2FN/3FN, semilla y roles con guion bajo |
| SQL / seed | Roles con guion bajo (`adm_plataforma`); consistente |
| `README.md` | Actualizado: ya no dice que el esquema no existe; `api/auth.go` ya implementa JWKS |

### 7. Módulos pendientes en el momento de la revisión original — implementados

| Ítem | Estado |
|------|--------|
| Semilla (≥3 clínicas, profesionales, citas) | Implementada en [`seed/`](../seed/); 3 clínicas, 6 profesionales, 30 citas |
| Roles de BD / GRANTs / RLS | Implementados en [`03_rls.sql`](../db/migrations/03_rls.sql) y [`04_db_roles_matrix.sql`](../db/migrations/04_db_roles_matrix.sql) |
| **I5** a nivel motor | RLS habilitada; la API entra como `medicitas_app` y usa `SET ROLE` a perfiles de menor privilegio |
| SP de reprogramación | Implementado en `app."reprogramarCita"` |

---

## Relación con Tarea 1

De `tareaunobd2` conviene **reusar el patrón operativo**, no el dominio plano de `Reserva`:

- Escrituras vía **SPs** (reserva / reprogramación / cancelación) y API delgada.
- Compose + migraciones (sin Kubernetes en MediCitas).
- Keycloak + pruebas de integración.

El modelo MediCitas ya es el adecuado para citas médicas multi-tenant.

---

## Prioridad si más adelante se evoluciona el DDL

Los puntos críticos de la revisión original ya están resueltos. Las mejoras
futuras podrían orientarse a:

1. Endurecer el despliegue de Keycloak (service account dedicado en lugar de
   admin master para provisionar usuarios).
2. Agregar métricas y reintentos explícitos ante fallos de invalidación de Redis
   (`ErrCacheInvalidation`).
3. Evaluar single-flight en la caché de disponibilidad para reducir recálculos
   concurrentes.
4. Sincronizar periódicamente la documentación si el esquema sigue evolucionando.

---

## Conclusión

El modelo **sostiene el dominio** y las invariantes centrales de agenda (I1, I2,
I4, gran parte de I3/I6/I7/I8) están ancladas en el motor. Los hallazgos
identificados en la revisión original ya fueron corregidos en el DDL: control
de estados en INSERT, reprogramación atómica via SP, anti-solape de horarios,
alineación de especialidad, validación de rol profesional, RLS, perfiles SQL y
semilla multi-clínica. Los puntos a defender con honestidad ahora son el
endurecimiento operativo (Keycloak service account, política ante fallo de
invalidación de Redis) y la validación manual de las pruebas de integración en
un entorno limpio. No hace falta rediseñar el ER.
