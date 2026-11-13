# Arquitectura Etapa 1 — MediCitas

Mapa vivo del sistema **real** (Compose multi-servicio). Prioridad: **consistencia** (Postgres), **stateless** (JWT), **latencia** (Redis RF7 sin contradecir PG: I8).

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
  API -->|"JWKS_validar"| KC
  API -->|"SQL_tenant_idpSub"| PG
  API -->|"GetOrLoad_RF7"| RD
  SEED --> PG
  SEED --> KC

  subgraph identity [Identidad]
    auth[JWT_matriz_idpSub]
  end

  subgraph dominio [Dominio_hecho]
    rf1to8[RF1_8_catalogo_agenda_dispo]
    rf14to21[RF14_21_pacientes_notas_ops]
    rf9to13[RF9_13_ciclo_cita]
  end

  subgraph extra [Modulo_extra]
    rls[RLS_segunda_estrategia]
  end

  API --- identity
  API --- dominio
  API -->|"app.clinica_id"| rls
  rls --> PG
  rf9to13 -->|"INSERT_UPDATE_reprogramarCita"| PG
  rf9to13 -->|"InvalidateAgenda"| RD
  rf1to8 --> PG
  rf1to8 --> RD
```

## Ciclo de la cita

```mermaid
stateDiagram-v2
  [*] --> reservada: POST_citas
  reservada --> reprogramada: reprogramar
  reprogramada --> reservada: nueva_cita
  reservada --> cancelada: cancelar
  reservada --> atendida: desenlace
  reservada --> ausente: desenlace
  atendida --> [*]
  ausente --> [*]
  cancelada --> [*]
  reprogramada --> [*]
```

Concurrencia RF9: **READ COMMITTED** + `EXCLUDE` GiST. RF10: `app.reprogramarCita` atómica. RF13: `validarTransicionCita` + filtros `estado = reservada` en API.

## Tabla de progreso

| Bloque | Estado |
|--------|--------|
| Compose + identidad | Hecho |
| RF1–RF8 | Hecho |
| RF14–RF16, RF15, RF18–RF21 | Hecho |
| Redis / I8 en RF7 | Hecho |
| **RF9–RF13 ciclo + carrera** | **Hecho** |
| Módulo extra: RLS | Hecho |

## Defensa en una frase

Compose multi-servicio; Postgres como verdad; JWT multi-tenant con RLS encima de `clinicaId`; reserva concurrente con una ganadora; caché de disponibilidad invalidada al mutar la agenda.
