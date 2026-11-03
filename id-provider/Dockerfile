# Imagen de Keycloak para MediCitas.
# Al arrancar con `start-dev --import-realm`, importa los JSON de import/
# (medicitas-realm.json: roles, client api, mapper clinica_id, users temporales).

FROM quay.io/keycloak/keycloak:26.0.6

COPY import/ /opt/keycloak/data/import/

EXPOSE 6767
