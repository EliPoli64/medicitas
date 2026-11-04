// Package availabilitycache contiene la caché de disponibilidad de MediCitas
// (RF7/I8).
//
// El paquete es deliberadamente independiente de HTTP, PostgreSQL y Keycloak.
// Su responsabilidad es envolver el cálculo de disponibilidad con una política
// de caché explícita y versionada; la fuente de verdad sigue siendo el motor
// relacional.
//
// Solo se cachean espacios de disponibilidad acotados por clínica. No se
// almacenan datos de pacientes ni notas clínicas.
package availabilitycache
