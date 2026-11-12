# Disponibilidad (RF7/RF8) — modelo de cálculo e índices

Documento de defensa de las decisiones de RF7 (espacios disponibles de un
profesional en una fecha) y RF8 (disponibilidad por servicio en un rango):
reglas del cálculo, contrato de las rutas y justificación de los índices con
planes de ejecución reales.

## 1. Modelo de cálculo

Un **espacio** es un intervalo `[inicio, fin)` de `duracionMinutos` del servicio
solicitado (I4: la duración la fija el servicio; la API nunca la recibe del
cliente) que cumple, a la vez:

1. cabe **entero dentro de un solo bloque** de `HorarioAtencion` vigente para
   ese día de la semana (mismo criterio que el trigger `validarCitaEnHorario`;
   RF5 admite varios bloques por día: jornada partida);
2. no se solapa con ningún **bloqueo** (`Bloqueo`, incluso los multidía, que se
   recortan al día consultado);
3. no se solapa con ninguna **cita vigente** (`Cita.estado = 'reservada'`).
   Cancelar o reprogramar libera el espacio de inmediato (I7); `atendida` y
   `ausente` son desenlaces de citas ya pasadas y no ocupan.

Además:

- los **inicios se alinean a una rejilla** de `pasoMinutos` (30 por defecto,
  5–120) medida desde la medianoche local de la clínica (08:00, 08:30, …);
- solo se ofrecen espacios **futuros** (disponibilidad hacia adelante, RF6);
- todo se calcula en la **zona horaria de la clínica** (`Clinica.zonaHoraria`):
  la fecha es local, los horarios son horas locales, y la salida viaja como
  instantes RFC3339 (con `horaLocal` como lectura cómoda).

Consecuencia de defensa: **todo espacio que RF7 ofrece es reservable según el
motor**, porque el cálculo es un espejo deliberado de los triggers
`prepararCita` + `validarCitaEnHorario`. La prueba de integración
`TestIntegrationRF7RF8Disponibilidad` lo verifica en la dirección contraria:
inserta una cita vía SQL en el primer espacio calculado y el INSERT pasa los
triggers; si el cálculo se desalineara del motor, esa prueba falla.

## 2. Contrato de las rutas

Permiso (matriz RF17, `OpConsultarDisponibilidad`): `adm-clinica`, `recepcion`
y `profesional`. Tenant siempre del token (I5): un id de otra clínica responde
`404`, sin filtrar existencia.

### `GET /profesionales/:id/disponibilidad` (RF7)

Query: `fecha=YYYY-MM-DD` (obligatoria), `servicioId` (obligatorio),
`pasoMinutos` (opcional).

```json
{
  "clinicaIdentificador": "clinica-norte",
  "profesionalId": "…", "servicioId": "…",
  "fecha": "2026-11-16", "zonaHoraria": "America/Costa_Rica",
  "duracionMinutos": 30, "pasoMinutos": 30,
  "espacios": [
    {"inicio": "2026-11-16T08:00:00-06:00", "fin": "2026-11-16T08:30:00-06:00", "horaLocal": "08:00"}
  ]
}
```

Códigos: `200` / `400` (parámetros, servicio o profesional inactivo,
profesional no habilitado para el servicio) / `401` / `403` (rol fuera de la
matriz) / `404` (profesional o servicio no es de la clínica) / `503`.

### `GET /servicios/:id/disponibilidad` (RF8)

Query: `desde=YYYY-MM-DD`, `hasta=YYYY-MM-DD` (obligatorias, máximo 62 días),
`pasoMinutos` (opcional). Devuelve **solo las fechas en las que algún
profesional habilitado tiene al menos un espacio**, con los espacios de cada
uno ("los profesionales que tienen espacio y cuáles son").

Códigos: `200` / `400` (rango inválido o servicio inactivo) / `401` / `403` /
`404` / `503`.

Colección de peticiones: [`docs/http/disponibilidad.http`](http/disponibilidad.http).

## 3. Las tres consultas de RF7 y sus índices

