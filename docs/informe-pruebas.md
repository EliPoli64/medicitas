# Informe de pruebas — MediCitas Etapa 1

Documento de trazabilidad entre requisitos funcionales (RF1–RF21), invariantes
(I1–I8) y las pruebas automatizadas del repositorio. Incluye además las
estrategias de concurrencia, RLS y Redis, y los comandos para reproducir cada
verificación.

Fuentes:

- Enunciado: [`spec.md`](../spec.md)
- Esquema SQL: [`db/migrations/01_schema.sql`](../db/migrations/01_schema.sql)
- Migraciones RLS/perfiles: [`db/migrations/03_rls.sql`](../db/migrations/03_rls.sql), [`db/migrations/04_db_roles_matrix.sql`](../db/migrations/04_db_roles_matrix.sql)
- Caché: [`docs/cache-disponibilidad.md`](cache-disponibilidad.md)
- Disponibilidad: [`docs/disponibilidad.md`](disponibilidad.md)
- RLS: [`docs/rls.md`](rls.md)

## Cómo ejecutar las pruebas

### 1. Arranque del sistema

```bash
cp .env.example .env
docker compose up --build
```

El comando anterior levanta PostgreSQL, Keycloak, Redis, la API y el job de
semilla. No requiere pasos manuales adicionales.

### 2. Pruebas unitarias (sin Docker)

Desde el directorio [`api/`](../api/):

```bash
cd api
go test ./...
```

### 3. Pruebas de integración (requieren la pila real)

```bash
cd api
go test -tags=integration ./...
```

### 4. Smoke E2E

Con la pila arriba:

```bash
./scripts/e2e-smoke.sh
```

Opciones:

```bash
./scripts/e2e-smoke.sh --up       # levanta/reconstruye la pila y espera /ready
./scripts/e2e-smoke.sh --with-go  # añade go test unit + integración al final
./scripts/e2e-smoke.sh --keep     # conserva los datos E2E creados
```

### 5. Benchmark de caché RF7

Requiere PostgreSQL y Redis accesibles desde el host:

```bash
cd api
DB_HOST=localhost DB_PORT=5432 DB_USER=medicitas DB_PASSWORD=cambiar_en_local DB_NAME=medicitas \
REDIS_ADDR=localhost:6379 REDIS_DB=15 \
  go test -tags=integration -run '^$' -bench '^BenchmarkRF7' -benchmem -count=5 .
```

## Trazabilidad RF ↔ pruebas

### RF1 — Alta de clínica

| Prueba | Tipo | Qué verifica |
|--------|------|--------------|
| `TestIntegrationRF1AltaClinica` | Integración | `POST /clinicas` crea clínica y primer administrador en una transacción; devuelve 201 |
| Fases 1–2 de `e2e-smoke.sh` | E2E | Solo `adm-plataforma` puede crear clínicas; el alta incluye usuario y rol en Keycloak |

### RF2 — Gestión de usuarios

| Prueba | Tipo | Qué verifica |
|--------|------|--------------|
| `TestIntegrationRF2aRF4Catalogo` | Integración | `POST /usuarios`, `PATCH /usuarios/:id`, asignación de rol, baja lógica y sincronización con Keycloak |
| Fase 3 de `e2e-smoke.sh` | E2E | Creación de usuarios recepción/profesional; recepción no crea servicios |

### RF3 — Catálogo de servicios

| Prueba | Tipo | Qué verifica |
|--------|------|--------------|
| `TestIntegrationRF2aRF4Catalogo` | Integración | `POST /servicios`, `PATCH /servicios/:id`, validación de especialidad, unicidad de nombre por clínica |
| `TestIntegrationRF3DuracionInvalidaMultiplesProfesionales` | Integración | Cambio de duración de un servicio invalida la agenda de todos los profesionales que lo ofrecen |
| `TestAgendaVersionRF3ServicioDuracionAfectaMultiplesProfesionales` | Integración | Trigger de `AgendaVersion` al cambiar la duración |
| Fase 3 de `e2e-smoke.sh` | E2E | Creación de servicio con duración y especialidad; validación de campos |

### RF4 — Profesionales y servicios habilitados

