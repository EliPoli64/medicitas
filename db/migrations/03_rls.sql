-- =============================================================================
-- MediCitas — Migración 03: RLS como segunda estrategia de multi-tenancy
--
-- La estrategia principal sigue siendo la columna "clinicaId" filtrada en la
-- API. Esta migración enciende Row-Level Security encima, para que el motor
-- oculte filas de otra clínica aunque un SELECT se olvide el WHERE.
--
-- POSTGRES_USER es superusuario y SIEMPRE se salta RLS, incluso con
-- FORCE ROW LEVEL SECURITY. Por eso la API no entra con ese usuario:
-- entra como medicitas_app (NOSUPERUSER, NOBYPASSRLS). Migraciones y seed
-- siguen como superusuario para poder cargar datos sin política.
--
-- Contraseña local de medicitas_app: medicitas_app
-- (APP_DB_USER / APP_DB_PASSWORD en Compose). No es un secreto de producción.
-- =============================================================================

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'medicitas_app') THEN
        CREATE ROLE medicitas_app
            LOGIN
            NOSUPERUSER
            NOBYPASSRLS
            NOCREATEDB
            NOCREATEROLE
            PASSWORD 'medicitas_app';
    END IF;
END
$$;

ALTER ROLE medicitas_app WITH
    LOGIN
    NOSUPERUSER
    NOBYPASSRLS
    NOCREATEDB
    NOCREATEROLE
    PASSWORD 'medicitas_app';

-- Verdadero si la fila pertenece a la clínica de la sesión, o si la sesión
-- es administrador de plataforma (ve todas las clínicas).
CREATE OR REPLACE FUNCTION app.tenant_visible(p_clinica uuid)
RETURNS boolean
LANGUAGE sql
STABLE
PARALLEL SAFE
AS $$
    SELECT current_setting('app.rol', true) = 'adm_plataforma'
        OR (
            p_clinica IS NOT NULL
            AND p_clinica::text = NULLIF(current_setting('app.clinica_id', true), '')
        );
$$;

-- Catálogos globales: sin RLS. El rol de la API solo los lee.
GRANT USAGE ON SCHEMA public, app TO medicitas_app;
GRANT SELECT ON TABLE "Rol", "TipoContacto", "Especialidad" TO medicitas_app;
GRANT EXECUTE ON FUNCTION app.tenant_visible(uuid) TO medicitas_app;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA app TO medicitas_app;

GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE
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
TO medicitas_app;

GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO medicitas_app;

-- ---------------------------------------------------------------------------
-- Políticas. USING y WITH CHECK coinciden, salvo el arranque de sesión:
-- "Usuario" deja ver la fila propia por idpSub antes de conocer la clínica,
-- y "Clinica" deja ver la clínica de ese usuario para el JOIN de /me.
-- WITH CHECK no incluye idpSub: no se puede insertar en otra clínica
-- copiando el propio subject.
-- ---------------------------------------------------------------------------

ALTER TABLE "Clinica" ENABLE ROW LEVEL SECURITY;
ALTER TABLE "Clinica" FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS "ClinicaTenant" ON "Clinica";
CREATE POLICY "ClinicaTenant" ON "Clinica"
    FOR ALL
    USING (
        app.tenant_visible("id")
        OR "id" IN (
            SELECT u."clinicaId"
            FROM "Usuario" u
            WHERE u."idpSub" = NULLIF(current_setting('app.idp_sub', true), '')
              AND u."clinicaId" IS NOT NULL
        )
    )
    WITH CHECK (app.tenant_visible("id"));

ALTER TABLE "Usuario" ENABLE ROW LEVEL SECURITY;
ALTER TABLE "Usuario" FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS "UsuarioTenant" ON "Usuario";
CREATE POLICY "UsuarioTenant" ON "Usuario"
    FOR ALL
    USING (
        app.tenant_visible("clinicaId")
        OR "idpSub" = NULLIF(current_setting('app.idp_sub', true), '')
    )
    WITH CHECK (app.tenant_visible("clinicaId"));

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'Profesional',
        'Servicio',
        'ProfesionalServicio',
        'HorarioAtencion',
        'Bloqueo',
        'Paciente',
        'ContactoPaciente',
        'Cita',
        'NotaClinica',
        'Auditoria',
        'AgendaVersion'
    ]
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || 'Tenant', t);
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR ALL USING (app.tenant_visible("clinicaId")) WITH CHECK (app.tenant_visible("clinicaId"))',
            t || 'Tenant',
            t
        );
    END LOOP;
END
$$;

INSERT INTO ops."Migracion" ("version", "checksum", "exito", "detalle")
VALUES ('03_rls', 'manual', true, 'RLS por clinicaId, rol medicitas_app')
ON CONFLICT ("version") DO NOTHING;
