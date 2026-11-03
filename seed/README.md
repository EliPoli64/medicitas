# Semilla de MediCitas

Job one-shot que prepara la demo después de que PostgreSQL y Keycloak estén sanos.
No expone HTTP: carga datos, crea/verifica usuarios y termina.

## Qué crea

| Entidad | Cantidad | Detalle |
|---------|---------:|---------|
| Clínicas | 3 | `clinica-norte`, `clinica-centro`, `clinica-sur` |
| Users Keycloak | 13 | 1 plataforma, 3 adm-clínica, 3 recepción, 6 profesionales |
| Profesionales | 6 | 2 por clínica, con especialidad y servicio |
| Servicios | 6 | 2 por clínica |
| Horarios | 60 | lunes a viernes, 09:00–12:00 y 13:00–17:00 |
| Pacientes | 12 | 4 por clínica, con teléfono y email |
| Citas | 30 | 10 por clínica |
| Notas | 9 | solo en citas atendidas |

Las citas se generan con fechas relativas al día de ejecución: atendidas a −7, −14 y
−21 días; ausente a −3; cancelada a −5; futuras a +1, +3 y +5; y una reprogramación
por clínica (+7 14:00 → +7 15:00). Los fines de semana se desplazan al día hábil más
cercano. Las horas se interpretan en `America/Costa_Rica`.

## Users y passwords

| User | Rol | Clínica | Password |
|------|-----|---------|----------|
| `plat-admin` | `adm-plataforma` | — | `plat123` (del realm) |
| `recep-norte` | `recepcion` | `clinica-norte` | `recep123` (del realm) |
| `adm-norte`, `adm-centro`, `adm-sur` | `adm-clinica` | su clínica | `SEED_USER_PASSWORD` |
| `recep-centro`, `recep-sur` | `recepcion` | su clínica | `SEED_USER_PASSWORD` |
| `dra-garcia`, `dr-rojas` | `profesional` | `clinica-norte` | `SEED_USER_PASSWORD` |
| `dra-jimenez`, `dr-mora` | `profesional` | `clinica-centro` | `SEED_USER_PASSWORD` |
| `dra-vargas`, `dr-solano` | `profesional` | `clinica-sur` | `SEED_USER_PASSWORD` |

Los users existentes se actualizan sin enviar `credentials`, por lo que su password
no se reinicia. El atributo `clinica_id` se guarda con el `identificador` de la
clínica y se omite para `plat-admin`.

## Idempotencia

Antes de escribir, el job toma un advisory lock de PostgreSQL y revisa
`ops."Semilla"` para `SEED_VERSION` (por defecto `demo-v1`).

- Primera ejecución: crea todo y registra la versión en `ops."Semilla"`.
- Ejecuciones siguientes: salen con código 0 sin tocar datos ni users.
- No hay `--force`. Para reiniciar desde cero:

```bash
docker compose down -v
docker compose up --build
```

Si se cambia el contenido de la semilla y se quiere volver a aplicar sobre el mismo
volumen, hay que subir `SEED_VERSION` (o recrear el volumen).

## Verificación rápida

```bash
docker compose exec db psql -U medicitas -d medicitas \
  -c 'SELECT "estado", count(*) FROM "Cita" GROUP BY 1 ORDER BY 1;'
```

Resultado esperado: 12 `reservada`, 9 `atendida`, 3 `ausente`, 3 `cancelada` y
3 `reprogramada`.

Token de un profesional nuevo:

```bash
curl -s -X POST "http://localhost:6767/realms/medicitas/protocol/openid-connect/token" \
  -d "client_id=api" -d "username=dra-garcia" \
  -d "password=$SEED_USER_PASSWORD" -d "grant_type=password" \
  -d "scope=openid"
```

El access token debe incluir `realm_access.roles=["profesional"]` y
`clinica_id="clinica-norte"`.
