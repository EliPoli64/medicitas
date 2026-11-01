-- =============================================================================
-- MediCitas — Esquema base (Etapa 1)
--
-- Convenciones:
--   * Tablas en PascalCase y columnas en camelCase. En PostgreSQL los
--     identificadores mixtos DEBEN ir entre comillas dobles; sin comillas
--     se pliegan a minúsculas. Todo el script usa identificadores citados.
--   * Las tablas de negocio viven en public. Los objetos técnicos viven en ops.
--   * Los catálogos base (roles y tipos de contacto) se cargan al final.
--
-- RLS, roles de base de datos y GRANTs se definen en una migración posterior.
-- =============================================================================

CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE SCHEMA IF NOT EXISTS app;
CREATE SCHEMA IF NOT EXISTS ops;

-- =============================================================================
-- Tipos y catálogos globales
-- =============================================================================

CREATE TYPE "EstadoCita" AS ENUM (
    'reservada',
    'atendida',
    'ausente',
    'cancelada',
    'reprogramada'
);

CREATE TABLE "Rol" (
    "id"             uuid        NOT NULL DEFAULT gen_random_uuid(),
    "codigo"         text        NOT NULL,
    "nombre"         text        NOT NULL,
    "descripcion"    text,
    "creadoEn"       timestamptz NOT NULL DEFAULT now(),
    "actualizadoEn"  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT "RolPk" PRIMARY KEY ("id"),
    CONSTRAINT "RolCodigoUq" UNIQUE ("codigo")
);

CREATE TABLE "TipoContacto" (
    "id"       uuid NOT NULL DEFAULT gen_random_uuid(),
    "codigo"   text NOT NULL,
    "nombre"   text NOT NULL,
    CONSTRAINT "TipoContactoPk" PRIMARY KEY ("id"),
    CONSTRAINT "TipoContactoCodigoUq" UNIQUE ("codigo")
);

CREATE TABLE "Especialidad" (
    "id"             uuid        NOT NULL DEFAULT gen_random_uuid(),
    "codigo"         text        NOT NULL,
    "nombre"         text        NOT NULL,
    "activo"         boolean     NOT NULL DEFAULT true,
    "creadoEn"       timestamptz NOT NULL DEFAULT now(),
    "actualizadoEn"  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT "EspecialidadPk" PRIMARY KEY ("id"),
    CONSTRAINT "EspecialidadCodigoUq" UNIQUE ("codigo")
);

-- =============================================================================
-- Tenant y usuarios
-- =============================================================================