| Prueba | Tipo | Qué verifica |
|--------|------|--------------|
| `TestIntegrationRF2aRF4Catalogo` | Integración | `POST /profesionales`, `PUT /profesionales/:id/servicios`, validación de especialidad |
| `TestAgendaVersionRF4CambioEstadoProfesional` | Integración | `AgendaVersion` se incrementa al activar/desactivar un profesional |
| `TestAgendaVersionRF4CambioServiciosHabilitados` | Integración | `AgendaVersion` se incrementa al cambiar los servicios habilitados |
| Fase 3 de `e2e-smoke.sh` | E2E | Habilitar servicio en profesional; validación de rol profesional |

### RF5 — Horarios de atención

| Prueba | Tipo | Qué verifica |
|--------|------|--------------|
| `TestIntegrationRF5InvalidaCache` | Integración | Crear/actualizar un horario invalida la caché de disponibilidad |
| `TestAgendaVersionRF5InsertarHorarioIncrementa` | Integración | Trigger de `AgendaVersion` al insertar horario |
| `TestIntegrationI3DentroDeHorarioYBloqueo` | Integración | Cita fuera del horario es rechazada |
| Fase 4 de `e2e-smoke.sh` | E2E | Creación de horarios; horarios solapados devuelven 409 |

### RF6 — Bloqueos de agenda

| Prueba | Tipo | Qué verifica |
|--------|------|--------------|
| `TestIntegrationRF6InvalidaCache` | Integración | Crear/eliminar un bloqueo invalida la caché |
| `TestAgendaVersionRF6BloqueoInsertDelete` | Integración | Trigger de `AgendaVersion` al insertar/eliminar bloqueo |
| `TestIntegrationI3DentroDeHorarioYBloqueo` | Integración | Cita dentro de un bloqueo es rechazada |
| Fase 4 de `e2e-smoke.sh` | E2E | Creación de bloqueo; la disponibilidad oculta el espacio bloqueado |

### RF7 — Disponibilidad por profesional

| Prueba | Tipo | Qué verifica |
|--------|------|--------------|
| `TestIntegrationRF7RF8Disponibilidad` | Integración | Cálculo de espacios, alineación a rejilla, bloqueos, citas vigentes, validación de parámetros |
| `TestDisponibilidadLoaderMatchesRF7Handler` | Unitaria | El `Loader` de caché y el handler RF7 devuelven el mismo resultado |
| `TestIntegrationRF7Cache_*` | Integración | MISS→HIT, invalidación por agenda, aislamiento entre clínicas, fallback si Redis cae, concurrencia |
| Fase 5 de `e2e-smoke.sh` | E2E | `GET /profesionales/:id/disponibilidad` devuelve espacios correctos |
| Benchmark `BenchmarkRF7_*` | Benchmark | Comparación directa PostgreSQL vs HIT Redis |

### RF8 — Disponibilidad por servicio

| Prueba | Tipo | Qué verifica |
|--------|------|--------------|
| `TestIntegrationRF7RF8Disponibilidad` | Integración | `GET /servicios/:id/disponibilidad` en rango, solo fechas con espacios |
| Fase 5 de `e2e-smoke.sh` | E2E | Rango válido/inválido; profesionales habilitados con espacios |

### RF9 — Reservar cita

| Prueba | Tipo | Qué verifica |
|--------|------|--------------|
| `TestIntegrationRF9ReservarCita` | Integración | Reserva válida; validaciones de entrada |
| `TestIntegrationRF9I8ConsultarReservarConsultar` | Integración | Reservar invalida la caché; la siguiente consulta no ofrece el espacio |
| `TestIntegrationRF9CarreraUnaGanadora` | Integración | Cinco clientes concurrentes compiten por el último espacio; exactamente uno gana |
| `TestIntegrationI2PacienteSinSolape` | Integración | Paciente con dos citas solapadas es rechazado |
| Fases 6–7 de `e2e-smoke.sh` | E2E | Reserva, solape profesional/paciente, carrera concurrente |

### RF10 — Reprogramar cita

| Prueba | Tipo | Qué verifica |
|--------|------|--------------|
| `TestIntegrationRF10a13CicloCita` | Integración | Reprogramación atómica vía `app.reprogramarCita`; origen pasa a `reprogramada`; nueva cita `reservada` |
| Fase 6 de `e2e-smoke.sh` | E2E | Reprogramar libera el espacio anterior y ocupa el nuevo |

### RF11 — Cancelar cita

