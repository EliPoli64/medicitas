# Modelo de datos — MediCitas

Documento de trabajo del modelo relacional. La fuente de verdad es
[`db/migrations/01_schema.sql`](../db/migrations/01_schema.sql); el diagrama
completo está en [`docs/esquema-er.md`](esquema-er.md).

Convenciones del esquema:

- Tablas en **PascalCase** y columnas en **camelCase**. En PostgreSQL los
  identificadores mixtos deben ir entre comillas dobles.
- Las tablas de negocio viven en `public`; los objetos técnicos en `ops` y las
  funciones auxiliares en `app`.
- Todas las tablas de negocio llevan `clinicaId` y usan FKs compuestas por
  clínica para impedir referencias entre tenants.
- El aislamiento se reforzará con RLS en una migración posterior.

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
| **ContactoPaciente** | Contactos 1:N | Teléfono, correo o dirección; puede haber varios y uno principal |
| **Cita** | Espacio reservado | Paciente + profesional + servicio + intervalo; máquina de estados |
| **NotaClinica** | Dato sensible | 1:1 con cita atendida; solo el profesional que atendió |
| **Auditoria** | Bitácora append-only | Quién, qué, paciente y cuándo; sin FKs para conservar el rastro |
| **ops.LogEvento** | Log técnico | Sin datos clínicos ni de contacto |
| **ops.JobEjecucion** | Ejecución de jobs | Estado, inicio, fin y error |
| **ops.Migracion** | Control de migraciones | Versión, checksum y resultado |

## Cita: estados, cancelación y reprogramación

```
reservada → atendida | ausente | cancelada | reprogramada
```

- Los cuatro estados finales no admiten salida.
- Una cita nueva **siempre nace `reservada`**; no se inserta directamente una
  cita atendida, ausente, cancelada o reprogramada.
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

## Normalización (3FN)

1. **1FN:** atributos atómicos; los horarios son filas y los contactos del
   paciente viven en `ContactoPaciente`, no en columnas repetidas.
2. **2FN:** la N:M `ProfesionalServicio` está separada; la duración vive en
   `Servicio`, no repetida en cada cita vigente.
3. **3FN:** la especialidad del profesional no determina el tenant. `clinicaId`
   se propaga a los hechos (`Cita`, `NotaClinica`, `Auditoria`) para aislar sin
   depender solo de joins largos.

## Semilla mínima (pendiente)

Al menos **tres clínicas**, varios profesionales por clínica y citas repartidas
en el tiempo. Identificadores provisionales:

- `clinica-norte`
- `clinica-centro`
- `clinica-sur`

## Diccionario (pendiente de completar)

Tabla a completar en la entrega: nombre de columna, tipo SQL, nulo, FK,
descripción e invariante relacionado (I1–I8).
