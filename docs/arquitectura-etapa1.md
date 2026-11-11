# Arquitectura Etapa 1 — MediCitas

Mapa vivo del sistema **real** (Compose multi-servicio). No es el diagrama V2 de microservicios por entidad: aquí la prioridad es **consistencia** (Postgres fuente de verdad), luego **stateless** (JWT) y **tiempo de respuesta** (Redis solo para RF7).

## Vista general

```mermaid
flowchart TB
  subgraph clients [Clientes]
    curl[curl_HTTP_collections]
  end

  subgraph compose [Docker_Compose_varios_servicios]
    KC[keycloak_OIDC]
    API[api_Gin_stateless]
    PG[(postgres_fuente_verdad)]
    RD[(redis_cache_RF7)]
    SEED[seed_oneshot]
  end

  curl -->|"Bearer_JWT"| API
  curl -->|"password_grant"| KC
  KC -->|"iss_roles_clinica_id"| curl
  API -->|"JWKS_validar"| KC
  API -->|"SQL_tenant_idpSub"| PG
  SEED --> PG
  SEED --> KC
  API -.->|"pendiente_RF7"| RD

  subgraph identity [Identidad_hecho]
    auth[auth.go_JWT]
    perm[permissions.go_RF17]
    users[users.go_idpSub]
  end

  subgraph catalog [Catalogo_hecho]
    rf1[RF1_clinicas]
    rf2[RF2_usuarios]
    rf3[RF3_servicios]
    rf4[RF4_profesionales]
  end

  subgraph agendaDone [Agenda_hecho]
    rf5[RF5_horarios]
    rf6[RF6_bloqueos]
  end

  subgraph patients [Pacientes_hecho]
    rf14[RF14_CRUD_contactos]
    rf16[RF16_historial]
  end

  subgraph disponib [Disponibilidad_hecho]
    rf7[RF7_RF8_disponibilidad]
  end

  subgraph operacion [Operacion_diaria]
    rf20[RF20_agenda_dia]
    rf21[RF21_conteo_estados]
  end

  subgraph nextUp [Siguiente]
    redisWire[Redis_cableado_I8]
    rf9[RF9_10_reserva_carrera]
  end

  API --- identity
  API --- catalog
  API --- agendaDone
  API --- patients
  API --- disponib
  API --- operacion
  operacion -->|"SELECT_Cita"| PG
  rf7 --> PG
  redisWire -.-> RD
  rf9 -.-> PG
  rf9 -.-> rf7
```

## Tubo de una petición autenticada

1. Cliente obtiene JWT en Keycloak (`client_id=api`).
2. `api` valida firma (JWKS), `iss`, `exp`.
3. `requirePerm` / matriz RF17.
4. `loadDomainUser`: `"Usuario"."idpSub" = JWT.sub` → clínica de dominio.
5. Handler filtra por esa clínica (I5); nunca confía un `clinicaId` del query/body.

## Tabla de progreso (rellenable)

| Bloque | RF / pieza | Estado |
|--------|------------|--------|
| Compose | api, db, keycloak, redis, seed | Hecho |
| Identidad | JWT, matriz, idpSub, `/me` | Hecho |
| RF1–RF4 | Clínicas, usuarios, servicios, profesionales | Hecho |
| RF5–RF6 | Horarios, bloqueos | Hecho |
| RF14, RF16 | Pacientes/contactos, historial | Hecho |
| RF7–RF8 | Disponibilidad (API) | Hecho |
| **RF20** | Agenda del día | **Hecho** |
| **RF21** | Conteo por estado | **Hecho** |
| Redis I8 | Cableado caché en RF7 | Pendiente |
| RF9–RF13 | Ciclo de cita + carrera | Pendiente |
| RF15, RF18–RF19 | Notas, bitácora | Pendiente |
| Módulo extra | p. ej. RLS | Pendiente |

## Defensa en una frase

Varios servicios Compose; una fuente de verdad relacional; identidad delegada; caché solo donde el enunciado lo pide (RF7), sin contradecir Postgres (I8).
