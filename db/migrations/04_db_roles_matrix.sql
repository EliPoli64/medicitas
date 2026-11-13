-- =============================================================================
-- MediCitas — Migración 04: perfiles SQL de menor privilegio
--
-- medicitas_app sigue siendo el LOGIN de la API (NOBYPASSRLS), pero NOINHERIT:
-- no hereda los privilegios de los perfiles. Tras resolver "Usuario", la API
-- hace SET ROLE al perfil alineado con la matriz:
--
--   adm_plataforma → medicitas_adm_plataforma   (RF1)
--   adm_clinica    → medicitas_adm_clinica
--   recepcion      → medicitas_recepcion
--   profesional    → medicitas_profesional
--
-- "Auditoria" no recibe UPDATE ni DELETE (I6), además del trigger append-only.
-- "AgendaVersion" no se actualiza con GRANT: lo hacen funciones SECURITY DEFINER.
-- Esta migración también reaplica esas funciones y los triggers de Bloqueo /
-- HorarioAtencion, porque 02_agenda_version ya puede estar aplicada.
-- =============================================================================

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'medicitas_recepcion') THEN
        CREATE ROLE medicitas_recepcion NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'medicitas_profesional') THEN
        CREATE ROLE medicitas_profesional NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'medicitas_adm_clinica') THEN
        CREATE ROLE medicitas_adm_clinica NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'medicitas_adm_plataforma') THEN
        CREATE ROLE medicitas_adm_plataforma NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
    END IF;
END
$$;

ALTER ROLE medicitas_recepcion WITH NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOINHERIT;
ALTER ROLE medicitas_profesional WITH NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOINHERIT;
ALTER ROLE medicitas_adm_clinica WITH NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOINHERIT;
ALTER ROLE medicitas_adm_plataforma WITH NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOINHERIT;

-- Sin INHERIT, ser miembro solo autoriza SET ROLE. El CRUD amplio de 03_rls
-- deja de valer para la conexión de la API.
ALTER ROLE medicitas_app NOINHERIT;

GRANT medicitas_recepcion TO medicitas_app;
GRANT medicitas_profesional TO medicitas_app;
GRANT medicitas_adm_clinica TO medicitas_app;
GRANT medicitas_adm_plataforma TO medicitas_app;

REVOKE ALL ON TABLE
    "Clinica",
    "Usuario",
    "Profesional",
    "Servicio",
    "ProfesionalServicio",
    "HorarioAtencion",
    "Bloqueo",
    "Paciente",
    "ContactoPaciente",
    "Cita",
    "NotaClinica",
    "Auditoria",
    "AgendaVersion"
FROM medicitas_app;

-- Bootstrap de loadDomainUser, antes de SET ROLE.
GRANT USAGE ON SCHEMA public, app TO medicitas_app;
GRANT SELECT ON TABLE "Rol", "Usuario", "Clinica" TO medicitas_app;
GRANT EXECUTE ON FUNCTION app.tenant_visible(uuid) TO medicitas_app;

GRANT USAGE ON SCHEMA public, app TO
    medicitas_recepcion,
    medicitas_profesional,
    medicitas_adm_clinica,
    medicitas_adm_plataforma;

GRANT EXECUTE ON FUNCTION app.tenant_visible(uuid) TO
    medicitas_recepcion,
    medicitas_profesional,
    medicitas_adm_clinica,
    medicitas_adm_plataforma;

GRANT SELECT ON TABLE "Rol", "TipoContacto", "Especialidad" TO
    medicitas_recepcion,
    medicitas_profesional,
    medicitas_adm_clinica;
GRANT SELECT ON TABLE "Rol" TO medicitas_adm_plataforma;

-- Lectura de tenant compartida por los tres perfiles clínicos.
GRANT SELECT ON TABLE
    "Clinica",
    "Usuario",
    "Profesional",
    "Servicio",
    "ProfesionalServicio",
    "HorarioAtencion",
    "Bloqueo",
    "Paciente",
    "ContactoPaciente",
    "Cita",
    "AgendaVersion"
TO medicitas_recepcion, medicitas_profesional, medicitas_adm_clinica;

-- Adm. plataforma: solo alta de clínica y de su primer administrador (RF1).
GRANT SELECT, INSERT ON TABLE "Clinica", "Usuario" TO medicitas_adm_plataforma;

-- Adm. clínica: catálogo, agenda, pacientes, citas y bitácora.
GRANT INSERT, UPDATE, DELETE ON TABLE
    "Usuario",
    "Profesional",
    "Servicio",
    "ProfesionalServicio",
    "HorarioAtencion",
    "Paciente",
    "ContactoPaciente"
TO medicitas_adm_clinica;
GRANT INSERT, UPDATE ON TABLE "Cita" TO medicitas_adm_clinica;
GRANT INSERT, DELETE ON TABLE "Bloqueo" TO medicitas_adm_clinica;
GRANT SELECT ON TABLE "NotaClinica" TO medicitas_adm_clinica;
GRANT SELECT, INSERT ON TABLE "Auditoria" TO medicitas_adm_clinica;