CREATE TABLE "Clinica" (
    "id"             uuid        NOT NULL DEFAULT gen_random_uuid(),
    "identificador"  text        NOT NULL,
    "nombre"         text        NOT NULL,
    "zonaHoraria"    text        NOT NULL DEFAULT 'America/Costa_Rica',
    "activo"         boolean     NOT NULL DEFAULT true,
    "creadoEn"       timestamptz NOT NULL DEFAULT now(),
    "actualizadoEn"  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT "ClinicaPk" PRIMARY KEY ("id"),
    CONSTRAINT "ClinicaIdentificadorUq" UNIQUE ("identificador"),
    CONSTRAINT "ClinicaIdentificadorFormato" CHECK ("identificador" ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$')
);

CREATE TABLE "Usuario" (
    "id"             uuid        NOT NULL DEFAULT gen_random_uuid(),
    "idpSub"         text        NOT NULL,
    "clinicaId"      uuid,
    "rolId"          uuid        NOT NULL,
    "activo"         boolean     NOT NULL DEFAULT true,
    "creadoEn"       timestamptz NOT NULL DEFAULT now(),
    "actualizadoEn"  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT "UsuarioPk" PRIMARY KEY ("id"),
    CONSTRAINT "UsuarioIdpSubUq" UNIQUE ("idpSub"),
    CONSTRAINT "UsuarioClinicaIdUq" UNIQUE ("clinicaId", "id"),
    CONSTRAINT "UsuarioClinicaFk" FOREIGN KEY ("clinicaId")
        REFERENCES "Clinica" ("id"),
    CONSTRAINT "UsuarioRolFk" FOREIGN KEY ("rolId")
        REFERENCES "Rol" ("id")
);

CREATE TABLE "Profesional" (
    "id"             uuid        NOT NULL DEFAULT gen_random_uuid(),
    "clinicaId"      uuid        NOT NULL,
    "usuarioId"      uuid        NOT NULL,
    "especialidadId" uuid        NOT NULL,
    "activo"         boolean     NOT NULL DEFAULT true,
    "creadoEn"       timestamptz NOT NULL DEFAULT now(),
    "actualizadoEn"  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT "ProfesionalPk" PRIMARY KEY ("id"),
    CONSTRAINT "ProfesionalClinicaIdUq" UNIQUE ("clinicaId", "id"),
    CONSTRAINT "ProfesionalUsuarioUq" UNIQUE ("clinicaId", "usuarioId"),
    CONSTRAINT "ProfesionalClinicaFk" FOREIGN KEY ("clinicaId")
        REFERENCES "Clinica" ("id"),
    CONSTRAINT "ProfesionalUsuarioFk" FOREIGN KEY ("clinicaId", "usuarioId")
        REFERENCES "Usuario" ("clinicaId", "id"),
    CONSTRAINT "ProfesionalEspecialidadFk" FOREIGN KEY ("especialidadId")
        REFERENCES "Especialidad" ("id")
);

-- =============================================================================
-- Catálogo de servicios y agenda
-- =============================================================================

CREATE TABLE "Servicio" (
    "id"              uuid        NOT NULL DEFAULT gen_random_uuid(),
    "clinicaId"       uuid        NOT NULL,
    "nombre"          text        NOT NULL,
    "duracionMinutos" integer     NOT NULL,
    "especialidadId"  uuid        NOT NULL,
    "activo"          boolean     NOT NULL DEFAULT true,
    "creadoEn"        timestamptz NOT NULL DEFAULT now(),
    "actualizadoEn"   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT "ServicioPk" PRIMARY KEY ("id"),
    CONSTRAINT "ServicioClinicaIdUq" UNIQUE ("clinicaId", "id"),
    CONSTRAINT "ServicioNombreUq" UNIQUE ("clinicaId", "nombre"),
    CONSTRAINT "ServicioDuracionPositiva" CHECK ("duracionMinutos" > 0),
    CONSTRAINT "ServicioClinicaFk" FOREIGN KEY ("clinicaId")
        REFERENCES "Clinica" ("id"),
    CONSTRAINT "ServicioEspecialidadFk" FOREIGN KEY ("especialidadId")
        REFERENCES "Especialidad" ("id")
);

CREATE TABLE "ProfesionalServicio" (
    "clinicaId"     uuid        NOT NULL,
    "profesionalId" uuid        NOT NULL,
    "servicioId"    uuid        NOT NULL,
    "creadoEn"      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT "ProfesionalServicioPk"
        PRIMARY KEY ("clinicaId", "profesionalId", "servicioId"),
    CONSTRAINT "ProfesionalServicioProfesionalFk"
        FOREIGN KEY ("clinicaId", "profesionalId")
        REFERENCES "Profesional" ("clinicaId", "id"),
    CONSTRAINT "ProfesionalServicioServicioFk"
        FOREIGN KEY ("clinicaId", "servicioId")
        REFERENCES "Servicio" ("clinicaId", "id")
);

CREATE TABLE "HorarioAtencion" (
    "id"            uuid        NOT NULL DEFAULT gen_random_uuid(),
    "clinicaId"     uuid        NOT NULL,
    "profesionalId" uuid        NOT NULL,
    "diaSemana"     smallint    NOT NULL,
    "horaInicio"    time        NOT NULL,
    "horaFin"       time        NOT NULL,
    "vigenteDesde"  date        NOT NULL DEFAULT current_date,
    "vigenteHasta"  date,
    "activo"        boolean     NOT NULL DEFAULT true,
    "creadoEn"      timestamptz NOT NULL DEFAULT now(),
    "actualizadoEn" timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT "HorarioAtencionPk" PRIMARY KEY ("id"),
    CONSTRAINT "HorarioAtencionDiaValido" CHECK ("diaSemana" BETWEEN 1 AND 7),
    CONSTRAINT "HorarioAtencionRangoValido" CHECK ("horaInicio" < "horaFin"),
    CONSTRAINT "HorarioAtencionVigenciaValida"
        CHECK ("vigenteHasta" IS NULL OR "vigenteHasta" >= "vigenteDesde"),
    CONSTRAINT "HorarioAtencionProfesionalFk"
        FOREIGN KEY ("clinicaId", "profesionalId")
        REFERENCES "Profesional" ("clinicaId", "id")
);

-- RF5: varios bloques por día son válidos, pero no pueden solaparse para el
-- mismo profesional en el mismo día y con vigencias que se cruzan.
ALTER TABLE "HorarioAtencion"
    ADD CONSTRAINT "HorarioAtencionSinSolape"
    EXCLUDE USING gist (
        "clinicaId" WITH =,
        "profesionalId" WITH =,
        "diaSemana" WITH =,
        tsrange(
            date '2000-01-01' + "horaInicio",
            date '2000-01-01' + "horaFin",
            '[)'
        ) WITH &&,
        daterange("vigenteDesde", "vigenteHasta", '[]') WITH &&
    )
    WHERE ("activo" = true);

CREATE TABLE "Bloqueo" (
    "id"                  uuid        NOT NULL DEFAULT gen_random_uuid(),
    "clinicaId"           uuid        NOT NULL,
    "profesionalId"       uuid        NOT NULL,
    "inicio"              timestamptz NOT NULL,
    "fin"                 timestamptz NOT NULL,
    "motivo"              text        NOT NULL,
    "creadoPorUsuarioId"  uuid        NOT NULL,
    "creadoEn"            timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT "BloqueoPk" PRIMARY KEY ("id"),
    CONSTRAINT "BloqueoRangoValido" CHECK ("inicio" < "fin"),
    CONSTRAINT "BloqueoMotivoNoVacio" CHECK (btrim("motivo") <> ''),
    CONSTRAINT "BloqueoProfesionalFk"
        FOREIGN KEY ("clinicaId", "profesionalId")
        REFERENCES "Profesional" ("clinicaId", "id"),
    CONSTRAINT "BloqueoCreadoPorFk"
        FOREIGN KEY ("clinicaId", "creadoPorUsuarioId")
        REFERENCES "Usuario" ("clinicaId", "id")
);

-- =============================================================================
-- Pacientes y contactos
-- =============================================================================

CREATE TABLE "Paciente" (
    "id"              uuid        NOT NULL DEFAULT gen_random_uuid(),
    "clinicaId"       uuid        NOT NULL,
    "identificacion"  text        NOT NULL,
    "nombre"          text        NOT NULL,
    "fechaNacimiento" date        NOT NULL,
    "activo"          boolean     NOT NULL DEFAULT true,
    "creadoEn"        timestamptz NOT NULL DEFAULT now(),
    "actualizadoEn"   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT "PacientePk" PRIMARY KEY ("id"),
    CONSTRAINT "PacienteClinicaIdUq" UNIQUE ("clinicaId", "id"),
    CONSTRAINT "PacienteIdentificacionUq" UNIQUE ("clinicaId", "identificacion"),
    CONSTRAINT "PacienteClinicaFk" FOREIGN KEY ("clinicaId")
        REFERENCES "Clinica" ("id"),
    CONSTRAINT "PacienteIdentificacionNoVacia" CHECK (btrim("identificacion") <> ''),
    CONSTRAINT "PacienteNombreNoVacio" CHECK (btrim("nombre") <> '')
);

CREATE TABLE "ContactoPaciente" (
    "id"              uuid        NOT NULL DEFAULT gen_random_uuid(),
    "clinicaId"       uuid        NOT NULL,
    "pacienteId"      uuid        NOT NULL,
    "tipoContactoId"  uuid        NOT NULL,
    "valor"           text        NOT NULL,
    "esPrincipal"     boolean     NOT NULL DEFAULT false,
    "activo"          boolean     NOT NULL DEFAULT true,
    "creadoEn"        timestamptz NOT NULL DEFAULT now(),
    "actualizadoEn"   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT "ContactoPacientePk" PRIMARY KEY ("id"),
    CONSTRAINT "ContactoPacienteValorNoVacio" CHECK (btrim("valor") <> ''),
    CONSTRAINT "ContactoPacientePacienteFk"
        FOREIGN KEY ("clinicaId", "pacienteId")
        REFERENCES "Paciente" ("clinicaId", "id"),
    CONSTRAINT "ContactoPacienteTipoFk" FOREIGN KEY ("tipoContactoId")
        REFERENCES "TipoContacto" ("id")
);

-- Un solo contacto principal por tipo y paciente.
CREATE UNIQUE INDEX "ContactoPacientePrincipalUq"
    ON "ContactoPaciente" ("clinicaId", "pacienteId", "tipoContactoId")
    WHERE "esPrincipal" = true AND "activo" = true;

-- =============================================================================
-- Citas y notas clínicas
-- =============================================================================

CREATE TABLE "Cita" (
    "id"                     uuid          NOT NULL DEFAULT gen_random_uuid(),
    "clinicaId"              uuid          NOT NULL,
    "pacienteId"             uuid          NOT NULL,
    "profesionalId"          uuid          NOT NULL,
    "servicioId"             uuid          NOT NULL,
    "inicio"                 timestamptz   NOT NULL,
    "duracionMinutos"        integer       NOT NULL,
    "fin"                    timestamptz   NOT NULL,
    "estado"                 "EstadoCita"  NOT NULL DEFAULT 'reservada',
    "canceladaEn"            timestamptz,
    "canceladaPorUsuarioId"  uuid,
    "motivoCancelacion"      text,
    "citaOrigenId"           uuid,
    "creadoEn"               timestamptz   NOT NULL DEFAULT now(),
    "actualizadoEn"          timestamptz   NOT NULL DEFAULT now(),
    CONSTRAINT "CitaPk" PRIMARY KEY ("id"),
    CONSTRAINT "CitaClinicaIdUq" UNIQUE ("clinicaId", "id"),
    -- Necesaria para que NotaClinica pueda exigir el profesional de la cita.
    CONSTRAINT "CitaClinicaCitaProfesionalUq"
        UNIQUE ("clinicaId", "id", "profesionalId"),
    CONSTRAINT "CitaDuracionPositiva" CHECK ("duracionMinutos" > 0),
    CONSTRAINT "CitaRangoValido" CHECK ("fin" > "inicio"),
    CONSTRAINT "CitaOrigenDistinto" CHECK ("citaOrigenId" IS NULL OR "citaOrigenId" <> "id"),
    CONSTRAINT "CitaCancelacionCoherente" CHECK (
        (
            "estado" = 'cancelada'
            AND "canceladaEn" IS NOT NULL
            AND "canceladaPorUsuarioId" IS NOT NULL
            AND "motivoCancelacion" IS NOT NULL
            AND btrim("motivoCancelacion") <> ''
        )
        OR
        (
            "estado" <> 'cancelada'
            AND "canceladaEn" IS NULL
            AND "canceladaPorUsuarioId" IS NULL
            AND "motivoCancelacion" IS NULL
        )
    ),
    CONSTRAINT "CitaPacienteFk" FOREIGN KEY ("clinicaId", "pacienteId")
        REFERENCES "Paciente" ("clinicaId", "id"),
    CONSTRAINT "CitaProfesionalFk" FOREIGN KEY ("clinicaId", "profesionalId")
        REFERENCES "Profesional" ("clinicaId", "id"),
    CONSTRAINT "CitaServicioFk" FOREIGN KEY ("clinicaId", "servicioId")
        REFERENCES "Servicio" ("clinicaId", "id"),
    CONSTRAINT "CitaCanceladaPorFk"
        FOREIGN KEY ("clinicaId", "canceladaPorUsuarioId")
        REFERENCES "Usuario" ("clinicaId", "id"),
    CONSTRAINT "CitaOrigenFk" FOREIGN KEY ("clinicaId", "citaOrigenId")
        REFERENCES "Cita" ("clinicaId", "id")
);

-- I1: un profesional no puede tener dos citas vigentes solapadas.
ALTER TABLE "Cita"
    ADD CONSTRAINT "CitaProfesionalSinSolape"
    EXCLUDE USING gist (
        "clinicaId" WITH =,
        "profesionalId" WITH =,
        tstzrange("inicio", "fin", '[)') WITH &&
    )
    WHERE ("estado" = 'reservada');

-- I2: un paciente no puede tener dos citas vigentes solapadas en su clínica.
ALTER TABLE "Cita"
    ADD CONSTRAINT "CitaPacienteSinSolape"
    EXCLUDE USING gist (
        "clinicaId" WITH =,
        "pacienteId" WITH =,
        tstzrange("inicio", "fin", '[)') WITH &&
    )
    WHERE ("estado" = 'reservada');

CREATE TABLE "NotaClinica" (
    "id"            uuid        NOT NULL DEFAULT gen_random_uuid(),
    "clinicaId"     uuid        NOT NULL,
    "citaId"        uuid        NOT NULL,
    "profesionalId" uuid        NOT NULL,
    "contenido"     text        NOT NULL,
    "creadoEn"      timestamptz NOT NULL DEFAULT now(),
    "actualizadoEn" timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT "NotaClinicaPk" PRIMARY KEY ("id"),
    CONSTRAINT "NotaClinicaCitaUq" UNIQUE ("clinicaId", "citaId"),
    CONSTRAINT "NotaClinicaContenidoNoVacio" CHECK (btrim("contenido") <> ''),
    -- La FK compuesta obliga a que la nota la escriba el profesional de la cita.
    CONSTRAINT "NotaClinicaCitaProfesionalFk"
        FOREIGN KEY ("clinicaId", "citaId", "profesionalId")
        REFERENCES "Cita" ("clinicaId", "id", "profesionalId")
);

-- =============================================================================
-- Auditoría de datos sensibles (RF18/RF19)
--
-- Append-only: los triggers impiden UPDATE, DELETE y TRUNCATE. La tabla no
-- tiene FKs para conservar el rastro aunque se desactiven usuarios o pacientes.
-- =============================================================================

CREATE TABLE "Auditoria" (
    "id"          bigint      GENERATED ALWAYS AS IDENTITY,
    "clinicaId"   uuid,
    "usuarioId"   uuid,
    "idpSub"      text,
    "accion"      text        NOT NULL,
    "entidad"     text        NOT NULL,
    "entidadId"   uuid,
    "pacienteId"  uuid,
    "ocurridoEn"  timestamptz NOT NULL DEFAULT now(),
    "requestId"   uuid,
    "metadata"    jsonb       NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT "AuditoriaPk" PRIMARY KEY ("id"),
    CONSTRAINT "AuditoriaAccionValida" CHECK ("accion" IN ('lectura', 'escritura')),
    CONSTRAINT "AuditoriaEntidadNoVacia" CHECK (btrim("entidad") <> ''),
    CONSTRAINT "AuditoriaMetadataObjeto" CHECK (jsonb_typeof("metadata") = 'object')
);

-- =============================================================================
-- Funciones y triggers de negocio
-- =============================================================================

CREATE OR REPLACE FUNCTION app."setActualizadoEn"()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    NEW."actualizadoEn" := now();
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION app."validarUsuarioClinica"()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
    vRol text;
BEGIN
    SELECT r."codigo" INTO vRol
    FROM public."Rol" r
    WHERE r."id" = NEW."rolId";

    IF NOT FOUND THEN
        RAISE EXCEPTION 'El rol % no existe', NEW."rolId";
    END IF;

    IF vRol = 'adm_plataforma' AND NEW."clinicaId" IS NOT NULL THEN
        RAISE EXCEPTION 'El administrador de plataforma no pertenece a una clínica';
    END IF;

    IF vRol <> 'adm_plataforma' AND NEW."clinicaId" IS NULL THEN
        RAISE EXCEPTION 'El usuario con rol % debe pertenecer a una clínica', vRol;
    END IF;

    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION app."validarRolProfesional"()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
    vRol text;
BEGIN
    SELECT r."codigo" INTO vRol
    FROM public."Usuario" u
    JOIN public."Rol" r ON r."id" = u."rolId"
    WHERE u."id" = NEW."usuarioId"
      AND u."clinicaId" = NEW."clinicaId"
      AND u."activo" = true;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'El usuario % no existe, no está activo o no pertenece a la clínica %',
            NEW."usuarioId", NEW."clinicaId";
    END IF;

    IF vRol <> 'profesional' THEN
        RAISE EXCEPTION 'El usuario % tiene rol % y no puede asociarse como profesional',
            NEW."usuarioId", vRol;
    END IF;

    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION app."validarEspecialidadProfesionalServicio"()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
    vEspecialidadProfesional uuid;
    vEspecialidadServicio    uuid;
BEGIN
    SELECT p."especialidadId", s."especialidadId"
    INTO vEspecialidadProfesional, vEspecialidadServicio
    FROM public."Profesional" p
    JOIN public."Servicio" s
      ON s."id" = NEW."servicioId"
     AND s."clinicaId" = NEW."clinicaId"
    WHERE p."id" = NEW."profesionalId"
      AND p."clinicaId" = NEW."clinicaId";

    IF NOT FOUND THEN
        RAISE EXCEPTION 'El profesional % o el servicio % no existen en la clínica %',
            NEW."profesionalId", NEW."servicioId", NEW."clinicaId";
    END IF;

    IF vEspecialidadProfesional <> vEspecialidadServicio THEN
        RAISE EXCEPTION 'El servicio % no corresponde a la especialidad del profesional %',
            NEW."servicioId", NEW."profesionalId";
    END IF;

    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION app."prepararCita"()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
    vDuracion integer;
BEGIN
    IF TG_OP = 'INSERT' THEN
        -- Una cita nueva siempre nace reservada (RF13). No se permite
        -- insertar directamente una cita atendida, ausente, cancelada o
        -- reprogramada.
        IF NEW."estado" IS DISTINCT FROM 'reservada' THEN
            RAISE EXCEPTION 'Una cita nueva debe crearse en estado reservada; se recibió %',
                NEW."estado";
        END IF;

        SELECT s."duracionMinutos" INTO vDuracion
        FROM public."Servicio" s
        WHERE s."id" = NEW."servicioId"
          AND s."clinicaId" = NEW."clinicaId"
          AND s."activo" = true;

        IF NOT FOUND THEN
            RAISE EXCEPTION 'El servicio % no existe o no está activo en la clínica %',
                NEW."servicioId", NEW."clinicaId";
        END IF;

        IF NOT EXISTS (
            SELECT 1
            FROM public."Profesional" p
            WHERE p."id" = NEW."profesionalId"
              AND p."clinicaId" = NEW."clinicaId"
              AND p."activo" = true
        ) THEN
            RAISE EXCEPTION 'El profesional % no existe o no está activo en la clínica %',
                NEW."profesionalId", NEW."clinicaId";
        END IF;

        IF NOT EXISTS (
            SELECT 1
            FROM public."Paciente" p
            WHERE p."id" = NEW."pacienteId"
              AND p."clinicaId" = NEW."clinicaId"
              AND p."activo" = true
        ) THEN
            RAISE EXCEPTION 'El paciente % no existe o no está activo en la clínica %',
                NEW."pacienteId", NEW."clinicaId";
        END IF;

        IF NOT EXISTS (
            SELECT 1
            FROM public."ProfesionalServicio" ps
            WHERE ps."clinicaId" = NEW."clinicaId"
              AND ps."profesionalId" = NEW."profesionalId"
              AND ps."servicioId" = NEW."servicioId"
        ) THEN
            RAISE EXCEPTION 'El profesional % no está habilitado para el servicio %',
                NEW."profesionalId", NEW."servicioId";
        END IF;

        -- Refuerzo RF3/RF4: la asociación pudo quedar obsoleta si cambió la
        -- especialidad del profesional; la cita exige que sigan alineadas.
        IF NOT EXISTS (
            SELECT 1
            FROM public."Profesional" p
            JOIN public."Servicio" s
              ON s."id" = NEW."servicioId"
             AND s."clinicaId" = NEW."clinicaId"
            WHERE p."id" = NEW."profesionalId"
              AND p."clinicaId" = NEW."clinicaId"
              AND p."especialidadId" = s."especialidadId"
        ) THEN
            RAISE EXCEPTION 'El servicio % no corresponde a la especialidad del profesional %',
                NEW."servicioId", NEW."profesionalId";
        END IF;

        NEW."duracionMinutos" := vDuracion;
        NEW."fin" := NEW."inicio" + make_interval(mins => vDuracion);
        RETURN NEW;
    END IF;

    -- Una cita no se reprograma cambiando la fila: se marca reprogramada y se
    -- crea otra cita enlazada con citaOrigenId.
    IF NEW."clinicaId" IS DISTINCT FROM OLD."clinicaId"
       OR NEW."pacienteId" IS DISTINCT FROM OLD."pacienteId"
       OR NEW."profesionalId" IS DISTINCT FROM OLD."profesionalId"
       OR NEW."servicioId" IS DISTINCT FROM OLD."servicioId"
       OR NEW."inicio" IS DISTINCT FROM OLD."inicio"
       OR NEW."duracionMinutos" IS DISTINCT FROM OLD."duracionMinutos"
       OR NEW."citaOrigenId" IS DISTINCT FROM OLD."citaOrigenId"
    THEN
        RAISE EXCEPTION 'Los datos de la cita no se pueden modificar; use una reprogramación';
    END IF;

    NEW."fin" := OLD."fin";
    NEW."actualizadoEn" := now();
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION app."validarCitaEnHorario"()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
    vZona         text;
    vInicioLocal  timestamp;
    vFinLocal     timestamp;
    vDia          smallint;
BEGIN
    SELECT c."zonaHoraria" INTO vZona
    FROM public."Clinica" c
    WHERE c."id" = NEW."clinicaId";

    vInicioLocal := NEW."inicio" AT TIME ZONE vZona;
    vFinLocal := NEW."fin" AT TIME ZONE vZona;
    vDia := EXTRACT(ISODOW FROM vInicioLocal)::smallint;

    IF vInicioLocal::date <> vFinLocal::date THEN
        RAISE EXCEPTION 'La cita no puede cruzar la medianoche';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM public."HorarioAtencion" h
        WHERE h."clinicaId" = NEW."clinicaId"
          AND h."profesionalId" = NEW."profesionalId"
          AND h."diaSemana" = vDia
          AND h."activo" = true
          AND h."vigenteDesde" <= vInicioLocal::date
          AND (h."vigenteHasta" IS NULL OR h."vigenteHasta" >= vInicioLocal::date)
          AND vInicioLocal::time >= h."horaInicio"
          AND vFinLocal::time <= h."horaFin"
    ) THEN
        RAISE EXCEPTION 'La cita no cae dentro del horario de atención del profesional';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM public."Bloqueo" b
        WHERE b."clinicaId" = NEW."clinicaId"
          AND b."profesionalId" = NEW."profesionalId"
          AND tstzrange(b."inicio", b."fin", '[)')
              && tstzrange(NEW."inicio", NEW."fin", '[)')
    ) THEN
        RAISE EXCEPTION 'La cita cae dentro de un bloqueo de agenda';
    END IF;

    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION app."validarTransicionCita"()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    -- Los estados finales no admiten salida.
    IF OLD."estado" IN ('atendida', 'ausente', 'cancelada', 'reprogramada')
       AND NEW."estado" IS DISTINCT FROM OLD."estado"
    THEN
        RAISE EXCEPTION 'La cita % está en estado final % y no puede pasar a %',
            OLD."id", OLD."estado", NEW."estado";
    END IF;

    -- El desenlace solo se registra una vez pasada la hora de inicio.
    IF NEW."estado" IN ('atendida', 'ausente')
       AND now() < OLD."inicio"
    THEN
        RAISE EXCEPTION 'No se puede registrar el desenlace de una cita que aún no inicia';
    END IF;

    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION app."validarCitaOrigen"()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
    vEstado "EstadoCita";
