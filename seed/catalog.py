"""Catálogo de datos de demostración para MediCitas."""

from __future__ import annotations

from dataclasses import dataclass
from datetime import date, datetime, time, timedelta
from zoneinfo import ZoneInfo

CLINIC_TZ = ZoneInfo("America/Costa_Rica")


@dataclass(frozen=True)
class ClinicSpec:
    identificador: str
    nombre: str


@dataclass(frozen=True)
class SpecialtySpec:
    codigo: str
    nombre: str


@dataclass(frozen=True)
class UserSpec:
    username: str
    db_role: str
    clinic: str | None
    first_name: str
    last_name: str

    @property
    def email(self) -> str:
        return f"{self.username}@medicitas.local"


@dataclass(frozen=True)
class ProfessionalSpec:
    username: str
    clinic: str
    specialty: str
    service_name: str
    service_duration: int


@dataclass(frozen=True)
class PatientSpec:
    key: str
    clinic: str
    identificacion: str
    nombre: str
    fecha_nacimiento: date


CLINICS = (
    ClinicSpec("clinica-norte", "Clínica Norte"),
    ClinicSpec("clinica-centro", "Clínica Centro"),
    ClinicSpec("clinica-sur", "Clínica Sur"),
)

SPECIALTIES = (
    SpecialtySpec("general", "Medicina General"),
    SpecialtySpec("pediatria", "Pediatría"),
    SpecialtySpec("cardiologia", "Cardiología"),
    SpecialtySpec("dermatologia", "Dermatología"),
)

USERS = (
    UserSpec("plat-admin", "adm_plataforma", None, "Plataforma", "Admin"),
    UserSpec("adm-norte", "adm_clinica", "clinica-norte", "Ana", "Méndez"),
    UserSpec("recep-norte", "recepcion", "clinica-norte", "Recepción", "Norte"),
    UserSpec("dra-garcia", "profesional", "clinica-norte", "Laura", "García"),
    UserSpec("dr-rojas", "profesional", "clinica-norte", "Andrés", "Rojas"),
    UserSpec("adm-centro", "adm_clinica", "clinica-centro", "Carlos", "Vega"),
    UserSpec("recep-centro", "recepcion", "clinica-centro", "Recepción", "Centro"),
    UserSpec("dra-jimenez", "profesional", "clinica-centro", "Sofía", "Jiménez"),
    UserSpec("dr-mora", "profesional", "clinica-centro", "Diego", "Mora"),
    UserSpec("adm-sur", "adm_clinica", "clinica-sur", "Marta", "Salas"),
    UserSpec("recep-sur", "recepcion", "clinica-sur", "Recepción", "Sur"),
    UserSpec("dra-vargas", "profesional", "clinica-sur", "Valeria", "Vargas"),
    UserSpec("dr-solano", "profesional", "clinica-sur", "Esteban", "Solano"),
)

PROFESSIONALS = (
    ProfessionalSpec("dra-garcia", "clinica-norte", "general", "Consulta de medicina general", 30),
    ProfessionalSpec("dr-rojas", "clinica-norte", "pediatria", "Consulta pediátrica", 30),
    ProfessionalSpec("dra-jimenez", "clinica-centro", "cardiologia", "Consulta cardiológica", 45),
    ProfessionalSpec("dr-mora", "clinica-centro", "general", "Consulta de medicina general", 30),
    ProfessionalSpec("dra-vargas", "clinica-sur", "dermatologia", "Consulta dermatológica", 30),
    ProfessionalSpec("dr-solano", "clinica-sur", "general", "Consulta de medicina general", 30),
)

PATIENTS = (
    PatientSpec("norte-1", "clinica-norte", "PAC-NORTE-001", "María Solís", date(1985, 3, 12)),
    PatientSpec("norte-2", "clinica-norte", "PAC-NORTE-002", "José Ramírez", date(1978, 7, 24)),
    PatientSpec("norte-3", "clinica-norte", "PAC-NORTE-003", "Ana Chaves", date(1992, 11, 2)),
    PatientSpec("norte-4", "clinica-norte", "PAC-NORTE-004", "Luis Fonseca", date(1969, 5, 18)),
    PatientSpec("centro-1", "clinica-centro", "PAC-CENTRO-001", "Carmen Rojas", date(1988, 1, 30)),
    PatientSpec("centro-2", "clinica-centro", "PAC-CENTRO-002", "Pedro Arias", date(1975, 9, 9)),
    PatientSpec("centro-3", "clinica-centro", "PAC-CENTRO-003", "Lucía Méndez", date(2001, 4, 14)),
    PatientSpec("centro-4", "clinica-centro", "PAC-CENTRO-004", "Jorge Solano", date(1962, 12, 5)),
    PatientSpec("sur-1", "clinica-sur", "PAC-SUR-001", "Elena Castro", date(1990, 6, 21)),
    PatientSpec("sur-2", "clinica-sur", "PAC-SUR-002", "Miguel Vargas", date(1983, 8, 17)),
    PatientSpec("sur-3", "clinica-sur", "PAC-SUR-003", "Rosa Villalobos", date(1995, 2, 28)),
    PatientSpec("sur-4", "clinica-sur", "PAC-SUR-004", "Andrés Quesada", date(1971, 10, 11)),
)

KEYCLOAK_ROLE_BY_DB = {
    "adm_plataforma": "adm-plataforma",
    "adm_clinica": "adm-clinica",
    "recepcion": "recepcion",
    "profesional": "profesional",
}


def local_today() -> date:
    return datetime.now(CLINIC_TZ).date()


def relative_weekday(base: date, offset_days: int) -> date:
    """Desplaza una fecha y la ajusta al día hábil más cercano."""
    current = base + timedelta(days=offset_days)
    step = 1 if offset_days >= 0 else -1
    while current.weekday() >= 5:
        current += timedelta(days=step)
    return current


def local_start(day: date, hour: int, minute: int = 0) -> datetime:
    return datetime.combine(day, time(hour, minute), tzinfo=CLINIC_TZ)
