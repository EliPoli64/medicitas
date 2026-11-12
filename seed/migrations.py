"""Aplicador idempotente de migraciones SQL de MediCitas.

Lee los archivos .sql de /migrations en orden alfabético, ejecuta los que no
estén registrados en ops."Migracion" usando psql, y los marca como aplicados.

Se ejecuta desde el job one-shot `seed` en cada `docker compose up`, por lo que
las bases de datos existentes reciben migraciones nuevas sin intervención
manual y sin duplicar las ya aplicadas.
"""

from __future__ import annotations

import os
import subprocess
from pathlib import Path
from typing import Any

import psycopg

MIGRATIONS_DIR = Path("/migrations")


def _required_env(name: str) -> str:
    value = os.getenv(name)
    if value is None or value == "":
        raise RuntimeError(f"Falta la variable de entorno {name}")
    return value


def _list_migration_files() -> list[Path]:
    if not MIGRATIONS_DIR.exists():
        raise RuntimeError(f"No existe el directorio de migraciones: {MIGRATIONS_DIR}")
    return sorted(p for p in MIGRATIONS_DIR.iterdir() if p.suffix == ".sql")


def _already_applied(cur: psycopg.Cursor, version: str) -> bool:
    cur.execute('SELECT 1 FROM ops."Migracion" WHERE "version" = %s', (version,))
    return cur.fetchone() is not None


def _migrations_table_exists(cur: psycopg.Cursor) -> bool:
    cur.execute(
        "SELECT 1 FROM pg_tables WHERE schemaname = 'ops' AND tablename = 'Migracion'"
    )
    return cur.fetchone() is not None


def _apply_sql_file(path: Path) -> None:
    """Ejecuta un archivo SQL con psql, deteniéndose ante cualquier error."""
    env = os.environ.copy()
    env["PGPASSWORD"] = _required_env("DB_PASSWORD")
    cmd = [
        "psql",
        "-h", _required_env("DB_HOST"),
        "-p", os.getenv("DB_PORT", "5432"),
        "-U", _required_env("DB_USER"),
        "-d", _required_env("DB_NAME"),
        "-v", "ON_ERROR_STOP=1",
        "-X",  # no leer ~/.psqlrc
        "--single-transaction",  # rollback completo si una sentencia falla
        "-f", str(path),
    ]
    result = subprocess.run(cmd, env=env, capture_output=True, text=True)
    if result.returncode != 0:
        raise RuntimeError(
            f"La migración {path.name} falló:\n{result.stdout}\n{result.stderr}"
        )


def apply_migrations(conn: psycopg.Connection) -> list[str]:
    """Aplica las migraciones pendientes y devuelve la lista de versiones aplicadas."""
    applied: list[str] = []
    with conn.cursor() as cur:
        table_exists = _migrations_table_exists(cur)
        for path in _list_migration_files():
            version = path.stem
            if table_exists and _already_applied(cur, version):
                print(f"Migración {version} ya aplicada; se omite.")
                continue

            print(f"Aplicando migración {version}...")
            _apply_sql_file(path)
            cur.execute(
                'INSERT INTO ops."Migracion" ("version", "checksum", "exito", "detalle") VALUES (%s, %s, true, %s) ON CONFLICT ("version") DO NOTHING',
                (version, "psql-sha256-na", f"aplicada por seed desde {path.name}"),
            )
            applied.append(version)
            if version == "01_schema":
                table_exists = True
            print(f"Migración {version} aplicada.")
    return applied


if __name__ == "__main__":
    # Uso independiente para diagnóstico manual.
    import sys

    conn = psycopg.connect(
        host=_required_env("DB_HOST"),
        port=int(os.getenv("DB_PORT", "5432")),
        user=_required_env("DB_USER"),
        password=_required_env("DB_PASSWORD"),
        dbname=_required_env("DB_NAME"),
        connect_timeout=10,
        autocommit=True,
    )
    try:
        applied = apply_migrations(conn)
        print(f"Migraciones aplicadas: {applied}")
        raise SystemExit(0)
    except Exception as exc:  # noqa: BLE001
        print(f"Error aplicando migraciones: {exc}", file=sys.stderr)
        raise SystemExit(1) from exc
    finally:
        conn.close()