BEGIN
    IF NEW."citaOrigenId" IS NULL THEN
        RETURN NEW;
    END IF;

    SELECT c."estado" INTO vEstado
    FROM public."Cita" c
    WHERE c."id" = NEW."citaOrigenId"
      AND c."clinicaId" = NEW."clinicaId";

    IF NOT FOUND THEN
        RAISE EXCEPTION 'La cita origen % no existe en la clínica %',
            NEW."citaOrigenId", NEW."clinicaId";
    END IF;

    IF vEstado <> 'reprogramada' THEN
        RAISE EXCEPTION 'La cita origen % debe estar reprogramada antes de crear la cita nueva',
            NEW."citaOrigenId";
    END IF;

    RETURN NEW;
END;
$$;

-- RF10: la reprogramación es atómica. Marca la cita original como
-- reprogramada y crea la nueva cita en la misma transacción. Si la nueva cita
-- viola una restricción, PostgreSQL revierte también el cambio de la original.
CREATE OR REPLACE FUNCTION app."reprogramarCita"(
    pCitaOrigenId uuid,
    pClinicaId uuid,
    pNuevoInicio timestamptz,
    pNuevoProfesionalId uuid DEFAULT NULL,
    pNuevoServicioId uuid DEFAULT NULL
)
RETURNS uuid
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
    vPacienteId    uuid;
    vProfesionalId uuid;
    vServicioId    uuid;
    vEstado        "EstadoCita";
    vNuevaCitaId   uuid;
