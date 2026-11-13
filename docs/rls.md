# RLS como segunda estrategia

La estrategia principal sigue siendo la columna `clinicaId`, filtrada en la API a partir del usuario de dominio (`idpSub` del JWT). Esta capa no se quitó.

RLS es la segunda estrategia, encendida en [`db/migrations/03_rls.sql`](../db/migrations/03_rls.sql). Si un `SELECT` se olvida el `WHERE`, el motor igual no devuelve filas de otra clínica.

## Por qué la API no entra como superusuario

`POSTGRES_USER` es superusuario. Un superusuario **ignora** Row-Level Security aunque la tabla tenga `FORCE ROW LEVEL SECURITY`. Migraciones y el job `seed` siguen con ese usuario, para poder cargar datos. La API entra como `medicitas_app` (`LOGIN`, `NOSUPERUSER`, `NOBYPASSRLS`, `NOINHERIT`). Compose lo apunta con `APP_DB_USER` / `APP_DB_PASSWORD` (en local, ambos `medicitas_app`).

Ese login solo puede leer `"Usuario"`, `"Clinica"` y `"Rol"` para resolver la sesión. Después `SET ROLE` baja al perfil de la matriz ([`04_db_roles_matrix.sql`](../db/migrations/04_db_roles_matrix.sql)):

| Código de `"Rol"` | Perfil SQL | Privilegio |
|-------------------|------------|------------|
| `adm_plataforma` | `medicitas_adm_plataforma` | Alta de clínica y de su primer administrador (RF1) |
| `adm_clinica` | `medicitas_adm_clinica` | Catálogo, agenda, pacientes, citas y lectura de la bitácora |
| `recepcion` | `medicitas_recepcion` | Pacientes, citas y disponibilidad. Sin catálogo ni bitácora |
| `profesional` | `medicitas_profesional` | Horarios, bloqueos, desenlace y notas. Pacientes solo lectura |

`"Auditoria"` no tiene `UPDATE` ni `DELETE` en ningún perfil. `"Bloqueo"` no tiene `UPDATE`. `"AgendaVersion"` la mueven funciones `SECURITY DEFINER`, no un `GRANT` de escritura.

## Qué ve cada sesión

En la conexión de la petición la API fija tres variables:

| Variable | Cuándo | Efecto |
|----------|--------|--------|
| `app.idp_sub` | Antes de buscar `"Usuario"` | La política deja ver la fila propia para resolver `/me` |
| `app.clinica_id` | Después, con el uuid de dominio | El resto de tablas solo muestra esa clínica |
| `app.rol` | El código de `"Rol"` (`adm_plataforma`, …) | `adm_plataforma` ve todas las clínicas |

Predicado común, `app.tenant_visible(clinica)`: el rol es plataforma, o el uuid coincide con `app.clinica_id`. `"Usuario"` además acepta `idpSub = app.idp_sub` en `USING` (no en `WITH CHECK`: no se puede insertar en otra clínica copiando el subject). `"Clinica"` también deja ver la clínica de ese `idpSub`, porque el `JOIN` de `/me` ocurre antes de conocer el uuid.

Catálogos globales (`Rol`, `TipoContacto`, `Especialidad`) no tienen RLS.

GUC vacío → cero filas de tenant. Es fallo cerrado.

## La misma conexión

`set_config` en una conexión del pool no alcanza: la siguiente sentencia puede salir por otra. `loadDomainUser` toma una `*sql.Conn`, fija las variables ahí y la guarda en el contexto. Los stores consultan esa conexión.

No es `SET LOCAL`. Cada store hace `COMMIT` de su propio hecho y **después** invalida Redis (I8). Un `SET LOCAL` moriría en ese `COMMIT` y el resto de la petición quedaría sin clínica. `SET ROLE` tampoco es local: vale para toda la conexión. Al terminar la petición se hace `RESET ROLE` y `set_config` a vacío antes de devolver la conexión al pool. Si ese reset falla, la conexión se descarta.

## Comparación sobre la misma consulta

`SELECT count(*) FROM "Paciente" WHERE "clinicaId" = $1`, 100 veces, en `TestIntegrationRLS`:

| Camino | Qué pasa |
|--------|----------|
| Superusuario | RLS no aplica. El `WHERE` es el único filtro (estrategia de columna). |
| `medicitas_recepcion` con `app.clinica_id` de norte | La misma consulta. Además, el `WHERE` de la clínica sur devuelve 0. |
| `medicitas_recepcion` sin GUC | `count(*)` de `"Paciente"` es 0. |
| `medicitas_adm_plataforma` | Ve las clínicas (RF1). No puede leer `"Paciente"`. |
| `INSERT` de un paciente de sur con GUC de norte | El motor rechaza (`42501`). |
| Recepción `INSERT` en `"Servicio"`, profesional `SELECT` en `"Auditoria"` | El motor rechaza (`42501`): el perfil no tiene ese privilegio. |

La API sigue ignorando `?clinicaId=sur` (columna + identidad). Con RLS, ese filtro ya no es la única barrera.

Medición de una corrida (`TestIntegrationRLS`, 100 repeticiones, un paciente en la clínica):

- RLS (`medicitas_app`): 68 ms
- Superusuario + `WHERE`: 65 ms

El número sale en el log del test como `RLS_MEDICION`. No es un benchmark de carga: la política y el filtro de columna cuestan casi lo mismo en este volumen.
