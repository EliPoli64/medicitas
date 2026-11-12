#!/usr/bin/env python3
"""Job de semilla de MediCitas.

Carga datos de demostración relativos a la fecha de ejecución y crea/verifica
los usuarios de Keycloak definidos por el realm importado desde id-provider/.
"""

from __future__ import annotations

import os
import sys
import uuid
from datetime import date, datetime, time, timedelta
from typing import Any

import psycopg
from psycopg.types.json import Jsonb

from catalog import (
    CLINICS,
    KEYCLOAK_ROLE_BY_DB,
    PATIENTS,
    PROFESSIONALS,
    SPECIALTIES,
    USERS,
    local_start,
    local_today,
    relative_weekday,
)
from keycloak_admin import KeycloakAdmin, KeycloakError
from migrations import apply_migrations

SEED_LOCK_KEY = "medicitas-seed"


def seed_uuid(kind: str, *parts: str) -> str:
    key = "/".join((kind, *parts))
    return str(uuid.uuid5(uuid.NAMESPACE_URL, f"https://medicitas.local/seed/{key}"))


def _required_env(name: str) -> str:
    value = os.getenv(name)
    if value is None or value == "":
        raise RuntimeError(f"Falta la variable de entorno {name}")
    return value


def _connect() -> psycopg.Connection:
    return psycopg.connect(
        host=_required_env("DB_HOST"),
        port=int(os.getenv("DB_PORT", "5432")),
        user=_required_env("DB_USER"),
        password=_required_env("DB_PASSWORD"),
        dbname=_required_env("DB_NAME"),
        connect_timeout=10,
        application_name="medicitas-seed",
        autocommit=True,
    )


def _get_id(cur: psycopg.Cursor, query: str, params: tuple[Any, ...], label: str) -> uuid.UUID:
    cur.execute(query, params)
    row = cur.fetchone()
    if row is None:
        raise RuntimeError(f"No se encontró {label}")
    return row[0]


def _insert_cita(
    cur: psycopg.Cursor,
    *,
    clinic_id: uuid.UUID,
    patient_id: uuid.UUID,
    professional_id: uuid.UUID,
    service_id: uuid.UUID,
    start_local: datetime,
    key: str,
) -> tuple[uuid.UUID, str]:
    cita_id = uuid.UUID(seed_uuid("cita", str(clinic_id), key))
    cur.execute(
        """
        INSERT INTO "Cita" (
            "id", "clinicaId", "pacienteId", "profesionalId", "servicioId", "inicio"
        )
        VALUES (%s, %s, %s, %s, %s, %s)
        ON CONFLICT ("id") DO NOTHING
        """,
        (cita_id, clinic_id, patient_id, professional_id, service_id, start_local),
    )
    cur.execute('SELECT "estado" FROM "Cita" WHERE "id" = %s', (cita_id,))
    row = cur.fetchone()
    if row is None:
        raise RuntimeError(f"No se pudo leer la cita {key}")
    return cita_id, row[0]


def _set_cita_state(
    cur: psycopg.Cursor,
    cita_id: uuid.UUID,
    state: str,
    *,
    cancel_user_id: uuid.UUID | None = None,
    cancel_reason: str | None = None,
) -> None:
    cur.execute('SELECT "estado" FROM "Cita" WHERE "id" = %s', (cita_id,))
    row = cur.fetchone()
    if row is None:
        raise RuntimeError(f"No existe la cita {cita_id}")
    current = row[0]

    if current == state:
        return

    if current != "reservada":
        raise RuntimeError(
            f"La cita {cita_id} está en estado {current}; no se puede pasar a {state}"
        )

    if state == "cancelada":
        if cancel_user_id is None or not cancel_reason:
            raise RuntimeError("La cancelación requiere usuario y motivo")
        cur.execute(
            """
            UPDATE "Cita"
            SET "estado" = 'cancelada',
                "canceladaEn" = now(),
                "canceladaPorUsuarioId" = %s,
                "motivoCancelacion" = %s
            WHERE "id" = %s
            """,
            (cancel_user_id, cancel_reason, cita_id),
        )
        return

    if state in ("atendida", "ausente"):
        cur.execute(
            'UPDATE "Cita" SET "estado" = %s WHERE "id" = %s',
            (state, cita_id),
        )
        return

    raise RuntimeError(f"Estado de cita no soportado por el seed: {state}")


