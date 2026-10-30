# Esqueleto de la imagen de Keycloak.
# El realm de MediCitas (roles, clientes y usuarios de prueba) se versionará
# en id-provider/import/ y se importará al arrancar el contenedor.

FROM quay.io/keycloak/keycloak:26.0.6

COPY import/ /opt/keycloak/data/import/

EXPOSE 6767
