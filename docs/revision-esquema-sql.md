# Revisión del esquema SQL — MediCitas

Documento de revisión del modelo de datos implementado en [`db/migrations/01_schema.sql`](../db/migrations/01_schema.sql), contrastado con [`spec.md`](../spec.md), [`esquema-er.md`](esquema-er.md) y [`modelo-datos.md`](modelo-datos.md).

**Alcance:** solo hallazgos. Este documento **no** aplica cambios al DDL.

**Fecha de revisión:** 2026-10-31

---

## Veredicto

El esquema está **bien**. No hay errores graves de normalización ni relaciones rotas. Las FKs compuestas por `clinicaId`, el enum `EstadoCita`, las exclusiones GiST (I1/I2), la duración tomada del servicio (I4) y la auditoría append-only están bien pensadas y cubren el dominio clínico de la Etapa 1.

Lo que aparece no son fallas típicas de “entidades mal armadas”, sino:

1. Huecos de integridad que el motor aún no cierra del todo.
2. Documentación desfasada respecto al SQL real.
3. Entregables del proyecto todavía pendientes (semilla, roles DB/RLS), que no invalidan el ER.

Fuente de verdad del modelo: **`01_schema.sql`**. El ER en `esquema-er.md` está alineado. `modelo-datos.md` y el `README` van atrasados.

---

## Inventario breve

| Área | Contenido |
|------|-----------|
| Extensiones | `pgcrypto`, `btree_gist` |
| Esquemas | `public` (negocio), `app` (funciones de trigger), `ops` (técnico) |
| Enum | `EstadoCita`: reservada, atendida, ausente, cancelada, reprogramada |
| Tablas de negocio | Rol, TipoContacto, Especialidad, Clinica, Usuario, Profesional, Servicio, ProfesionalServicio, HorarioAtencion, Bloqueo, Paciente, ContactoPaciente, Cita, NotaClinica, Auditoria |
| Tablas ops | LogEvento, JobEjecucion, Migracion |
| Semilla en schema | Solo roles y tipos de contacto |

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

### 1. Máquina de estados incompleta en INSERT — severidad media-alta

`CitaValidarTransicion` solo corre en **UPDATE**. En **INSERT** se puede crear una cita ya en `atendida`, `ausente`, `cancelada` o `reprogramada`, saltándose RF13 (“desde reservada…”).

**Corrección sugerida (cuando se toque el DDL):** en `prepararCita` (rama INSERT), forzar `estado = 'reservada'` o rechazar cualquier otro valor.

### 2. RF10 (reprogramación) no garantizado en BD — severidad media

El diseño documentado (marcar origen como `reprogramada` + nueva fila con `citaOrigenId`) está reflejado en comentarios y en el bloqueo de mutar campos de scheduling, pero **nada impone** que ambas operaciones vayan juntas. Dos filas `reservada` o un origen sin sucesor son posibles si la API falla a mitad de camino.

**Corrección sugerida:** SP `reprogramarCita(...)` en una sola transacción (patrón de Tarea 1: lógica en PostgreSQL). Opcional: trigger que, si hay `citaOrigenId`, exija que el origen pase a / esté en `reprogramada`.

### 3. Solapes en `HorarioAtencion` — severidad media

RF5 permite varios bloques por día (jornada partida), pero no dos bloques que se pisen. Hoy no hay exclusión sobre profesional + día + vigencia + rango horario.

**Corrección sugerida:** `EXCLUDE` / trigger para horarios activos vigentes que se solapen el mismo día.

### 4. Especialidad del profesional vs del servicio — severidad media-baja

Al asociar `ProfesionalServicio` o al reservar, solo se valida la N:M. No se exige que `Servicio.especialidadId` coincida con `Profesional.especialidadId` (intención RF3 / RF4).

**Corrección sugerida:** trigger en `ProfesionalServicio` (y refuerzo opcional en `prepararCita`).

### 5. `Profesional.usuarioId` sin validar rol — severidad baja-media

La FK compuesta a `Usuario` de la misma clínica es correcta. No se exige que el usuario tenga rol `profesional`. Además, `modelo-datos.md` habla de enlace “opcional”; el SQL lo hace **obligatorio** (decisión válida, pero hay que documentarla).

**Corrección sugerida:** trigger al INSERT/UPDATE de `Profesional`; alinear el documento de modelo.

### 6. Documentación y naming desfasados — severidad baja (pega en defensa / avance)

| Fuente | Problema |
|--------|----------|
| `modelo-datos.md` | snake_case; roles con guion (`adm-plataforma`); contacto “en columnas”; Profesional opcional |
| SQL / seed | códigos con guion bajo (`adm_plataforma`) |
| `README.md` | aún dice que el esquema no existe |
| Diccionario / justificación 3FN | pendiente (rúbrica “Modelo de datos”) |

No son bugs del DDL; conviene sincronizar docs antes del avance obligatorio.

### 7. Fuera del error de modelo (trabajo pendiente del proyecto)

| Ítem | Estado |
|------|--------|
| Semilla (≥3 clínicas, profesionales, citas) | Solo catálogos base en el SQL |
| Roles de BD / GRANTs / RLS | Diferidos a propósito (comentario en L11 del schema) |
| **I5** a nivel motor | Hoy depende de API + FKs compuestas; RLS es módulo opcional / posterior |
| SPs de dominio (reserva, listados) | Aún no; Tarea 1 sí tenía este patrón |

Estos puntos no invalidan el diagrama; son módulos 1, 2 y 5 de la spec todavía por completar.

---

## Relación con Tarea 1

De `tareaunobd2` conviene **reusar el patrón operativo**, no el dominio plano de `Reserva`:

- Escrituras vía **SPs** (reserva / reprogramación / cancelación) y API delgada.
- Compose + migraciones (sin Kubernetes en MediCitas).
- Keycloak + pruebas de integración.

El modelo MediCitas ya es el adecuado para citas médicas multi-tenant.

---

## Prioridad si más adelante se corrige el DDL

1. Forzar `estado = 'reservada'` en INSERT de `Cita`.
2. SP (o constraint) de reprogramación atómica (RF10).
3. Anti-solape en `HorarioAtencion`.
4. Alineación de especialidad en `ProfesionalServicio`.
5. Validar rol `profesional` al crear `Profesional`.
6. Sincronizar `modelo-datos.md`, README y códigos de rol con el SQL.

---

## Conclusión

Para el avance: el modelo **sostienen el dominio** y las invariantes centrales de agenda (I1, I2, I4, gran parte de I3/I6/I7) están ancladas en el motor. Los puntos a defender con honestidad son el INSERT de estados, RF10 aún en capa de aplicación, y que I5 / semilla / roles DB vienen después. No hace falta rediseñar el ER.