def _insert_nota(
    cur: psycopg.Cursor,
    *,
    clinic_id: uuid.UUID,
    cita_id: uuid.UUID,
    professional_id: uuid.UUID,
    contenido: str,
) -> None:
    nota_id = uuid.UUID(seed_uuid("nota", str(cita_id)))
    cur.execute(
        """
        INSERT INTO "NotaClinica" (
            "id", "clinicaId", "citaId", "profesionalId", "contenido"
        )
        VALUES (%s, %s, %s, %s, %s)
        ON CONFLICT ("clinicaId", "citaId") DO NOTHING
        """,
        (nota_id, clinic_id, cita_id, professional_id, contenido),
    )


def _reprogramar(
    cur: psycopg.Cursor,
    *,
    origin_id: uuid.UUID,
    clinic_id: uuid.UUID,
    new_start: datetime,
) -> None:
    cur.execute('SELECT "estado" FROM "Cita" WHERE "id" = %s', (origin_id,))
    row = cur.fetchone()
    if row is None:
        raise RuntimeError(f"No existe la cita origen {origin_id}")

    if row[0] == "reprogramada":
        return
    if row[0] != "reservada":
        raise RuntimeError(
            f"La cita origen {origin_id} está en estado {row[0]} y no se puede reprogramar"
        )

    cur.execute(
        'SELECT app."reprogramarCita"(%s, %s, %s)',
        (origin_id, clinic_id, new_start),
    )


