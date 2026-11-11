-- =============================================================================
-- MediCitas — bench de planes de ejecución de RF7/RF8 (docs/disponibilidad.md)
--
-- Puebla un dataset sintético dentro de una transacción que se revierte al
-- final (ROLLBACK): la base queda como estaba. Los triggers de "Cita" se
-- desactivan SOLO para poblar rápido; las exclusiones I1/I2 siguen activas y
-- los datos las respetan.
--
-- Uso:  docker compose exec -T db psql -U medicitas -d medicitas < docs/bench/disponibilidad_bench.sql
-- =============================================================================

BEGIN;

ALTER TABLE "Cita" DISABLE TRIGGER "CitaPreparar";
ALTER TABLE "Cita" DISABLE TRIGGER "CitaValidarHorario";

-- Clínica sintética.
INSERT INTO "Clinica" ("id","identificador","nombre")
VALUES ('00000000-0000-4000-8000-000000000001','bench-clinica','Bench');

-- 50 profesionales (el medido es ...0004) con usuario y especialidad.
INSERT INTO "Usuario" ("id","idpSub","clinicaId","rolId")
SELECT gen_random_uuid(), 'bench-sub-' || g,
       '00000000-0000-4000-8000-000000000001',
       (SELECT "id" FROM "Rol" WHERE "codigo" = 'profesional')
FROM generate_series(1,50) g;

INSERT INTO "Profesional" ("clinicaId","usuarioId","especialidadId")
SELECT '00000000-0000-4000-8000-000000000001', u."id",
       (SELECT "id" FROM "Especialidad" LIMIT 1)
FROM "Usuario" u
WHERE u."idpSub" LIKE 'bench-sub-%';

UPDATE "Profesional" p
SET "id" = '00000000-0000-4000-8000-000000000004'
WHERE p."usuarioId" = (SELECT "id" FROM "Usuario" WHERE "idpSub" = 'bench-sub-1');

INSERT INTO "Servicio" ("id","clinicaId","nombre","duracionMinutos","especialidadId")
VALUES ('00000000-0000-4000-8000-000000000006','00000000-0000-4000-8000-000000000001','Bench 30',30,
        (SELECT "id" FROM "Especialidad" LIMIT 1));

INSERT INTO "ProfesionalServicio" ("clinicaId","profesionalId","servicioId")
SELECT '00000000-0000-4000-8000-000000000001', p."id", '00000000-0000-4000-8000-000000000006'
FROM "Profesional" p WHERE p."clinicaId" = '00000000-0000-4000-8000-000000000001';

-- Un paciente por profesional de los 10 con citas (I2).
INSERT INTO "Paciente" ("id","clinicaId","identificacion","nombre","fechaNacimiento")
SELECT gen_random_uuid(), '00000000-0000-4000-8000-000000000001', 'BENCH-' || g, 'Bench ' || g, '1990-01-01'
FROM generate_series(1,10) g;

-- 10 horarios por profesional (mañana y tarde, lunes a viernes).
INSERT INTO "HorarioAtencion" ("clinicaId","profesionalId","diaSemana","horaInicio","horaFin","vigenteDesde")
SELECT '00000000-0000-4000-8000-000000000001', p."id", d,
       CASE WHEN b = 0 THEN '08:00'::time ELSE '13:00'::time END,
       CASE WHEN b = 0 THEN '12:00'::time ELSE '17:00'::time END,
       '2025-01-01'
FROM "Profesional" p, generate_series(1,5) d, generate_series(0,1) b
WHERE p."clinicaId" = '00000000-0000-4000-8000-000000000001';

-- 52 bloqueos semanales por profesional (2.600 filas).
INSERT INTO "Bloqueo" ("clinicaId","profesionalId","inicio","fin","motivo","creadoPorUsuarioId")
SELECT '00000000-0000-4000-8000-000000000001', p."id",
       ts + (g || ' days')::interval, ts + (g || ' days')::interval + '2 hours'::interval,
       'bench', u."id"
FROM "Profesional" p
JOIN "Usuario" u ON u."id" = p."usuarioId",
     generate_series(0,357,7) g,
     (SELECT '2026-01-05 08:00:00-06'::timestamptz AS ts) t
WHERE p."clinicaId" = '00000000-0000-4000-8000-000000000001';

