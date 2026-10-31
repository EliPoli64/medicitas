# MediCitas — Etapa 1 (OLTP)

Plataforma SaaS multi-tenant de gestión de citas médicas (Arquitectura e Ingeniería de Datos, TEC).

Núcleo transaccional: API REST **sin interfaz gráfica**, clínicas aisladas, agenda, ciclo de vida de la cita, identidad en Keycloak y caché de disponibilidad en Redis.

Enunciado del curso: [`spec.md`](spec.md).

## Estado actual

- Repo del grupo (`EliPoli64/medicitas`) con la spec, la documentación de trabajo y el esqueleto del proyecto.
- **Sin implementación todavía:** los archivos de `api/`, `db/` e `id-provider/` son puntos de partida limpios; no contienen el dominio de la Tarea Corta 1 (`reservas`) ni configuración de ese proyecto.
- **Pendiente de acordar en el grupo:** lenguaje definitivo de la API y qué traemos de la Tarea Corta 1.
- El esqueleto actual de `api/` está en **Go** como punto de partida, siguiendo la recomendación inicial. Si el grupo elige otro lenguaje, se reemplaza esa carpeta.

## Estructura del repositorio

```text
.
├── api/                  # Esqueleto de la API (main, auth, Dockerfile, go.mod)
├── db/                   # Imagen de PostgreSQL y carpeta de migraciones vacía
├── id-provider/          # Imagen de Keycloak y carpeta de import del realm
├── docs/                 # Modelo de datos, multi-tenancy y división del trabajo
├── docker-compose.yml    # Esqueleto con api + db + keycloak + redis
├── .env.example          # Variables de entorno de ejemplo
├── .gitignore
└── spec.md               # Enunciado del curso
```

## Propuesta de división (a confirmar)

Somos tres: que **cada uno se quede con un tercio (A, B o C)**, o lo repartimos de otra forma si les late más. No está asignado aún — márcalo en el chat / en [`docs/division-trabajo.md`](docs/division-trabajo.md).

| Tercio | Alcance | RF / módulos |
|--------|---------|--------------|
| **A — Fundación** | Modelo ER, diccionario, multi-tenancy, semilla, Compose base | Modelo, I5 (diseño), arranque |
| **B — Identidad y catálogo** | Keycloak/roles, clínicas, usuarios, servicios, profesionales, pacientes | RF1–RF6, RF14, RF17 |
| **C — Citas y caché** | Disponibilidad, reserva/reprogramación, auditoría, Redis, prueba de carrera | RF7–RF13, RF15–RF16, RF18–RF21, I1–I8 |

Módulo extra (grupos de 3): elegir **uno**. Candidato propuesto: **RLS como segunda estrategia**.

## Qué traer de la Tarea Corta 1 (a decidir)

El repo quedó limpio del código de TC1 a propósito. Propongo que el grupo revise y diga qué sí metemos.

### Vale la pena traer (adaptar, no pegar ciego)

| Pieza en TC1 | Por qué |
|--------------|---------|
| `docker-compose.yml` | Patrón api + db + keycloak, healthchecks, `depends_on`, volumen |
| `api/Dockerfile` | Multi-stage, sin secretos en la imagen |
| `api/auth.go` (+ tests de auth) | Validación JWT/JWKS contra Keycloak (RF17) |
| `id-provider/Dockerfile` + estructura del realm | Import de realm al arrancar (habría que renombrar roles a MediCitas) |
| `db/Dockerfile` + idea de `migrator` | Schema al levantar sin pasos manuales |
| `.env.example` + `.gitignore` / `.dockerignore` | Config sin secretos en el repo |
| `api/go.mod` / `go.sum` | Solo si cerramos **Go** |
| Rutas `/health` y `/ready` (patrón en `api/main.go`) | Contrato de liveness/readiness |

### Traer solo la idea, no el dominio

| Pieza | Nota |
|-------|------|
| Tabla/SPs de `Reserva` y CRUD `/reservas` | Dominio distinto; no sirve pegarlo como MediCitas |
| Realm `tareauno` / rol `reserva-writer` | Hay que rehacer roles de la matriz |
| `k8s/` (kind/Kustomize) | **No** lo pide Etapa 1; dejar fuera salvo que queramos demo extra |
| `data/` local de Postgres | Nunca al repo |
| Scripts `run-k8s` / cleanup de TC1 | Opcional; no son el núcleo de MediCitas |

### Si el lenguaje no es Go

Igual conviene traer Compose, Keycloak/realm (adaptado), migraciones SQL nuevas y la idea de health/ready. Lo que no se reutiliza: binario Go, `auth.go`, Dockerfiles de la API.

## Documentación de trabajo

| Documento | Contenido |
|-----------|-----------|
| [`docs/modelo-datos.md`](docs/modelo-datos.md) | Entidades, ER (borrador), notas 3FN |
| [`docs/esquema-er.md`](docs/esquema-er.md) | Diagrama ER completo del esquema implementado |
| [`docs/multi-tenancy.md`](docs/multi-tenancy.md) | Comparación de estrategias y decisión propuesta |
| [`docs/division-trabajo.md`](docs/division-trabajo.md) | Reparto A/B/C propuesto |

## Pregunta rectora (defensa)

> Dos recepcionistas reservan el último espacio de la agenda exactamente al mismo tiempo. ¿Qué hace el sistema, por qué, y cómo se demuestra?
