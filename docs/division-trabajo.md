# División del trabajo — grupo de 3

Reparto acordado en la práctica (Keycloak / Redis / semilla).

| Quién | Encargo |
|-------|---------|
| **Matías** | Keycloak: realm, roles, client, claim `clinica_id`, docs de identidad ([`docs/keycloak.md`](keycloak.md)) |
| **Cristian** | Redis: caché de disponibilidad, invalidación, I8 |
| **Elías** | Schema SQL + seeder Python (BD + users canónicos en Keycloak) |

## Tercios funcionales (referencia de la spec)

### A — Fundación

- Modelo relacional, ER, multi-tenancy (`clinica_id`), Compose base.
- Semilla de negocio: Elías (script).
- IdP listo al arrancar: Matías.

### B — Identidad y catálogo

- Realm/roles (Matías) + RF17 en API (validar JWT) cuando se implemente.
- RF1–RF6, RF14: catálogo/pacientes (por asignar en código de API).

### C — Citas, concurrencia y caché

- RF7–RF13, RF15–RF21, prueba de carrera.
- Redis / I8: **Cristian**.

## Módulo extra (obligatorio para 3)

Elegir exactamente uno. Propuesta: **RLS como segunda estrategia**.

## Convención de commits

Cada integrante debe tener commits propios. En archivos compartidos la autoría se considera conjunta.
