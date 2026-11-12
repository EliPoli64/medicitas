package main

// Matriz de permisos RF17 (spec.md).
//
// Capa de autorización sobre el JWT ya validado: responde "¿este rol puede
// intentar la operación X?". No consulta Keycloak ni Postgres.
//
// Filas con "solo propios / propias" (horarios, notas): el rol profesional
// entra en la matriz; el handler del recurso debe comprobar después que el
// objeto le pertenece (profesionalId / cita atendida por él).

// Operation nombra una fila de la matriz (estable para rutas y tests).
type Operation string

const (
	OpAltaClinica                 Operation = "AltaClinica"
	OpGestionCatalogo             Operation = "GestionCatalogo"
	OpHorariosBloqueos            Operation = "HorariosBloqueos"
	OpConsultarDisponibilidad     Operation = "ConsultarDisponibilidad"
	OpReservarReprogramarCancelar Operation = "ReservarReprogramarCancelar"
	OpRegistrarDesenlace          Operation = "RegistrarDesenlace"
	OpPacientesEscritura          Operation = "PacientesEscritura"
	OpPacientesLectura            Operation = "PacientesLectura"
	OpNotaClinica                 Operation = "NotaClinica"
	OpHistorialPaciente           Operation = "HistorialPaciente"
	OpBitacora                    Operation = "Bitacora"
	// Extensión documentada de la matriz (operación diaria; no figuran en el PDF).
	OpAgendaDia   Operation = "AgendaDia"
	OpConteoCitas Operation = "ConteoCitas"

	RoleAdmPlataforma = "adm-plataforma"
	RoleAdmClinica    = "adm-clinica"
	RoleRecepcion     = "recepcion"
	RoleProfesional   = "profesional"
)

// matrix: operación → roles de realm que pueden ejecutarla (permiso base).
var matrix = map[Operation][]string{
	OpAltaClinica:                 {RoleAdmPlataforma},
	OpGestionCatalogo:             {RoleAdmClinica},
	OpHorariosBloqueos:            {RoleAdmClinica, RoleProfesional}, // profesional: solo propios (handler)
	OpConsultarDisponibilidad:     {RoleAdmClinica, RoleRecepcion, RoleProfesional},
	OpReservarReprogramarCancelar: {RoleAdmClinica, RoleRecepcion},
	OpRegistrarDesenlace:          {RoleAdmClinica, RoleRecepcion, RoleProfesional},
	OpPacientesEscritura:          {RoleAdmClinica, RoleRecepcion},
	OpPacientesLectura:            {RoleAdmClinica, RoleRecepcion, RoleProfesional},
	OpNotaClinica:                 {RoleProfesional}, // solo citas propias (handler)
	OpHistorialPaciente:           {RoleAdmClinica, RoleRecepcion, RoleProfesional},
	OpBitacora:                    {RoleAdmClinica},
	OpAgendaDia:                   {RoleAdmClinica, RoleRecepcion, RoleProfesional}, // profesional: solo propia (handler)
	OpConteoCitas:                 {RoleAdmClinica, RoleRecepcion, RoleProfesional}, // profesional: solo propias (handler)
}

// AllOperations orden estable para /permisos y tests.
func AllOperations() []Operation {
	return []Operation{
		OpAltaClinica,
		OpGestionCatalogo,
		OpHorariosBloqueos,
		OpConsultarDisponibilidad,
		OpReservarReprogramarCancelar,
		OpRegistrarDesenlace,
		OpPacientesEscritura,
		OpPacientesLectura,
		OpNotaClinica,
		OpHistorialPaciente,
		OpBitacora,
		OpAgendaDia,
		OpConteoCitas,
	}
}

// Can indica si alguno de los roles del JWT está autorizado a la operación.
func Can(roles []string, op Operation) bool {
	allowed, ok := matrix[op]
	if !ok {
		return false
	}
	for _, have := range roles {
		for _, want := range allowed {
			if have == want {
				return true
			}
		}
	}
	return false
}

// AllowedOperations filtra AllOperations con Can.
func AllowedOperations(roles []string) []Operation {
	out := make([]Operation, 0, len(matrix))
	for _, op := range AllOperations() {
		if Can(roles, op) {
			out = append(out, op)
		}
	}
	return out
}