BEGIN
    SELECT c."pacienteId", c."profesionalId", c."servicioId", c."estado"
    INTO vPacienteId, vProfesionalId, vServicioId, vEstado
    FROM public."Cita" c
    WHERE c."id" = pCitaOrigenId
      AND c."clinicaId" = pClinicaId
    FOR UPDATE;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'La cita origen % no existe en la clínica %',
            pCitaOrigenId, pClinicaId;
    END IF;

    IF vEstado <> 'reservada' THEN
        RAISE EXCEPTION 'Solo se puede reprogramar una cita reservada; la cita % está en estado %',
            pCitaOrigenId, vEstado;
    END IF;

    IF pNuevoProfesionalId IS NOT NULL THEN
        vProfesionalId := pNuevoProfesionalId;
    END IF;

    IF pNuevoServicioId IS NOT NULL THEN
        vServicioId := pNuevoServicioId;
    END IF;

    UPDATE public."Cita"
    SET "estado" = 'reprogramada'
    WHERE "id" = pCitaOrigenId
      AND "clinicaId" = pClinicaId;

    INSERT INTO public."Cita" (
        "clinicaId",
        "pacienteId",
        "profesionalId",
        "servicioId",
        "inicio",
        "citaOrigenId"
    ) VALUES (
        pClinicaId,
        vPacienteId,
        vProfesionalId,
        vServicioId,
        pNuevoInicio,
        pCitaOrigenId
    )
    RETURNING "id" INTO vNuevaCitaId;

    RETURN vNuevaCitaId;