def _seed_appointments(
    cur: psycopg.Cursor,
    *,
    clinic_ids: dict[str, uuid.UUID],
    professional_ids: dict[tuple[uuid.UUID, str], uuid.UUID],
    service_ids: dict[tuple[uuid.UUID, str], uuid.UUID],
    patient_ids: dict[tuple[uuid.UUID, str], uuid.UUID],
    user_ids: dict[str, uuid.UUID],
    base: date,
) -> None:
    for clinic in CLINICS:
        clinic_id = clinic_ids[clinic.identificador]
        clinic_profs = [p for p in PROFESSIONALS if p.clinic == clinic.identificador]
        clinic_pats = [p for p in PATIENTS if p.clinic == clinic.identificador]

        if len(clinic_profs) != 2:
            raise RuntimeError(f"{clinic.identificador} debe tener dos profesionales")
        if len(clinic_pats) != 4:
            raise RuntimeError(f"{clinic.identificador} debe tener cuatro pacientes")

        p0, p1 = clinic_profs
        prof0 = professional_ids[(clinic_id, p0.username)]
        prof1 = professional_ids[(clinic_id, p1.username)]
        serv0 = service_ids[(clinic_id, p0.specialty)]
        serv1 = service_ids[(clinic_id, p1.specialty)]

        def patient_id(index: int) -> uuid.UUID:
            return patient_ids[(clinic_id, clinic_pats[index].key)]

        def start(offset: int, hour: int, minute: int = 0) -> datetime:
            return local_start(relative_weekday(base, offset), hour, minute)

        reception_username = "recep-" + clinic.identificador.removeprefix("clinica-")
        reception_id = user_ids[reception_username]

        appointments: list[dict[str, Any]] = [
            {
                "key": "atendida-1",
                "professional_id": prof0,
                "service_id": serv0,
                "patient_id": patient_id(0),
                "start": start(-7, 9),
                "state": "atendida",
                "note": "Paciente estable. Se revisan signos vitales y se refuerzan recomendaciones.",
            },
            {
                "key": "atendida-2",
                "professional_id": prof1,
                "service_id": serv1,
                "patient_id": patient_id(1),
                "start": start(-14, 10),
                "state": "atendida",
                "note": "Consulta de seguimiento. Evolución favorable y sin signos de alarma.",
            },
            {
                "key": "atendida-3",
                "professional_id": prof0,
                "service_id": serv0,
                "patient_id": patient_id(2),
                "start": start(-21, 9, 30),
                "state": "atendida",
                "note": "Se completa valoración inicial y se indica control en tres meses.",
            },
            {
                "key": "ausente-1",
                "professional_id": prof1,
                "service_id": serv1,
                "patient_id": patient_id(3),
                "start": start(-3, 9),
                "state": "ausente",
            },
            {
                "key": "cancelada-1",
                "professional_id": prof0,
                "service_id": serv0,
                "patient_id": patient_id(1),
                "start": start(-5, 11),
                "state": "cancelada",
                "cancel_reason": "Paciente solicitó cancelar por viaje.",
            },
            {
                "key": "futura-1",
                "professional_id": prof0,
                "service_id": serv0,
                "patient_id": patient_id(2),
                "start": start(1, 9),
                "state": "reservada",
            },
            {
                "key": "futura-2",
                "professional_id": prof1,
                "service_id": serv1,
                "patient_id": patient_id(3),
                "start": start(3, 10),
                "state": "reservada",
            },
            {
                "key": "futura-3",
                "professional_id": prof0,
                "service_id": serv0,
                "patient_id": patient_id(0),
                "start": start(5, 10),
                "state": "reservada",
            },
        ]

        for appointment in appointments:
            cita_id, _ = _insert_cita(
                cur,
                clinic_id=clinic_id,
                patient_id=appointment["patient_id"],
                professional_id=appointment["professional_id"],
                service_id=appointment["service_id"],
                start_local=appointment["start"],
                key=f"{clinic.identificador}-{appointment['key']}",
            )
            state = appointment["state"]
            _set_cita_state(
                cur,
                cita_id,
                state,
                cancel_user_id=reception_id,
                cancel_reason=appointment.get("cancel_reason"),
            )
            if state == "atendida":
                _insert_nota(
                    cur,
                    clinic_id=clinic_id,
                    cita_id=cita_id,
                    professional_id=appointment["professional_id"],
                    contenido=appointment["note"],
                )

        origin_id, _ = _insert_cita(
            cur,
            clinic_id=clinic_id,
            patient_id=patient_id(1),
            professional_id=prof1,
            service_id=serv1,
            start_local=start(7, 14),
            key=f"{clinic.identificador}-reprogramada-origen",
        )
        _reprogramar(
            cur,
            origin_id=origin_id,
            clinic_id=clinic_id,
            new_start=start(7, 15),
        )