| Prueba | Tipo | Qué verifica |
|--------|------|--------------|
| `TestIntegrationRF10a13CicloCita` | Integración | Cancelación registra motivo, usuario y timestamp; libera el espacio |
| Fase 6 de `e2e-smoke.sh` | E2E | Cancelar devuelve 200; el espacio vuelve a estar disponible |

### RF12 — Registrar desenlace

| Prueba | Tipo | Qué verifica |
|--------|------|--------------|
| `TestIntegrationRF10a13CicloCita` | Integración | Desenlace `atendida`/`ausente` solo después de `inicio` |
| Fase 9 de `e2e-smoke.sh` | E2E | Desenlace atendida sobre cita pasada; desenlace antes de hora devuelve 400 |

### RF13 — Máquina de estados

| Prueba | Tipo | Qué verifica |
|--------|------|--------------|
| `TestIntegrationRF10a13CicloCita` | Integración | Estados terminales inmutables; transiciones ilegales rechazadas |
| Fase 6 de `e2e-smoke.sh` | E2E | Desenlace sobre cita cancelada devuelve 400 |

### RF14 — Pacientes y contactos

| Prueba | Tipo | Qué verifica |
|--------|------|--------------|
| `TestIntegrationRF15RF18RF19NotasBitacora` | Integración | Alta/lectura de pacientes y contactos; auditoría de ambas operaciones |
| Fase 9 de `e2e-smoke.sh` | E2E | Crear paciente, agregar contacto, leer paciente |

### RF15 — Notas clínicas

| Prueba | Tipo | Qué verifica |
|--------|------|--------------|
| `TestIntegrationRF15RF18RF19NotasBitacora` | Integración | Nota solo si la cita está `atendida`; solo el profesional que atendió; una sola nota por cita |
| Fase 9 de `e2e-smoke.sh` | E2E | Crear nota, rechazo de nota vacía, lectura por el profesional, rechazo por recepción |

### RF16 — Historial de citas del paciente

| Prueba | Tipo | Qué verifica |
|--------|------|--------------|
| `TestIntegrationRF15RF18RF19NotasBitacora` | Integración | `GET /pacientes/:id/citas` lista citas con estado dentro de la clínica |
| Fase 6 de `e2e-smoke.sh` | E2E | El historial respeta el tenant |

### RF17 — Autenticación y autorización

| Prueba | Tipo | Qué verifica |
|--------|------|--------------|
| `TestIntegrationIdentityStack` | Integración | Validación JWKS, token inválido → 401, rol incorrecto → 403, `/me`, `/permisos` |
| `TestIntegrationRLS` | Integración | Sesión con `app.clinica_id` correcta; manipulación de query no cruza de clínica |
| `TestIntegrationPerfilesSQL` | Integración | Perfiles SQL de menor privilegio aplican la matriz |
| Fase 1 de `e2e-smoke.sh` | E2E | Sin token → 401; `adm-plataforma` sobre `/pacientes` → 403; token recepción funciona |
| Fase 11 de `e2e-smoke.sh` | E2E | `SET ROLE` a perfiles SQL; `adm_plataforma` no lee `Paciente`; recepción sin GUC ve 0 |

### RF18 — Bitácora append-only

| Prueba | Tipo | Qué verifica |
|--------|------|--------------|
| `TestIntegrationRF15RF18RF19NotasBitacora` | Integración | Lectura y escritura de notas/contactos generan entrada; metadata sin contenido sensible |
| Fase 9 de `e2e-smoke.sh` | E2E | Entradas de lectura/escritura presentes; `UPDATE`/`DELETE`/`TRUNCATE` rechazados por el motor |

### RF19 — Consulta de bitácora

| Prueba | Tipo | Qué verifica |
|--------|------|--------------|
| `TestIntegrationRF15RF18RF19NotasBitacora` | Integración | Filtros por usuario, paciente y rango de fechas |
| Fase 9 de `e2e-smoke.sh` | E2E | Filtros por paciente, usuario y rango; recepción no consulta bitácora |

### RF20 — Agenda del día

| Prueba | Tipo | Qué verifica |
|--------|------|--------------|
| `TestIntegrationRF10a13CicloCita` | Integración | `GET /agenda?fecha=` devuelve citas del día en zona horaria de la clínica |
| Fase 10 de `e2e-smoke.sh` | E2E | Agenda del día; coincidencia con conteo SQL |

### RF21 — Conteo por estado