END;
$$;

CREATE OR REPLACE FUNCTION app."validarNotaClinica"()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
    vEstado "EstadoCita";
BEGIN
    SELECT c."estado" INTO vEstado
    FROM public."Cita" c
    WHERE c."id" = NEW."citaId"
      AND c."clinicaId" = NEW."clinicaId"
      AND c."profesionalId" = NEW."profesionalId";

    IF NOT FOUND THEN
        RAISE EXCEPTION 'La cita no existe o no pertenece al profesional indicado';
    END IF;

    IF vEstado <> 'atendida' THEN
        RAISE EXCEPTION 'Solo se puede agregar nota clínica a una cita atendida';
    END IF;

    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION app."impedirMutacionAuditoria"()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'La tabla Auditoria es append-only: no admite UPDATE, DELETE ni TRUNCATE';
END;
$$;

-- Triggers de actualización de marcas de tiempo.

CREATE TRIGGER "RolActualizadoEn"
    BEFORE UPDATE ON "Rol"
    FOR EACH ROW EXECUTE FUNCTION app."setActualizadoEn"();

CREATE TRIGGER "EspecialidadActualizadoEn"
    BEFORE UPDATE ON "Especialidad"
    FOR EACH ROW EXECUTE FUNCTION app."setActualizadoEn"();

