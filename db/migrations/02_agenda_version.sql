-- =============================================================================
-- MediCitas — Migración 02: versionado autoritativo de agendas en PostgreSQL
--
-- Objetivo: mantener una versión monótona creciente por (clínica, profesional)
-- que cambie atómicamente junto con cualquier modificación que afecte la
-- disponibilidad. Esta versión complementa el epoch de Redis sin reemplazarlo.
--
-- La versión se actualiza mediante triggers para garantizar atomicidad,
-- independientemente de si la modificación viene por la API o por otro canal.
-- =============================================================================

-- ---------------------------------------------------------------------------
-- Tabla de versiones
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS "AgendaVersion" (
    "clinicaId"     uuid        NOT NULL,
    "profesionalId" uuid        NOT NULL,
    "version"       bigint      NOT NULL DEFAULT 0,
    "actualizadoEn" timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT "AgendaVersionPk" PRIMARY KEY ("clinicaId", "profesionalId"),
    CONSTRAINT "AgendaVersionClinicaFk"
        FOREIGN KEY ("clinicaId")
        REFERENCES "Clinica" ("id"),
    CONSTRAINT "AgendaVersionProfesionalFk"
        FOREIGN KEY ("clinicaId", "profesionalId")
        REFERENCES "Profesional" ("clinicaId", "id")
);

CREATE INDEX IF NOT EXISTS "AgendaVersionProfesionalIdx"
    ON "AgendaVersion" ("clinicaId", "profesionalId");

-- Versión inicial para profesionales existentes (migración).
INSERT INTO "AgendaVersion" ("clinicaId", "profesionalId", "version")
SELECT "clinicaId", "id", 0
FROM "Profesional"
ON CONFLICT ("clinicaId", "profesionalId") DO NOTHING;

-- ---------------------------------------------------------------------------
-- Funciones helper
-- ---------------------------------------------------------------------------

-- Crea la fila de versión 0 cuando se registra un profesional nuevo.
CREATE OR REPLACE FUNCTION app."crearAgendaVersionProfesional"()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
    INSERT INTO "AgendaVersion" ("clinicaId", "profesionalId", "version")
    VALUES (NEW."clinicaId", NEW."id", 0)
    ON CONFLICT ("clinicaId", "profesionalId") DO NOTHING;
    RETURN NEW;
END;
$$;

-- Incrementa en uno la versión de una agenda concreta.
CREATE OR REPLACE FUNCTION app."incrementarAgendaVersion"(
    pClinicaId uuid,
    pProfesionalId uuid
)
RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
    UPDATE "AgendaVersion"
    SET "version" = "version" + 1,
        "actualizadoEn" = now()
    WHERE "clinicaId" = pClinicaId
      AND "profesionalId" = pProfesionalId;
END;
$$;

-- Incrementa en uno la versión de todos los profesionales habilitados para un
-- servicio en una clínica.
CREATE OR REPLACE FUNCTION app."incrementarAgendaVersionPorServicio"(
    pClinicaId uuid,
    pServicioId uuid
)
RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
    UPDATE "AgendaVersion" av
    SET "version" = av."version" + 1,
        "actualizadoEn" = now()
    FROM "ProfesionalServicio" ps
    WHERE ps."clinicaId" = pClinicaId
      AND ps."servicioId" = pServicioId
      AND av."clinicaId" = ps."clinicaId"
      AND av."profesionalId" = ps."profesionalId";
END;
$$;

-- ---------------------------------------------------------------------------
-- Funciones de trigger
-- ---------------------------------------------------------------------------

-- RF4: cambio de estado (activo) de un profesional.
CREATE OR REPLACE FUNCTION app."trgAgendaVersionProfesional"()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
    IF NEW."activo" IS DISTINCT FROM OLD."activo" THEN
        PERFORM app."incrementarAgendaVersion"(NEW."clinicaId", NEW."id");
    END IF;
    RETURN NEW;
END;
$$;

-- RF4: cambio en los servicios habilitados de un profesional.
CREATE OR REPLACE FUNCTION app."trgAgendaVersionProfesionalServicio"()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
    vClinicaId uuid;
    vProfesionalId uuid;
