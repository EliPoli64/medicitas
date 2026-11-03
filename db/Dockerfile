# Imagen de PostgreSQL para MediCitas.
#
# Los scripts de db/migrations/ se ejecutan automáticamente la primera vez
# que se inicializa el volumen de datos (esquema, catálogos y funciones).
# Los datos de demostración los carga el job one-shot `seed/`.
# Para volver a ejecutar la migración hay que recrear el volumen.

FROM postgres:18-alpine

COPY migrations/ /docker-entrypoint-initdb.d/

EXPOSE 5432