CREATE TRIGGER "ClinicaActualizadoEn"
    BEFORE UPDATE ON "Clinica"
    FOR EACH ROW EXECUTE FUNCTION app."setActualizadoEn"();

CREATE TRIGGER "UsuarioActualizadoEn"
    BEFORE UPDATE ON "Usuario"
    FOR EACH ROW EXECUTE FUNCTION app."setActualizadoEn"();

CREATE TRIGGER "ProfesionalActualizadoEn"
    BEFORE UPDATE ON "Profesional"
    FOR EACH ROW EXECUTE FUNCTION app."setActualizadoEn"();

CREATE TRIGGER "ServicioActualizadoEn"
    BEFORE UPDATE ON "Servicio"
    FOR EACH ROW EXECUTE FUNCTION app."setActualizadoEn"();

CREATE TRIGGER "HorarioAtencionActualizadoEn"
    BEFORE UPDATE ON "HorarioAtencion"
    FOR EACH ROW EXECUTE FUNCTION app."setActualizadoEn"();

CREATE TRIGGER "PacienteActualizadoEn"
    BEFORE UPDATE ON "Paciente"
    FOR EACH ROW EXECUTE FUNCTION app."setActualizadoEn"();

CREATE TRIGGER "ContactoPacienteActualizadoEn"
    BEFORE UPDATE ON "ContactoPaciente"
    FOR EACH ROW EXECUTE FUNCTION app."setActualizadoEn"();

