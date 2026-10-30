# Esqueleto de la imagen de PostgreSQL.
# Los scripts de esquema, roles y datos semilla se agregarán en db/migrations/.

FROM postgres:18-alpine

COPY migrations/ /docker-entrypoint-initdb.d/

EXPOSE 5432