| Prueba | Tipo | Qué verifica |
|--------|------|--------------|
| `TestIntegrationRF10a13CicloCita` | Integración | `GET /citas/resumen` devuelve conteo por estado y total |
| Fase 10 de `e2e-smoke.sh` | E2E | Total coincide con SQL; estados ausentes aparecen en 0 |

## Trazabilidad invariantes ↔ pruebas

| Invariante | Pruebas | Cómo se sostiene |
|------------|---------|------------------|
| **I1** — Sin solape de profesional | `TestIntegrationRF9CarreraUnaGanadora`, `TestIntegrationRF9ReservarCita`, Fase 7 de `e2e-smoke.sh` | `EXCLUDE USING gist` en `Cita` con `WHERE estado = 'reservada'` |
| **I2** — Sin solape de paciente | `TestIntegrationI2PacienteSinSolape`, Fase 6 de `e2e-smoke.sh` | `EXCLUDE USING gist` en `Cita` con `WHERE estado = 'reservada'` |
| **I3** — Dentro de horario y fuera de bloqueos | `TestIntegrationI3DentroDeHorarioYBloqueo`, `TestIntegrationRF10a13CicloCita`, Fase 6 de `e2e-smoke.sh` | Trigger `CitaValidarHorario` |
| **I4** — Duración fijada por servicio | `TestIntegrationRF10a13CicloCita`, `TestIntegrationRF9ReservarCita`, Fase 6 de `e2e-smoke.sh` | Trigger `CitaPreparar` copia duración y calcula `fin` |
| **I5** — Aislamiento entre clínicas | `TestIntegrationRLS`, `TestIntegrationPerfilesSQL`, `TestReservarCitaI5IgnoresQuery`, `TestListPacientesI5IgnoresQueryClinicaId`, `TestCreateBloqueoI5IgnoresQuery`, `TestCreateHorarioI5IgnoresQuery`, `TestAgendaDiaI5IgnoresQuery`, `TestCreatePacienteI5IgnoresQuery`, `TestIntegrationRF7Cache_ClinicIsolation`, Fases 1–2 de `e2e-smoke.sh` | `clinicaId` + FKs compuestas + RLS + validación en API |
| **I6** — Bitácora append-only | `TestIntegrationRF15RF18RF19NotasBitacora`, Fase 9 de `e2e-smoke.sh` | Triggers `AuditoriaSinUpdate/Delete/Truncate` |
| **I7** — Liberación inmediata del espacio | `TestIntegrationRF10a13CicloCita`, `TestIntegrationRF7RF8Disponibilidad`, `TestIntegrationRF9I8ConsultarReservarConsultar`, Fase 6 de `e2e-smoke.sh` | Estados finales fuera del `EXCLUDE`; índices parciales; invalidación de caché |
| **I8** — Caché no contradice la BD | `TestIntegrationRF9I8ConsultarReservarConsultar`, `TestIntegrationRF7Cache_CitaReservadaInvalidaSnapshot`, `TestIntegrationRF7Cache_AgendaChangeInvalidatesHit`, `TestIntegrationRF5InvalidaCache`, `TestIntegrationRF6InvalidaCache`, Fase 8 de `e2e-smoke.sh` | `AgendaVersion` + epochs de Redis; validación de snapshot en HIT |

## Concurrencia y transacciones

### Reserva (RF9)

- Nivel de aislamiento: **READ COMMITTED** (default de PostgreSQL; suficiente con
  restricciones de exclusión).
- Mecanismo: `EXCLUDE USING gist` en `Cita` garantiza que, de dos transacciones
  concurrentes que intenten reservar el mismo espacio, solo una pueda hacer
  COMMIT.
- Manejo de deadlock: PostgreSQL levanta `40P01`; la API responde `409` o
  reintenta según el caso. En la práctica, el `EXCLUDE` resuelve el conflicto
  antes de que se produzca deadlock para el mismo espacio.
- Prueba de carrera: `TestIntegrationRF9CarreraUnaGanadora` y Fase 7 de
  `e2e-smoke.sh` lanzan varios clientes concurrentes contra el último espacio y
  verifican que solo una reserva resulta concedida.

### Reprogramación (RF10)

- La función `app."reprogramarCita"` ejecuta `UPDATE` de la cita origen a
  `reprogramada` e `INSERT` de la nueva cita dentro de la misma transacción.