CREATE TRIGGER "NotaClinicaActualizadoEn"
    BEFORE UPDATE ON "NotaClinica"
    FOR EACH ROW EXECUTE FUNCTION app."setActualizadoEn"();

-- Triggers de reglas de negocio.

CREATE TRIGGER "UsuarioValidarClinica"
    BEFORE INSERT OR UPDATE ON "Usuario"
    FOR EACH ROW EXECUTE FUNCTION app."validarUsuarioClinica"();

CREATE TRIGGER "ProfesionalValidarRol"
    BEFORE INSERT OR UPDATE ON "Profesional"
    FOR EACH ROW EXECUTE FUNCTION app."validarRolProfesional"();

CREATE TRIGGER "ProfesionalServicioValidarEspecialidad"
    BEFORE INSERT OR UPDATE ON "ProfesionalServicio"
    FOR EACH ROW EXECUTE FUNCTION app."validarEspecialidadProfesionalServicio"();

CREATE TRIGGER "CitaPreparar"
    BEFORE INSERT OR UPDATE ON "Cita"
    FOR EACH ROW EXECUTE FUNCTION app."prepararCita"();

CREATE TRIGGER "CitaValidarHorario"
    BEFORE INSERT ON "Cita"
    FOR EACH ROW EXECUTE FUNCTION app."validarCitaEnHorario"();

CREATE TRIGGER "CitaValidarOrigen"
    BEFORE INSERT ON "Cita"
    FOR EACH ROW EXECUTE FUNCTION app."validarCitaOrigen"();

CREATE TRIGGER "CitaValidarTransicion"
    BEFORE UPDATE ON "Cita"
    FOR EACH ROW EXECUTE FUNCTION app."validarTransicionCita"();

CREATE TRIGGER "NotaClinicaValidar"
    BEFORE INSERT OR UPDATE ON "NotaClinica"
    FOR EACH ROW EXECUTE FUNCTION app."validarNotaClinica"();

CREATE TRIGGER "AuditoriaSinUpdate"
    BEFORE UPDATE ON "Auditoria"
    FOR EACH ROW EXECUTE FUNCTION app."impedirMutacionAuditoria"();

CREATE TRIGGER "AuditoriaSinDelete"
    BEFORE DELETE ON "Auditoria"
    FOR EACH ROW EXECUTE FUNCTION app."impedirMutacionAuditoria"();

CREATE TRIGGER "AuditoriaSinTruncate"
    BEFORE TRUNCATE ON "Auditoria"
    FOR EACH STATEMENT EXECUTE FUNCTION app."impedirMutacionAuditoria"();

-- =============================================================================
-- Índices
-- =============================================================================

CREATE INDEX "UsuarioClinicaIdx" ON "Usuario" ("clinicaId");
CREATE INDEX "UsuarioRolIdx" ON "Usuario" ("rolId");

CREATE INDEX "ProfesionalClinicaIdx" ON "Profesional" ("clinicaId");
CREATE INDEX "ProfesionalEspecialidadIdx" ON "Profesional" ("especialidadId");
CREATE INDEX "ProfesionalServicioServicioIdx"
    ON "ProfesionalServicio" ("clinicaId", "servicioId");

CREATE INDEX "ServicioClinicaIdx" ON "Servicio" ("clinicaId", "activo");

