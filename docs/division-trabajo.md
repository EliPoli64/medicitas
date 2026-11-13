# División del trabajo — grupo de 3

Reparto acordado en la práctica (Keycloak / Redis / semilla).

| Quién | Encargo |
|-------|---------|
| **Matías** | Identidad + API de dominio (RF1–RF6, RF9–RF21, notas/bitácora). Docs: [`keycloak.md`](keycloak.md) |
| **Cristian** | Redis / I8: caché de disponibilidad cableada a RF7, `AgendaVersion`, invalidación, benchmark |
| **Elías** | Schema SQL + seeder Python (BD + users canónicos en Keycloak, escribe `idpSub`) |

## Tercios funcionales (referencia de la spec)

### A — Fundación

- Modelo relacional, ER, multi-tenancy (`clinica_id`), Compose base: **hecho** (Elías + repo).
- Semilla de negocio: **hecho** (Elías).
- IdP listo al arrancar: **hecho** (Matías).

### B — Identidad y catálogo

- Identidad delegada (rúbrica): **cerrada al 100 %** (Matías) — ver checklist en [`keycloak.md`](keycloak.md).
- RF1–RF6, RF14 y catálogo: **hecho** en API.

### C — Citas, concurrencia y caché

- RF7–RF13, RF15–RF21, prueba de carrera: **hecho** en API.
- Redis / I8: **cableado** a RF7 (Cristian) e invalidado al mutar agenda/citas.

## Módulo extra (obligatorio para 3)

Elegir exactamente uno. Propuesta: **RLS como segunda estrategia**.

## Convención de commits

Cada integrante debe tener commits propios. En archivos compartidos la autoría se considera conjunta.