RF7 carga tres conjuntos pequeños por (clínica, profesional, día) y calcula en
memoria; RF8 reutiliza el mismo cálculo por fecha con los datos cargados una
vez por rango. Cada consulta se sirve de un índice del esquema
(`db/migrations/01_schema.sql`, sección Índices). Medidas con
[`docs/bench/disponibilidad_bench.sql`](bench/disponibilidad_bench.sql): un
dataset sintético (50 profesionales, 500 horarios, 2.600 bloqueos, **19.200
citas** alrededor de la fecha consultada) dentro de una transacción que se
revierte. Reproducir:

```console
$ docker compose exec -T db psql -U medicitas -d medicitas < docs/bench/disponibilidad_bench.sql
```

### 3.1 Horarios del día → `HorarioAtencionProfesionalIdx`

```sql
SELECT h."diaSemana", h."horaInicio", h."horaFin", h."vigenteDesde"::text, h."vigenteHasta"::text
FROM "HorarioAtencion" h JOIN "Clinica" c ON c."id" = h."clinicaId"
WHERE c."identificador" = $1 AND h."profesionalId" = $2 AND h."activo" = true
  AND h."vigenteDesde" <= $4::date
  AND (h."vigenteHasta" IS NULL OR h."vigenteHasta" >= $3::date);
```

```
Nested Loop  (actual time=0.025..0.031 rows=10.00)
  ->  Index Scan using "ClinicaIdentificadorUq" on "Clinica" c
  ->  Index Scan using "HorarioAtencionSinSolape" on "HorarioAtencion" h
        Index Cond: (("clinicaId" = c.id) AND ("profesionalId" = '…'::uuid))
        Filter: (vigencia…)
Execution Time: 0.047 ms
```

El acceso es por `(clinicaId, profesionalId)`, que cubren tanto
`HorarioAtencionProfesionalIdx` (parcial, `WHERE activo`, incluye `diaSemana`)
como el índice GiST de la exclusión `HorarioAtencionSinSolape`; el planificador
elige uno u otro según escala y ambos devuelven solo los bloques del
profesional (~10 filas). La vigencia se aplica como filtro.

### 3.2 Bloqueos que tocan el día → `BloqueoProfesionalRangoIdx`

```sql
SELECT b."inicio", b."fin"
FROM "Bloqueo" b JOIN "Clinica" c ON c."id" = b."clinicaId"
WHERE c."identificador" = $1 AND b."profesionalId" = $2
  AND b."inicio" < $4::timestamptz AND b."fin" > $3::timestamptz;
```

```
  ->  Index Only Scan using "BloqueoProfesionalRangoIdx" on "Bloqueo" b
        Index Cond: (("clinicaId" = c.id) AND ("profesionalId" = '…'::uuid))
        Filter: (("inicio" < '2026-11-17 06:00:00+00') AND ("fin" > '2026-11-16 06:00:00+00'))
Execution Time: 0.074 ms
```

`BloqueoProfesionalRangoIdx (clinicaId, profesionalId, inicio, fin)` permite un
**index-only scan**: igualdad en las dos primeras columnas, cota superior sobre
`inicio` y el solape con el rango se resuelve sin visitar la tabla.

### 3.3 Citas vigentes que tocan el día → `CitaProfesionalInicioIdx`

```sql
SELECT ci."inicio", ci."fin"
FROM "Cita" ci JOIN "Clinica" c ON c."id" = ci."clinicaId"
WHERE c."identificador" = $1 AND ci."profesionalId" = $2
  AND ci."estado" = 'reservada'
  AND ci."inicio" >= $3::timestamptz AND ci."inicio" < $4::timestamptz;
```

```
  ->  Index Scan using "CitaProfesionalInicioIdx" on "Cita" ci
        Index Cond: (("clinicaId" = c.id) AND ("profesionalId" = '…'::uuid)
                     AND (inicio >= '2026-11-16 06:00:00+00') AND (inicio < '2026-11-17 06:00:00+00'))
Execution Time: 0.046 ms   (19.200 citas en la tabla; 16 devueltas)
```

