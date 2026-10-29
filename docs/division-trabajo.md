# División del trabajo — propuesta para el grupo

> Propuesta de Matías. **No está asignada:** cada uno puede quedarse con un tercio (A, B o C), o lo partimos de otra forma si les parece mejor. Comenten y cierren el reparto.

Somos tres. La idea es que cada persona tenga un bloque claro (y commits propios), para el avance y para la defensa.

## Tercios propuestos

### A — Fundación (arranque / avance)

- Modelo relacional, ER, borrador 3FN, diccionario.
- Comparación escrita de multi-tenancy y decisión (`clinica_id`).
- Semilla: ≥ 3 clínicas.
- Compose base (api + db + keycloak + redis) cuando el grupo decida stack.
- Docs en `docs/` y commits tempranos en este repositorio.

**Quién:** _por asignar_

### B — Identidad y catálogo

- Realm Keycloak con roles de la matriz.
- RF1–RF4 (clínicas, usuarios, servicios, profesionales).
- RF5–RF6 (horarios y bloqueos).
- RF14 (pacientes).
- RF17 (authn/authz; clínica derivada de la identidad).

**Quién:** _por asignar_ (Elías u otro)

### C — Citas, concurrencia y caché

- RF7–RF8 (disponibilidad; índices).
- RF9–RF13 (ciclo de vida; pregunta rectora).
- RF15–RF16, RF18–RF21 (notas, historial, auditoría, agenda del día, conteos).
- Redis + invalidación (I8).
- Prueba de carrera reproducible (una sola ganadora; I1).

**Quién:** _por asignar_ (el otro compañero u otro)

## Si prefieren otro corte

Ejemplos válidos: uno toma solo modelo+docs, otro solo infra/Compose, otro API; o A+parte de B vs C más grande. Lo importante es que los tres tengan commits y que nadie quede sin pieza defendible.

## Módulo extra (obligatorio para 3)

Elegir exactamente uno. Propuesta: **RLS como segunda estrategia** (encaja con el multi-tenancy de A).

## Convención de commits

Cada integrante debe tener commits propios. En archivos compartidos la autoría se considera conjunta; un integrante sin commits se evalúa solo por lo demostrable.