-- Recepción: pacientes, citas y auditoría de esas escrituras. Sin catálogo ni bitácora.
GRANT INSERT, UPDATE ON TABLE "Paciente", "ContactoPaciente", "Cita" TO medicitas_recepcion;
GRANT SELECT ON TABLE "NotaClinica" TO medicitas_recepcion;
GRANT INSERT ON TABLE "Auditoria" TO medicitas_recepcion;

-- Profesional: su agenda, desenlace, notas. Pacientes solo lectura (ya concedida).
GRANT INSERT, UPDATE, DELETE ON TABLE "HorarioAtencion" TO medicitas_profesional;
GRANT INSERT, DELETE ON TABLE "Bloqueo" TO medicitas_profesional;
GRANT UPDATE ON TABLE "Cita" TO medicitas_profesional;
GRANT SELECT, INSERT ON TABLE "NotaClinica" TO medicitas_profesional;
GRANT INSERT ON TABLE "Auditoria" TO medicitas_profesional;

GRANT EXECUTE ON FUNCTION app."reprogramarCita"(uuid, uuid, timestamptz, uuid, uuid)
    TO medicitas_recepcion, medicitas_adm_clinica;

-- ---------------------------------------------------------------------------
-- AgendaVersion: UPSERT + triggers que cubren UPDATE directo
-- (reaplicado aquí porque 02 puede estar ya registrada en ops."Migracion")
-- ---------------------------------------------------------------------------

CREATE OR REPLACE FUNCTION app."crearAgendaVersionProfesional"()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
    INSERT INTO "AgendaVersion" ("clinicaId", "profesionalId", "version")
    VALUES (NEW."clinicaId", NEW."id", 0)
    ON CONFLICT ("clinicaId", "profesionalId") DO NOTHING;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION app."incrementarAgendaVersion"(
    pClinicaId uuid,
    pProfesionalId uuid
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
    INSERT INTO "AgendaVersion" ("clinicaId", "profesionalId", "version")
    VALUES (pClinicaId, pProfesionalId, 1)
    ON CONFLICT ("clinicaId", "profesionalId")
    DO UPDATE SET
        "version" = "AgendaVersion"."version" + 1,
        "actualizadoEn" = now();
END;
$$;

CREATE OR REPLACE FUNCTION app."incrementarAgendaVersionPorServicio"(
    pClinicaId uuid,
    pServicioId uuid
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
    INSERT INTO "AgendaVersion" ("clinicaId", "profesionalId", "version")
    SELECT ps."clinicaId", ps."profesionalId", 1
    FROM "ProfesionalServicio" ps
    WHERE ps."clinicaId" = pClinicaId
      AND ps."servicioId" = pServicioId
    ON CONFLICT ("clinicaId", "profesionalId")
    DO UPDATE SET
        "version" = "AgendaVersion"."version" + 1,
        "actualizadoEn" = now();
END;
$$;

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
           OR NEW."activo" IS DISTINCT FROM OLD."activo"
           OR NEW."profesionalId" IS DISTINCT FROM OLD."profesionalId"
           OR NEW."clinicaId" IS DISTINCT FROM OLD."clinicaId" THEN
            vRelevante := true;
        END IF;
    END IF;

    IF vRelevante THEN
        PERFORM app."incrementarAgendaVersion"(vClinicaId, vProfesionalId);
    END IF;
    IF TG_OP = 'UPDATE'
       AND (NEW."profesionalId" IS DISTINCT FROM OLD."profesionalId"
            OR NEW."clinicaId" IS DISTINCT FROM OLD."clinicaId") THEN
        PERFORM app."incrementarAgendaVersion"(OLD."clinicaId", OLD."profesionalId");
    END IF;
    RETURN COALESCE(NEW, OLD);
END;
$$;

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
    ELSIF TG_OP = 'UPDATE' THEN
        PERFORM app."incrementarAgendaVersion"(NEW."clinicaId", NEW."profesionalId");
        IF NEW."profesionalId" IS DISTINCT FROM OLD."profesionalId"
           OR NEW."clinicaId" IS DISTINCT FROM OLD."clinicaId" THEN
            PERFORM app."incrementarAgendaVersion"(OLD."clinicaId", OLD."profesionalId");
        END IF;
        RETURN NEW;
    ELSE
        RETURN COALESCE(NEW, OLD);
    END IF;

    PERFORM app."incrementarAgendaVersion"(vClinicaId, vProfesionalId);
    RETURN COALESCE(NEW, OLD);
END;
$$;

DROP TRIGGER IF EXISTS "AgendaVersionBloqueo" ON "Bloqueo";
CREATE TRIGGER "AgendaVersionBloqueo"
    AFTER INSERT OR UPDATE OR DELETE ON "Bloqueo"
    FOR EACH ROW EXECUTE FUNCTION app."trgAgendaVersionBloqueo"();

INSERT INTO ops."Migracion" ("version", "checksum", "exito", "detalle")
VALUES ('04_db_roles_matrix', 'manual', true, 'perfiles SQL de la matriz y triggers de versión')
ON CONFLICT ("version") DO NOTHING;