def seed_database(
    conn: psycopg.Connection,
    *,
    keycloak_ids: dict[str, str],
    version: str,
) -> None:
    today = local_today()
    schedule_from = today - timedelta(days=180)
    schedule_to = today + timedelta(days=365)

    with conn.transaction():
        with conn.cursor() as cur:
            cur.execute(
                'SELECT 1 FROM ops."Semilla" WHERE "version" = %s',
                (version,),
            )
            if cur.fetchone() is not None:
                raise RuntimeError(f"La semilla {version} ya está aplicada")

            cur.execute('SELECT "codigo", "id" FROM "Rol"')
            role_ids = dict(cur.fetchall())
            missing_roles = set(KEYCLOAK_ROLE_BY_DB) - set(role_ids)
            if missing_roles:
                raise RuntimeError(f"Faltan roles en la base: {sorted(missing_roles)}")

            cur.execute('SELECT "codigo", "id" FROM "TipoContacto"')
            contact_type_ids = dict(cur.fetchall())
            for code in ("telefono", "email"):
                if code not in contact_type_ids:
                    raise RuntimeError(f"Falta el tipo de contacto {code}")

            specialty_ids: dict[str, uuid.UUID] = {}
            for specialty in SPECIALTIES:
                specialty_id = uuid.UUID(seed_uuid("especialidad", specialty.codigo))
                cur.execute(
                    """
                    INSERT INTO "Especialidad" ("id", "codigo", "nombre")
                    VALUES (%s, %s, %s)
                    ON CONFLICT ("codigo") DO NOTHING
                    """,
                    (specialty_id, specialty.codigo, specialty.nombre),
                )
                specialty_ids[specialty.codigo] = _get_id(
                    cur,
                    'SELECT "id" FROM "Especialidad" WHERE "codigo" = %s',
                    (specialty.codigo,),
                    f"la especialidad {specialty.codigo}",
                )

            clinic_ids: dict[str, uuid.UUID] = {}
            for clinic in CLINICS:
                clinic_id = uuid.UUID(seed_uuid("clinica", clinic.identificador))
                cur.execute(
                    """
                    INSERT INTO "Clinica" ("id", "identificador", "nombre")
                    VALUES (%s, %s, %s)
                    ON CONFLICT ("identificador") DO NOTHING
                    """,
                    (clinic_id, clinic.identificador, clinic.nombre),
                )
                clinic_ids[clinic.identificador] = _get_id(
                    cur,
                    'SELECT "id" FROM "Clinica" WHERE "identificador" = %s',
                    (clinic.identificador,),
                    f"la clínica {clinic.identificador}",
                )

            user_ids: dict[str, uuid.UUID] = {}
            for user in USERS:
                clinic_id = clinic_ids[user.clinic] if user.clinic else None
                role_id = role_ids[user.db_role]
                idp_sub = keycloak_ids[user.username]
                user_id = uuid.UUID(seed_uuid("usuario", user.username))
                cur.execute(
                    """
                    INSERT INTO "Usuario" ("id", "idpSub", "clinicaId", "rolId")
                    VALUES (%s, %s, %s, %s)
                    ON CONFLICT ("idpSub") DO UPDATE
                    SET "clinicaId" = EXCLUDED."clinicaId",
                        "rolId" = EXCLUDED."rolId",
                        "activo" = true,
                        "actualizadoEn" = now()
                    """,
                    (user_id, idp_sub, clinic_id, role_id),
                )
                user_ids[user.username] = _get_id(
                    cur,
                    'SELECT "id" FROM "Usuario" WHERE "idpSub" = %s',
                    (idp_sub,),
                    f"el usuario {user.username}",
                )

            professional_ids: dict[tuple[uuid.UUID, str], uuid.UUID] = {}
            for professional in PROFESSIONALS:
                clinic_id = clinic_ids[professional.clinic]
                user_id = user_ids[professional.username]
                specialty_id = specialty_ids[professional.specialty]
                professional_id = uuid.UUID(
                    seed_uuid("profesional", professional.clinic, professional.username)
                )
                cur.execute(
                    """
                    INSERT INTO "Profesional" (
                        "id", "clinicaId", "usuarioId", "especialidadId"
                    )
                    VALUES (%s, %s, %s, %s)
                    ON CONFLICT ("clinicaId", "usuarioId") DO UPDATE
                    SET "especialidadId" = EXCLUDED."especialidadId",
                        "activo" = true,
                        "actualizadoEn" = now()
                    """,
                    (professional_id, clinic_id, user_id, specialty_id),
                )
                professional_ids[(clinic_id, professional.username)] = _get_id(
                    cur,
                    """
                    SELECT "id" FROM "Profesional"
                    WHERE "clinicaId" = %s AND "usuarioId" = %s
                    """,
                    (clinic_id, user_id),
                    f"el profesional {professional.username}",
                )

            service_ids: dict[tuple[uuid.UUID, str], uuid.UUID] = {}
            for professional in PROFESSIONALS:
                clinic_id = clinic_ids[professional.clinic]
                service_key = (clinic_id, professional.specialty)
                if service_key in service_ids:
                    continue

                service_id = uuid.UUID(
                    seed_uuid("servicio", professional.clinic, professional.specialty)
                )
                cur.execute(
                    """
                    INSERT INTO "Servicio" (
                        "id", "clinicaId", "nombre", "duracionMinutos", "especialidadId"
                    )
                    VALUES (%s, %s, %s, %s, %s)
                    ON CONFLICT ("id") DO UPDATE
                    SET "nombre" = EXCLUDED."nombre",
                        "duracionMinutos" = EXCLUDED."duracionMinutos",
                        "especialidadId" = EXCLUDED."especialidadId",
                        "activo" = true,
                        "actualizadoEn" = now()
                    """,
                    (
                        service_id,
                        clinic_id,
                        professional.service_name,
                        professional.service_duration,
                        specialty_ids[professional.specialty],
                    ),
                )
                service_ids[service_key] = service_id

            for professional in PROFESSIONALS:
                clinic_id = clinic_ids[professional.clinic]
                professional_id = professional_ids[(clinic_id, professional.username)]
                service_id = service_ids[(clinic_id, professional.specialty)]
                cur.execute(
                    """
                    INSERT INTO "ProfesionalServicio" (
                        "clinicaId", "profesionalId", "servicioId"
                    )
                    VALUES (%s, %s, %s)
                    ON CONFLICT DO NOTHING
                    """,
                    (clinic_id, professional_id, service_id),
                )

            for professional in PROFESSIONALS:
                clinic_id = clinic_ids[professional.clinic]
                professional_id = professional_ids[(clinic_id, professional.username)]
                for weekday in range(1, 6):
                    for start_time, end_time in (
                        (time(9, 0), time(12, 0)),
                        (time(13, 0), time(17, 0)),
                    ):
                        schedule_id = uuid.UUID(
                            seed_uuid(
                                "horario",
                                professional.clinic,
                                professional.username,
                                str(weekday),
                                start_time.isoformat(timespec="minutes"),
                            )
                        )
                        cur.execute(
                            """
                            INSERT INTO "HorarioAtencion" (
                                "id", "clinicaId", "profesionalId", "diaSemana",
                                "horaInicio", "horaFin", "vigenteDesde", "vigenteHasta"
                            )
                            VALUES (%s, %s, %s, %s, %s, %s, %s, %s)
                            ON CONFLICT ("id") DO NOTHING
                            """,
                            (
                                schedule_id,
                                clinic_id,
                                professional_id,
                                weekday,
                                start_time,
                                end_time,
                                schedule_from,
                                schedule_to,
                            ),
                        )

            patient_ids: dict[tuple[uuid.UUID, str], uuid.UUID] = {}
            for index, patient in enumerate(PATIENTS, start=1):
                clinic_id = clinic_ids[patient.clinic]
                patient_id = uuid.UUID(
                    seed_uuid("paciente", patient.clinic, patient.key)
                )
                cur.execute(
                    """
                    INSERT INTO "Paciente" (
                        "id", "clinicaId", "identificacion", "nombre", "fechaNacimiento"
                    )
                    VALUES (%s, %s, %s, %s, %s)
                    ON CONFLICT ("clinicaId", "identificacion") DO UPDATE
                    SET "nombre" = EXCLUDED."nombre",
                        "fechaNacimiento" = EXCLUDED."fechaNacimiento",
                        "activo" = true,
                        "actualizadoEn" = now()
                    """,
                    (
                        patient_id,
                        clinic_id,
                        patient.identificacion,
                        patient.nombre,
                        patient.fecha_nacimiento,
                    ),
                )
                patient_ids[(clinic_id, patient.key)] = _get_id(
                    cur,
                    """
                    SELECT "id" FROM "Paciente"
                    WHERE "clinicaId" = %s AND "identificacion" = %s
                    """,
                    (clinic_id, patient.identificacion),
                    f"el paciente {patient.identificacion}",
                )

                phone_id = uuid.UUID(
                    seed_uuid("contacto", patient.clinic, patient.key, "telefono")
                )
                email_id = uuid.UUID(
                    seed_uuid("contacto", patient.clinic, patient.key, "email")
                )
                cur.execute(
                    """
                    INSERT INTO "ContactoPaciente" (
                        "id", "clinicaId", "pacienteId", "tipoContactoId",
                        "valor", "esPrincipal"
                    )
                    VALUES (%s, %s, %s, %s, %s, true)
                    ON CONFLICT ("id") DO NOTHING
                    """,
                    (
                        phone_id,
                        clinic_id,
                        patient_ids[(clinic_id, patient.key)],
                        contact_type_ids["telefono"],
                        f"+506 8000 {index:04d}",
                    ),
                )
                cur.execute(
                    """
                    INSERT INTO "ContactoPaciente" (
                        "id", "clinicaId", "pacienteId", "tipoContactoId",
                        "valor", "esPrincipal"
                    )
                    VALUES (%s, %s, %s, %s, %s, false)
                    ON CONFLICT ("id") DO NOTHING
                    """,
                    (
                        email_id,
                        clinic_id,
                        patient_ids[(clinic_id, patient.key)],
                        contact_type_ids["email"],
                        f"{patient.key}@example.test",
                    ),
                )

            _seed_appointments(
                cur,
                clinic_ids=clinic_ids,
                professional_ids=professional_ids,
                service_ids=service_ids,
                patient_ids=patient_ids,
                user_ids=user_ids,
                base=today,
            )

            detail = {
                "clinicas": len(CLINICS),
                "usuarios": len(USERS),
                "profesionales": len(PROFESSIONALS),
                "pacientes": len(PATIENTS),
                "fechaBase": today.isoformat(),
                "keycloakUsuarios": len(keycloak_ids),
            }
            cur.execute(
                """
                INSERT INTO ops."Semilla" ("version", "detalle")
                VALUES (%s, %s)
                """,
                (version, Jsonb(detail)),
            )