CREATE INDEX "HorarioAtencionProfesionalIdx"
    ON "HorarioAtencion" ("clinicaId", "profesionalId", "diaSemana")
    WHERE "activo" = true;

CREATE INDEX "BloqueoProfesionalRangoIdx"
    ON "Bloqueo" ("clinicaId", "profesionalId", "inicio", "fin");

CREATE INDEX "PacienteClinicaIdx" ON "Paciente" ("clinicaId", "activo");
CREATE INDEX "ContactoPacientePacienteIdx"
    ON "ContactoPaciente" ("clinicaId", "pacienteId")
    WHERE "activo" = true;

-- RF7: disponibilidad y agenda.
CREATE INDEX "CitaProfesionalInicioIdx"
    ON "Cita" ("clinicaId", "profesionalId", "inicio")
    WHERE "estado" = 'reservada';

CREATE INDEX "CitaPacienteInicioIdx"
    ON "Cita" ("clinicaId", "pacienteId", "inicio");

CREATE INDEX "CitaClinicaInicioIdx"
    ON "Cita" ("clinicaId", "inicio");

CREATE INDEX "CitaClinicaEstadoIdx"
    ON "Cita" ("clinicaId", "estado");

CREATE INDEX "AuditoriaClinicaFechaIdx"
    ON "Auditoria" ("clinicaId", "ocurridoEn");

CREATE INDEX "AuditoriaPacienteFechaIdx"
    ON "Auditoria" ("clinicaId", "pacienteId", "ocurridoEn");

CREATE INDEX "AuditoriaUsuarioFechaIdx"
    ON "Auditoria" ("clinicaId", "usuarioId", "ocurridoEn");

-- =============================================================================
-- Esquema técnico ops
--
-- No contiene datos clínicos ni datos de contacto. Es diagnostico, no la
-- bitácora de RF18. La aplicación escribira aqui mediante SPs controlados.
-- =============================================================================

CREATE TABLE ops."LogEvento" (
    "id"          bigint      GENERATED ALWAYS AS IDENTITY,
    "ocurridoEn"  timestamptz NOT NULL DEFAULT now(),
    "nivel"       text        NOT NULL,
    "servicio"    text        NOT NULL DEFAULT 'api',
    "categoria"   text        NOT NULL,
    "requestId"   uuid,
    "traceId"     text,
    "ruta"        text,
    "metodo"      text,
    "status"      integer,
    "duracionMs"  integer,
    "mensaje"     text,
    "errorCodigo" text,
    "sqlstate"    text,
    "metadata"    jsonb       NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT "LogEventoPk" PRIMARY KEY ("id"),
    CONSTRAINT "LogEventoNivelValido"
        CHECK ("nivel" IN ('debug', 'info', 'warn', 'error', 'fatal')),
    CONSTRAINT "LogEventoDuracionNoNegativa" CHECK ("duracionMs" IS NULL OR "duracionMs" >= 0),
    CONSTRAINT "LogEventoMetadataObjeto" CHECK (jsonb_typeof("metadata") = 'object')
);

CREATE INDEX "LogEventoOcurridoEnIdx" ON ops."LogEvento" ("ocurridoEn");
CREATE INDEX "LogEventoRequestIdIdx" ON ops."LogEvento" ("requestId");
CREATE INDEX "LogEventoNivelOcurridoEnIdx" ON ops."LogEvento" ("nivel", "ocurridoEn");

CREATE TABLE ops."JobEjecucion" (
    "id"        bigint      GENERATED ALWAYS AS IDENTITY,
    "tipo"      text        NOT NULL,
    "inicio"    timestamptz NOT NULL DEFAULT now(),
    "fin"       timestamptz,
    "estado"    text        NOT NULL,
    "detalle"   text,
    "error"     text,
    CONSTRAINT "JobEjecucionPk" PRIMARY KEY ("id"),
    CONSTRAINT "JobEjecucionEstadoValido"
        CHECK ("estado" IN ('en_ejecucion', 'exitoso', 'fallido')),
    CONSTRAINT "JobEjecucionRangoValido"
        CHECK ("fin" IS NULL OR "fin" >= "inicio")
);

CREATE INDEX "JobEjecucionInicioIdx" ON ops."JobEjecucion" ("inicio");

CREATE TABLE ops."Migracion" (
    "version"    text        NOT NULL,
    "checksum"   text,
    "aplicadaEn" timestamptz NOT NULL DEFAULT now(),
    "exito"      boolean     NOT NULL DEFAULT true,
    "detalle"    text,
    CONSTRAINT "MigracionPk" PRIMARY KEY ("version")
);

-- =============================================================================
-- Catálogos base
-- =============================================================================

INSERT INTO "Rol" ("codigo", "nombre", "descripcion") VALUES
    ('adm_plataforma', 'Administrador de plataforma', 'Da de alta clínicas; no accede a datos clínicos'),
    ('adm_clinica',    'Administrador de clínica',    'Gestiona usuarios, catálogo, agenda y auditoría de su clínica'),
    ('recepcion',      'Recepción',                   'Registra pacientes y opera la agenda'),
    ('profesional',    'Profesional de salud',        'Consulta su agenda y redacta notas clínicas')
ON CONFLICT ("codigo") DO NOTHING;

INSERT INTO "TipoContacto" ("codigo", "nombre") VALUES
    ('telefono',  'Teléfono'),
    ('email',     'Correo electrónico'),
    ('direccion', 'Dirección')
ON CONFLICT ("codigo") DO NOTHING;