- Si la nueva cita viola `I1`, `I2`, `I3` o `I4`, PostgreSQL revierte toda la
  transacción y la cita origen permanece `reservada`.

## RLS y perfiles SQL

- La estrategia principal de multi-tenancy es la columna `clinicaId` filtrada
  por la API a partir de la identidad (`idpSub`).
- RLS es la **segunda estrategia** (módulo de grupo de 3). La API se conecta
  como `medicitas_app` (`NOBYPASSRLS`, `NOINHERIT`) y, tras resolver el usuario,
  hace `SET ROLE` al perfil de la matriz.
- Perfiles: `medicitas_adm_plataforma`, `medicitas_adm_clinica`,
  `medicitas_recepcion`, `medicitas_profesional`.
- Pruebas: `TestIntegrationRLS` y `TestIntegrationPerfilesSQL` verifican que un
  perfil sin GUC de clínica ve 0 filas, que `adm_plataforma` no lee `Paciente`,
  que `medicitas_app` sin `SET ROLE` no lee tablas, y que un `INSERT` en otra
  clínica es rechazado.
- Smoke: Fase 11 de `e2e-smoke.sh` reproduce las mismas comprobaciones con
  `psql`.

## Redis y caché de disponibilidad

- La fuente de verdad sigue siendo PostgreSQL. Redis almacena snapshots de la
  disponibilidad RF7.
- Claves: `medicitas:avail:v2:epoch:{clinica}:{profesional}` y
  `medicitas:avail:v2:data:{clinica}:{profesional}:{servicio}:{fecha}:{paso}:{epoch}`.
- Invalidación: doble barrera formada por epochs aleatorios en Redis y la tabla
  `AgendaVersion` en PostgreSQL.
- Triggers en `Cita`, `Bloqueo`, `HorarioAtencion`, `Profesional`,
  `ProfesionalServicio` y `Servicio` incrementan `AgendaVersion` dentro de la
  transacción de la mutación.
- Después del COMMIT, la API llama a `InvalidateAgenda` para publicar un nuevo
  epoch.
- Si Redis falla, `AgendaVersion` sigue garantizando que un snapshot obsoleto no
  se sirve.
- Pruebas: `TestIntegrationRF7Cache_*`, `TestIntegrationRF5InvalidaCache`,
  `TestIntegrationRF6InvalidaCache`, `TestIntegrationRF9I8ConsultarReservarConsultar`
  y Fase 8 de `e2e-smoke.sh`.

## Resultados verificables

### Validaciones ejecutadas en este entorno

| Verificación | Comando | Resultado |
|--------------|---------|-----------|
| Sintaxis de `e2e-smoke.sh` | `bash -n scripts/e2e-smoke.sh` | Sin errores |
| Ayuda del smoke | `./scripts/e2e-smoke.sh --help` | Muestra uso y opciones correctamente |
| Finales de línea del smoke | inspección binaria | LF (no CRLF) |

### Validaciones que requieren la pila Docker

Las siguientes pruebas no se ejecutaron en este entorno porque requieren
PostgreSQL, Keycloak y Redis levantados. El equipo debe ejecutarlas antes de la
entrega:

| Verificación | Comando | Criterio de éxito |
|--------------|---------|-------------------|
| Arranque con un solo comando | `docker compose up --build` | Todos los servicios healthy; `/ready` responde 200 |
| Unitarias | `cd api && go test ./...` | PASS |
| Integración | `cd api && go test -tags=integration ./...` | PASS |
| Smoke E2E | `./scripts/e2e-smoke.sh --with-go` | `E2E SMOKE: OK` y Go tests PASS |
| Benchmark RF7 | comando en §5 | Resultados comparativos reproducibles |

### Pendientes manuales del equipo

1. Ejecutar `docker compose up --build` en una máquina limpia y confirmar que
   `/ready` responde 200 sin intervención.
2. Ejecutar `go test ./...` y `go test -tags=integration ./...` desde `api/` y
   confirmar que todos los tests pasan.
3. Ejecutar `./scripts/e2e-smoke.sh --with-go` y confirmar `E2E SMOKE: OK`.
4. Generar el video de demostración (arranque, recorrido de cita, prueba de
   carrera).
5. Confirmar que el repositorio no incluye `node_modules`, `vendor`, `venv` ni
   carpetas de dependencias instaladas.
