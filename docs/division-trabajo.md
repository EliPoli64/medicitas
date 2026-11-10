# División del trabajo — grupo de 3

Reparto acordado en la práctica (Keycloak / Redis / semilla).

| Quién | Encargo |
|-------|---------|
| **Matías** | Keycloak + identidad en API: realm, JWT/JWKS, matriz RF17, `sub`→`"Usuario"`, `GET /pacientes` (I5). Docs: [`keycloak.md`](keycloak.md) |
| **Cristian** | Redis: caché de disponibilidad, invalidación, I8 (paquete listo; cablear a RF7/API) |
| **Elías** | Schema SQL + seeder Python (BD + users canónicos en Keycloak, escribe `idpSub`) |

## Tercios funcionales (referencia de la spec)

### A — Fundación

- Modelo relacional, ER, multi-tenancy (`clinica_id`), Compose base: **hecho** (Elías + repo).
- Semilla de negocio: **hecho** (Elías).
- IdP listo al arrancar: **hecho** (Matías).

### B — Identidad y catálogo

- Identidad delegada (rúbrica): **cerrada al 100 %** (Matías) — ver checklist en [`keycloak.md`](keycloak.md).
- RF1–RF6 y resto de RF14 (alta/baja pacientes, catálogo): **pendiente** en API.

### C — Citas, concurrencia y caché

- RF7–RF13, RF15–RF21, prueba de carrera: **pendiente**.
- Redis / I8: librería **lista** (Cristian); integración con endpoints **pendiente**.

## Módulo extra (obligatorio para 3)

Elegir exactamente uno. Propuesta: **RLS como segunda estrategia**.

## Convención de commits

Cada integrante debe tener commits propios. En archivos compartidos la autoría se considera conjunta.