Dos decisiones sostienen este plan:

- **El índice es parcial**: `CitaProfesionalInicioIdx (clinicaId, profesionalId,
  inicio) WHERE estado = 'reservada'`. Solo las citas que ocupan espacio
  entran en él; las canceladas/reprogramadas/atendidas/ausentes no. Es la
  semántica de I7 convertida en estructura.
- **El rango va cerrado sobre `inicio`**. Como `validarCitaEnHorario` prohíbe
  citas que crucen la medianoche local, toda cita que toca `[desde, hasta)`
  empieza dentro del rango; acotar `inicio` (en vez de usar solape
  `inicio < fin_rango AND fin > inicio_rango`) deja el `Index Cond` completo y
  el planificador no cae a un barrido por `CitaClinicaEstadoIdx` (se midió el
  barrido alternativo: 8.001 filas leídas en vez de 16).

## 4. RF8: disponibilidad por servicio

La consulta de profesionales habilitados se sirve de
`ProfesionalServicioServicioIdx (clinicaId, servicioId)`, con las uniones a
`Profesional`, `Usuario` y `Especialidad` resueltas por sus claves/únicos
(`ProfesionalUsuarioUq`, `ProfesionalEspecialidadIdx`, `EspecialidadPk`):

```
Nested Loop … rows=50
  ->  Hash Join → Seq Scan on "ProfesionalServicio" ps  (rows=50)
  ->  Index Scan using "UsuarioClinicaIdx" on "Usuario" u
  ->  Index Scan using "ProfesionalUsuarioUq" on "Profesional" p
  ->  Index Scan using "EspecialidadPk" on "Especialidad" e
Execution Time: 2.7 ms
```

`ProfesionalServicio` es una tabla N:M pequeña (una fila por servicio
ofrecido); a escala de clínica con muchos servicios el planificador entra por
`ProfesionalServicioServicioIdx` y por eso el índice existe. Con el dataset de
bench (50 filas) el hash join es más barato y el coste total sigue en
milisegundos. Por cada profesional del resultado se ejecuta el cálculo del §3
una vez por rango (no por fecha).

## 5. Límites y decisiones explícitas

- **Rango máximo de RF8: 62 días.** El cálculo es O(profesionales × fechas ×
  huecos) en memoria con datos ya cargados; la cota protege la latencia sin
  perder el caso de uso (agenda de dos meses).
- **`pasoMinutos` es rejilla, no duración.** La duración de la cita siempre la
  fija el servicio (I4); el paso solo decide los inicios candidatos.
- **Horarios con segundos**: el inicio del bloque se redondea hacia arriba y el
  fin hacia abajo al pasar a minutos, para que ningún espacio calculado pueda
  ser rechazado por el trigger.
- **Caché**: el cálculo está cableado como `LoaderFunc` de
  [`api/internal/availabilitycache`](../api/internal/availabilitycache/cache.go).
  RF7 consulta la caché cuando está disponible y valida cada HIT contra
  `AgendaVersion` en PostgreSQL (doble barrera: epochs + versión). Ver
  [`docs/cache-disponibilidad.md`](cache-disponibilidad.md) para el diseño
  completo.
- **Pruebas**: 21 unitarias (cálculo puro + handlers con store falso),
  `TestIntegrationRF7RF8Disponibilidad` contra la pila real y seis pruebas de
  integración adicionales en
  [`api/availability_rf7_cache_integration_test.go`](../api/availability_rf7_cache_integration_test.go)
  que cubren MISS→HIT, invalidación por bloqueo, invalidación por cita
  reservada (I8), aislamiento entre clínicas, fallback si Redis cae y
  concurrencia.
- **Benchmark**: [`api/availability_rf7_bench_test.go`](../api/availability_rf7_bench_test.go)
  reporta que un HIT válido de Redis es ~3.3× más rápido que el cálculo directo
  desde PostgreSQL en el entorno medido (ver [`docs/cache-disponibilidad.md`](cache-disponibilidad.md)).