BEGIN
    IF TG_OP = 'INSERT' THEN
        vClinicaId := NEW."clinicaId";
        vProfesionalId := NEW."profesionalId";
    ELSE
        vClinicaId := OLD."clinicaId";
        vProfesionalId := OLD."profesionalId";
    END IF;

    PERFORM app."incrementarAgendaVersion"(vClinicaId, vProfesionalId);
    RETURN COALESCE(NEW, OLD);
END;
$$;

-- RF3: cambio de duración o estado de un servicio.
CREATE OR REPLACE FUNCTION app."trgAgendaVersionServicio"()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
    IF NEW."duracionMinutos" IS DISTINCT FROM OLD."duracionMinutos"
       OR NEW."activo" IS DISTINCT FROM OLD."activo" THEN
        PERFORM app."incrementarAgendaVersionPorServicio"(NEW."clinicaId", NEW."id");
    END IF;
    RETURN NEW;
END;
$$;

-- RF5: cambio en horarios de atención.
CREATE OR REPLACE FUNCTION app."trgAgendaVersionHorarioAtencion"()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
    vClinicaId uuid;
    vProfesionalId uuid;
    vRelevante boolean := false;
BEGIN
    IF TG_OP = 'INSERT' THEN
        vClinicaId := NEW."clinicaId";
        vProfesionalId := NEW."profesionalId";
        vRelevante := true;
    ELSIF TG_OP = 'DELETE' THEN
        vClinicaId := OLD."clinicaId";
        vProfesionalId := OLD."profesionalId";
        vRelevante := true;
    ELSIF TG_OP = 'UPDATE' THEN
        vClinicaId := NEW."clinicaId";
        vProfesionalId := NEW."profesionalId";
        IF NEW."diaSemana" IS DISTINCT FROM OLD."diaSemana"
           OR NEW."horaInicio" IS DISTINCT FROM OLD."horaInicio"
           OR NEW."horaFin" IS DISTINCT FROM OLD."horaFin"
           OR NEW."vigenteDesde" IS DISTINCT FROM OLD."vigenteDesde"
           OR NEW."vigenteHasta" IS DISTINCT FROM OLD."vigenteHasta"
           OR NEW."activo" IS DISTINCT FROM OLD."activo" THEN
            vRelevante := true;
        END IF;
    END IF;

    IF vRelevante THEN
        PERFORM app."incrementarAgendaVersion"(vClinicaId, vProfesionalId);
    END IF;
    RETURN COALESCE(NEW, OLD);
END;
$$;

-- RF6: alta o baja de bloqueos.
CREATE OR REPLACE FUNCTION app."trgAgendaVersionBloqueo"()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
    vClinicaId uuid;
    vProfesionalId uuid;
BEGIN
    IF TG_OP = 'INSERT' THEN
        vClinicaId := NEW."clinicaId";
        vProfesionalId := NEW."profesionalId";
    ELSIF TG_OP = 'DELETE' THEN
        vClinicaId := OLD."clinicaId";
        vProfesionalId := OLD."profesionalId";
    ELSE
        -- "Bloqueo" no admite UPDATE; por seguridad no se hace nada.
        RETURN COALESCE(NEW, OLD);
    END IF;

    PERFORM app."incrementarAgendaVersion"(vClinicaId, vProfesionalId);
    RETURN COALESCE(NEW, OLD);
END;
$$;

