package main

import "testing"

func TestCanMatrixCells(t *testing.T) {
	type cell struct {
		op    Operation
		role  string
		allow bool
	}
	cases := []cell{
		// AltaClinica: solo plataforma
		{OpAltaClinica, RoleAdmPlataforma, true},
		{OpAltaClinica, RoleAdmClinica, false},
		{OpAltaClinica, RoleRecepcion, false},
		{OpAltaClinica, RoleProfesional, false},

		// GestionCatalogo: solo adm clínica
		{OpGestionCatalogo, RoleAdmClinica, true},
		{OpGestionCatalogo, RoleAdmPlataforma, false},
		{OpGestionCatalogo, RoleRecepcion, false},

		// HorariosBloqueos: clínica + profesional (propios en handler)
		{OpHorariosBloqueos, RoleAdmClinica, true},
		{OpHorariosBloqueos, RoleProfesional, true},
		{OpHorariosBloqueos, RoleRecepcion, false},

		// Disponibilidad
		{OpConsultarDisponibilidad, RoleRecepcion, true},
		{OpConsultarDisponibilidad, RoleProfesional, true},
		{OpConsultarDisponibilidad, RoleAdmPlataforma, false},

		// Reservar…
		{OpReservarReprogramarCancelar, RoleRecepcion, true},
		{OpReservarReprogramarCancelar, RoleAdmClinica, true},
		{OpReservarReprogramarCancelar, RoleProfesional, false},
		{OpReservarReprogramarCancelar, RoleAdmPlataforma, false},

		// Desenlace
		{OpRegistrarDesenlace, RoleProfesional, true},
		{OpRegistrarDesenlace, RoleRecepcion, true},

		// Pacientes
		{OpPacientesEscritura, RoleRecepcion, true},
		{OpPacientesEscritura, RoleProfesional, false},
		{OpPacientesLectura, RoleProfesional, true},

		// Nota / historial / bitácora
		{OpNotaClinica, RoleProfesional, true},
		{OpNotaClinica, RoleAdmClinica, false},
		{OpNotaClinica, RoleRecepcion, false},
		{OpHistorialPaciente, RoleRecepcion, true},
		{OpHistorialPaciente, RoleProfesional, true},
		{OpBitacora, RoleAdmClinica, true},
		{OpBitacora, RoleRecepcion, false},
		{OpBitacora, RoleAdmPlataforma, false},
	}
	for _, tc := range cases {
		got := Can([]string{tc.role}, tc.op)
		if got != tc.allow {
			t.Errorf("Can(%s, %s)=%v, want %v", tc.role, tc.op, got, tc.allow)
		}
	}
}

func TestCanUnknownOperation(t *testing.T) {
	if Can([]string{RoleAdmClinica}, Operation("NoExiste")) {
		t.Fatal("operación desconocida no debe permitirse")
	}
}

func TestCanAnyRoleInToken(t *testing.T) {
	// Si el token trajera varios roles, basta con uno permitido.
	if !Can([]string{"otro", RoleRecepcion}, OpReservarReprogramarCancelar) {
		t.Fatal("debería permitir con recepción entre varios roles")
	}
}

func TestAllowedOperationsRecepcion(t *testing.T) {
	ops := AllowedOperations([]string{RoleRecepcion})
	has := func(want Operation) bool {
		for _, o := range ops {
			if o == want {
				return true
			}
		}
		return false
	}
	if !has(OpReservarReprogramarCancelar) {
		t.Fatal("recepción debería poder reservar")
	}
	if has(OpAltaClinica) {
		t.Fatal("recepción no debería dar de alta clínicas")
	}
	if has(OpBitacora) {
		t.Fatal("recepción no debería ver bitácora")
	}
	if has(OpNotaClinica) {
		t.Fatal("recepción no debería escribir notas")
	}
}

func TestAllowedOperationsPlataforma(t *testing.T) {
	ops := AllowedOperations([]string{RoleAdmPlataforma})
	has := func(want Operation) bool {
		for _, o := range ops {
			if o == want {
				return true
			}
		}
		return false
	}
	if !has(OpAltaClinica) {
		t.Fatal("plataforma debería AltaClinica")
	}
	if has(OpReservarReprogramarCancelar) {
		t.Fatal("plataforma no reserva citas")
	}
}

func TestAllOperationsCoverMatrix(t *testing.T) {
	listed := AllOperations()
	if len(listed) != len(matrix) {
		t.Fatalf("AllOperations=%d matrix=%d", len(listed), len(matrix))
	}
	for _, op := range listed {
		if _, ok := matrix[op]; !ok {
			t.Fatalf("%s en AllOperations pero no en matrix", op)
		}
	}
}
