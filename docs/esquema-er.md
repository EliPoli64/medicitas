# Esquema entidad-relación - MediCitas

Diagrama del esquema creado en [`db/migrations/01_schema.sql`](../db/migrations/01_schema.sql).

- **PK**: clave primaria.
- **FK**: clave foránea.
- **UK**: restricción de unicidad.
- Todas las tablas de negocio llevan `clinicaId` y usan FKs compuestas por clínica para impedir referencias entre tenants.
- `Auditoria` no tiene FKs a propósito: debe conservar el rastro aunque se desactiven usuarios o pacientes.
- El esquema `ops` es técnico y no contiene datos clínicos ni datos de contacto.

## Esquema `public`

```mermaid
erDiagram
    Rol {
        uuid id PK
        text codigo UK
        text nombre
        text descripcion
        timestamptz creadoEn
        timestamptz actualizadoEn
    }

    TipoContacto {
        uuid id PK
        text codigo UK
        text nombre
    }

    Especialidad {
        uuid id PK
        text codigo UK
        text nombre
        boolean activo
        timestamptz creadoEn
        timestamptz actualizadoEn
    }

    Clinica {
        uuid id PK
        text identificador UK
        text nombre
        text zonaHoraria
        boolean activo
        timestamptz creadoEn
        timestamptz actualizadoEn
    }

    Usuario {
        uuid id PK
        text idpSub UK
        uuid clinicaId FK
        uuid rolId FK
        boolean activo
        timestamptz creadoEn
        timestamptz actualizadoEn
    }

    Profesional {
        uuid id PK
        uuid clinicaId FK
        uuid usuarioId FK
        uuid especialidadId FK
        boolean activo
        timestamptz creadoEn
        timestamptz actualizadoEn
    }

    Servicio {
        uuid id PK
        uuid clinicaId FK
        text nombre
        integer duracionMinutos
        uuid especialidadId FK
        boolean activo
        timestamptz creadoEn
        timestamptz actualizadoEn
    }

    ProfesionalServicio {
        uuid clinicaId PK
        uuid profesionalId PK
        uuid servicioId PK
        timestamptz creadoEn
    }

    HorarioAtencion {
        uuid id PK
        uuid clinicaId FK
        uuid profesionalId FK
        smallint diaSemana
        time horaInicio
        time horaFin
        date vigenteDesde
        date vigenteHasta
        boolean activo
        timestamptz creadoEn
        timestamptz actualizadoEn
    }

    Bloqueo {
        uuid id PK
        uuid clinicaId FK
        uuid profesionalId FK
        timestamptz inicio
        timestamptz fin
        text motivo
        uuid creadoPorUsuarioId FK
        timestamptz creadoEn
    }

    Paciente {
        uuid id PK
        uuid clinicaId FK
        text identificacion
        text nombre
        date fechaNacimiento
        boolean activo
        timestamptz creadoEn
        timestamptz actualizadoEn
    }

    ContactoPaciente {
        uuid id PK
        uuid clinicaId FK
        uuid pacienteId FK
        uuid tipoContactoId FK
        text valor
        boolean esPrincipal
        boolean activo
        timestamptz creadoEn
        timestamptz actualizadoEn
    }

    Cita {
        uuid id PK
        uuid clinicaId FK
        uuid pacienteId FK
        uuid profesionalId FK
        uuid servicioId FK
        timestamptz inicio
        integer duracionMinutos
        timestamptz fin
        EstadoCita estado
        timestamptz canceladaEn
        uuid canceladaPorUsuarioId FK
        text motivoCancelacion
        uuid citaOrigenId FK
        timestamptz creadoEn
        timestamptz actualizadoEn
    }

    NotaClinica {
        uuid id PK
        uuid clinicaId FK
        uuid citaId FK
        uuid profesionalId FK
        text contenido
        timestamptz creadoEn
        timestamptz actualizadoEn
    }

    Auditoria {
        bigint id PK
        uuid clinicaId
        uuid usuarioId
        text idpSub
        text accion
        text entidad
        uuid entidadId
        uuid pacienteId
        timestamptz ocurridoEn
        uuid requestId
        jsonb metadata
    }

    Clinica |o--o{ Usuario : tiene
    Rol ||--o{ Usuario : asigna

    Clinica ||--o{ Profesional : emplea
    Usuario ||--o| Profesional : es
    Especialidad ||--o{ Profesional : clasifica

    Clinica ||--o{ Servicio : cataloga
    Especialidad ||--o{ Servicio : requiere

    Profesional ||--o{ ProfesionalServicio : habilita
    Servicio ||--o{ ProfesionalServicio : habilita

    Clinica ||--o{ HorarioAtencion : agenda
    Profesional ||--o{ HorarioAtencion : define

    Clinica ||--o{ Bloqueo : registra
    Profesional ||--o{ Bloqueo : bloquea
    Usuario ||--o{ Bloqueo : crea

    Clinica ||--o{ Paciente : registra
    Paciente ||--o{ ContactoPaciente : tiene
    TipoContacto ||--o{ ContactoPaciente : tipifica

    Clinica ||--o{ Cita : agenda
    Paciente ||--o{ Cita : solicita
    Profesional ||--o{ Cita : atiende
    Servicio ||--o{ Cita : tipifica
    Usuario |o--o{ Cita : cancela
    Cita |o--o{ Cita : origen

    Cita ||--o| NotaClinica : genera
    Profesional ||--o{ NotaClinica : escribe
```

## Esquema `ops`

```mermaid
erDiagram
    LogEvento {
        bigint id PK
        timestamptz ocurridoEn
        text nivel
        text servicio
        text categoria
        uuid requestId
        text traceId
        text ruta
        text metodo
        integer status
        integer duracionMs
        text mensaje
        text errorCodigo
        text sqlstate
        jsonb metadata
    }

    JobEjecucion {
        bigint id PK
        text tipo
        timestamptz inicio
        timestamptz fin
        text estado
        text detalle
        text error
    }

    Migracion {
        text version PK
        text checksum
        timestamptz aplicadaEn
        boolean exito
        text detalle
    }
```

## Notas del modelo

- **Estados de la cita:** `reservada`, `atendida`, `ausente`, `cancelada` y `reprogramada`. Los cuatro últimos son finales.
- **Cancelación:** vive en `Cita` mediante `canceladaEn`, `canceladaPorUsuarioId` y `motivoCancelacion`.
- **Reprogramación:** la cita original pasa a `reprogramada` y se crea otra cita con `citaOrigenId` apuntando a la anterior.
- **Duración:** `Cita.duracionMinutos` es una instantánea de `Servicio.duracionMinutos`; `fin` se calcula al insertar.
- **Solapamientos:** `Cita` tiene restricciones de exclusión por profesional y por paciente para citas `reservada`.
- **Horario y bloqueos:** se validan al insertar una cita, usando la zona horaria de `Clinica`.
- **Nota clínica:** solo se permite en citas `atendida` y la FK compuesta obliga a que la escriba el profesional de la cita.
- **Auditoría:** `Auditoria` es append-only; los triggers bloquean `UPDATE`, `DELETE` y `TRUNCATE`.
- **RLS:** pendiente en una migración posterior. Las columnas `clinicaId` y las FKs compuestas ya están preparadas.

## Cómo visualizarlo

- En GitHub, este archivo se renderiza automáticamente.
- En VS Code, con la extensión **Markdown Preview Mermaid Support**.
- En el [Mermaid Live Editor](https://mermaid.live/), pegando el contenido de los bloques `mermaid`.
- Contra la base real, con DBeaver: seleccionar las tablas de `public` o `ops` y abrir **ER Diagram**.
