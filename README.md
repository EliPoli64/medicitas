# MediCitas — Etapa 1 (OLTP)

Plataforma SaaS multi-tenant de gestión de citas médicas (Arquitectura e Ingeniería de Datos, TEC).

Núcleo transaccional: API REST **sin interfaz gráfica**, clínicas aisladas, agenda, ciclo de vida de la cita, identidad en Keycloak y caché de disponibilidad en Redis.

Enunciado del curso: [`spec.md`](spec.md).

## Estado actual

- Repo del grupo (`EliPoli64/medicitas`) con la spec (Elías) y esta propuesta de docs de Fundación.
- **Pendiente de acordar en el grupo:** lenguaje de la API y qué traemos de la Tarea Corta 1.
- Recomendación inicial: **Go**, para no reescribir auth/Compose/JWT desde cero. Si preferimos otra cosa (p. ej. Ruby), también se discute — ver abajo qué sí vale la pena copiar en cualquier caso.

## Propuesta de división (a confirmar)

Somos tres: que **cada uno se quede con un tercio (A, B o C)**, o lo repartimos de otra forma si les late más. No está asignado aún — márcalo en el chat / en [`docs/division-trabajo.md`](docs/division-trabajo.md).

| Tercio | Alcance | RF / módulos |
|--------|---------|--------------|
| **A — Fundación** | Modelo ER, diccionario, multi-tenancy, semilla, Compose base | Modelo, I5 (diseño), arranque |
| **B — Identidad y catálogo** | Keycloak/roles, clínicas, usuarios, servicios, profesionales, pacientes | RF1–RF6, RF14, RF17 |
| **C — Citas y caché** | Disponibilidad, reserva/reprogramación, auditoría, Redis, prueba de carrera | RF7–RF13, RF15–RF16, RF18–RF21, I1–I8 |

Módulo extra (grupos de 3): elegir **uno**. Candidato propuesto: **RLS como segunda estrategia**.

## Qué traer de la Tarea Corta 1 (a decidir)

No copié el código de TC1 a este repo a propósito. Propongo que el grupo revise y diga qué sí metemos.

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

## Documentación en este PR/commit

| Documento | Contenido |
|-----------|-----------|
| [`docs/modelo-datos.md`](docs/modelo-datos.md) | Entidades, ER (borrador), notas 3FN |
| [`docs/multi-tenancy.md`](docs/multi-tenancy.md) | Comparación de estrategias y decisión propuesta |
| [`docs/division-trabajo.md`](docs/division-trabajo.md) | Reparto A/B/C propuesto |

## Pregunta rectora (defensa)

> Dos recepcionistas reservan el último espacio de la agenda exactamente al mismo tiempo. ¿Qué hace el sistema, por qué, y cómo se demuestra?
