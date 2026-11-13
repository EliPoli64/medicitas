package rlsctx

import (
	"context"
	"testing"
)

func TestPickSinConexionUsaFallback(t *testing.T) {
	fallback := Queryer(nil)
	if got := Pick(context.Background(), fallback); got != fallback {
		t.Fatal("sin conexión en el contexto, Pick debe devolver el fallback")
	}
}