def main() -> int:
    version = os.getenv("SEED_VERSION", "demo-v1")

    conn = _connect()
    try:
        conn.execute(
            "SELECT pg_advisory_lock(hashtext(%s))",
            (SEED_LOCK_KEY,),
        )

        # Aplicar migraciones SQL pendientes antes de sembrar. Esto permite
        # actualizar bases de datos existentes sin recrear el volumen.
        applied = apply_migrations(conn)
        if applied:
            print(f"Migraciones aplicadas: {', '.join(applied)}")

        with conn.cursor() as cur:
            cur.execute(
                'SELECT 1 FROM ops."Semilla" WHERE "version" = %s',
                (version,),
            )
            if cur.fetchone() is not None:
                print(f"Semilla {version} ya aplicada; no hay nada que hacer.")
                return 0

        user_password = _required_env("SEED_USER_PASSWORD")
        keycloak = KeycloakAdmin(
            base_url=_required_env("KC_URL"),
            realm=_required_env("KC_REALM"),
            admin_username=_required_env("KC_ADMIN_USERNAME"),
            admin_password=_required_env("KC_ADMIN_PASSWORD"),
        )
        keycloak.verify_realm()
        keycloak.ensure_clinica_attribute()

        keycloak_ids: dict[str, str] = {}
        for user in USERS:
            keycloak_ids[user.username] = keycloak.ensure_user(user, user_password)
            print(f"Keycloak OK: {user.username}")

        seed_database(conn, keycloak_ids=keycloak_ids, version=version)
        print(
            f"Semilla {version} aplicada: {len(CLINICS)} clínicas, "
            f"{len(USERS)} usuarios, {len(PROFESSIONALS)} profesionales, "
            f"{len(PATIENTS)} pacientes."
        )
        return 0
    finally:
        try:
            conn.execute(
                "SELECT pg_advisory_unlock(hashtext(%s))",
                (SEED_LOCK_KEY,),
            )
        except psycopg.Error:
            pass
        conn.close()


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except psycopg.errors.UndefinedTable as exc:
        print(
            "Falta la tabla ops.\"Semilla\". Recrea el volumen de PostgreSQL "
            "o aplica db/migrations/01_schema.sql.",
            file=sys.stderr,
        )
        raise SystemExit(1) from exc
    except (psycopg.Error, KeycloakError, RuntimeError) as exc:
        print(f"Error en el job de semilla: {exc}", file=sys.stderr)
        raise SystemExit(1) from exc
