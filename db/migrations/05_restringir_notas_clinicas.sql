
-- RF15: las notas clínicas son exclusivas del profesional.
-- Restringir los permisos SQL de recepción y administración.

REVOKE SELECT ON TABLE public."NotaClinica"
FROM medicitas_recepcion, medicitas_adm_clinica;