-- RF9-RF11: cambios en citas que afectan la ocupación de la agenda.
-- Solo el estado 'reservada' ocupa un espacio ofrecido. Las transiciones
-- desde/hacia 'reservada' (cancelación, reprogramación, desenlace), cualquier
-- cambio de horario en una cita reservada, y cualquier cambio de profesional
-- o clínica alteran la disponibilidad.
CREATE OR REPLACE FUNCTION app."trgAgendaVersionCita"()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW."estado" = 'reservada' THEN
            PERFORM app."incrementarAgendaVersion"(NEW."clinicaId", NEW."profesionalId");
        END IF;
        RETURN NEW;
    ELSIF TG_OP = 'DELETE' THEN
        IF OLD."estado" = 'reservada' THEN
            PERFORM app."incrementarAgendaVersion"(OLD."clinicaId", OLD."profesionalId");
        END IF;
        RETURN OLD;
    ELSIF TG_OP = 'UPDATE' THEN
        -- La cita estaba o está reservada y cambió algún dato que afecta la
        -- agenda actual (estado, horario, profesional o clínica).
        IF (OLD."estado" = 'reservada' OR NEW."estado" = 'reservada')
           AND (
               OLD."estado" IS DISTINCT FROM NEW."estado"
               OR OLD."inicio" IS DISTINCT FROM NEW."inicio"
               OR OLD."fin" IS DISTINCT FROM NEW."fin"
               OR OLD."profesionalId" IS DISTINCT FROM NEW."profesionalId"
               OR OLD."clinicaId" IS DISTINCT FROM NEW."clinicaId"
           ) THEN
            PERFORM app."incrementarAgendaVersion"(NEW."clinicaId", NEW."profesionalId");
        END IF;

        -- Si cambió de profesional o clínica, la agenda anterior también se
        -- vio afectada (la cita dejó de ocupar o empezó a ocupar allí).
        IF (OLD."profesionalId" IS DISTINCT FROM NEW."profesionalId"
            OR OLD."clinicaId" IS DISTINCT FROM NEW."clinicaId")
           AND (OLD."estado" = 'reservada' OR NEW."estado" = 'reservada') THEN
            PERFORM app."incrementarAgendaVersion"(OLD."clinicaId", OLD."profesionalId");
        END IF;

        RETURN NEW;
    END IF;
    RETURN NULL;
END;
$$;

-- ---------------------------------------------------------------------------
-- Triggers
-- ---------------------------------------------------------------------------

DROP TRIGGER IF EXISTS "AgendaVersionProfesionalInsert" ON "Profesional";
CREATE TRIGGER "AgendaVersionProfesionalInsert"
    AFTER INSERT ON "Profesional"
    FOR EACH ROW EXECUTE FUNCTION app."crearAgendaVersionProfesional"();

DROP TRIGGER IF EXISTS "AgendaVersionProfesionalUpdate" ON "Profesional";
CREATE TRIGGER "AgendaVersionProfesionalUpdate"
    AFTER UPDATE ON "Profesional"
    FOR EACH ROW EXECUTE FUNCTION app."trgAgendaVersionProfesional"();

DROP TRIGGER IF EXISTS "AgendaVersionProfesionalServicio" ON "ProfesionalServicio";
CREATE TRIGGER "AgendaVersionProfesionalServicio"
    AFTER INSERT OR DELETE ON "ProfesionalServicio"
    FOR EACH ROW EXECUTE FUNCTION app."trgAgendaVersionProfesionalServicio"();

DROP TRIGGER IF EXISTS "AgendaVersionServicio" ON "Servicio";
CREATE TRIGGER "AgendaVersionServicio"
    AFTER UPDATE ON "Servicio"
    FOR EACH ROW EXECUTE FUNCTION app."trgAgendaVersionServicio"();

DROP TRIGGER IF EXISTS "AgendaVersionHorarioAtencion" ON "HorarioAtencion";
CREATE TRIGGER "AgendaVersionHorarioAtencion"
    AFTER INSERT OR UPDATE OR DELETE ON "HorarioAtencion"
    FOR EACH ROW EXECUTE FUNCTION app."trgAgendaVersionHorarioAtencion"();

DROP TRIGGER IF EXISTS "AgendaVersionBloqueo" ON "Bloqueo";
CREATE TRIGGER "AgendaVersionBloqueo"
    AFTER INSERT OR DELETE ON "Bloqueo"
    FOR EACH ROW EXECUTE FUNCTION app."trgAgendaVersionBloqueo"();

DROP TRIGGER IF EXISTS "AgendaVersionCita" ON "Cita";
CREATE TRIGGER "AgendaVersionCita"
    AFTER INSERT OR UPDATE OR DELETE ON "Cita"
    FOR EACH ROW EXECUTE FUNCTION app."trgAgendaVersionCita"();

-- ---------------------------------------------------------------------------
-- Registro de migración
-- ---------------------------------------------------------------------------

INSERT INTO ops."Migracion" ("version", "checksum", "exito", "detalle")
VALUES ('02_agenda_version', 'manual', true, 'versionado autoritativo de agendas')
ON CONFLICT ("version") DO NOTHING;