-- Citas: 10 profesionales × 120 días × 16 huecos de 30 min = 19.200 filas,
-- distribuidas alrededor de la fecha consultada (2026-11-16). Los datos
-- respetan I1 (sin solape por profesional) e I2 (un paciente por profesional).
INSERT INTO "Cita" ("clinicaId","pacienteId","profesionalId","servicioId","inicio","duracionMinutos","fin","estado")
SELECT '00000000-0000-4000-8000-000000000001', pac."id", p."id",
       '00000000-0000-4000-8000-000000000006',
       ts + (d || ' days')::interval + (m || ' minutes')::interval,
       30, ts + (d || ' days')::interval + ((m + 30) || ' minutes')::interval,
       'reservada'
FROM "Profesional" p
JOIN "Usuario" u ON u."id" = p."usuarioId"
JOIN LATERAL (SELECT pp."id" FROM "Paciente" pp
              WHERE pp."clinicaId" = p."clinicaId"
              ORDER BY pp."identificacion"
              OFFSET (substring(u."idpSub" from 'bench-sub-(\d+)')::int - 1) LIMIT 1) pac ON true,
     generate_series(-60,59) d, generate_series(480,930,30) m,
     (SELECT '2026-11-16 00:00:00-06'::timestamptz AS ts) t
WHERE p."clinicaId" = '00000000-0000-4000-8000-000000000001'
  AND substring(u."idpSub" from 'bench-sub-(\d+)')::int <= 10;

ANALYZE "Cita"; ANALYZE "HorarioAtencion"; ANALYZE "Bloqueo"; ANALYZE "ProfesionalServicio";

\echo '=== RF7 consulta 1: horarios del dia (HorarioAtencionProfesionalIdx) ==='
EXPLAIN (ANALYZE, BUFFERS)
SELECT h."diaSemana", h."horaInicio", h."horaFin", h."vigenteDesde"::text, h."vigenteHasta"::text
FROM "HorarioAtencion" h
JOIN "Clinica" c ON c."id" = h."clinicaId"
WHERE c."identificador" = 'bench-clinica'
  AND h."profesionalId" = '00000000-0000-4000-8000-000000000004'
  AND h."activo" = true
  AND h."vigenteDesde" <= '2026-11-16'::date
  AND (h."vigenteHasta" IS NULL OR h."vigenteHasta" >= '2026-11-16'::date);

\echo '=== RF7 consulta 2: bloqueos del dia (BloqueoProfesionalRangoIdx) ==='
EXPLAIN (ANALYZE, BUFFERS)
SELECT b."inicio", b."fin"
FROM "Bloqueo" b
JOIN "Clinica" c ON c."id" = b."clinicaId"
WHERE c."identificador" = 'bench-clinica'
  AND b."profesionalId" = '00000000-0000-4000-8000-000000000004'
  AND b."inicio" < '2026-11-17T00:00:00-06:00'::timestamptz
  AND b."fin"    > '2026-11-16T00:00:00-06:00'::timestamptz;

\echo '=== RF7 consulta 3: citas vigentes del dia (CitaProfesionalInicioIdx) ==='
EXPLAIN (ANALYZE, BUFFERS)
SELECT ci."inicio", ci."fin"
FROM "Cita" ci
JOIN "Clinica" c ON c."id" = ci."clinicaId"
WHERE c."identificador" = 'bench-clinica'
  AND ci."profesionalId" = '00000000-0000-4000-8000-000000000004'
  AND ci."estado" = 'reservada'
  AND ci."inicio" >= '2026-11-16T00:00:00-06:00'::timestamptz
  AND ci."inicio" <  '2026-11-17T00:00:00-06:00'::timestamptz;

\echo '=== RF8: profesionales habilitados para un servicio (ProfesionalServicioServicioIdx) ==='
EXPLAIN (ANALYZE, BUFFERS)
SELECT p."id"::text, u."idpSub", e."nombre", p."activo"
FROM "ProfesionalServicio" ps
JOIN "Clinica" c      ON c."id" = ps."clinicaId"
JOIN "Profesional" p  ON p."id" = ps."profesionalId" AND p."clinicaId" = ps."clinicaId"
JOIN "Usuario" u      ON u."id" = p."usuarioId" AND u."clinicaId" = p."clinicaId"
JOIN "Especialidad" e ON e."id" = p."especialidadId"
WHERE c."identificador" = 'bench-clinica'
  AND ps."servicioId" = '00000000-0000-4000-8000-000000000006'
  AND p."activo" = true;

ROLLBACK;
