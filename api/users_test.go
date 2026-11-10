package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

type fakeUserStore struct {
	bySub map[string]DomainUser
	err   error
}

func (f *fakeUserStore) FindByIdpSub(_ context.Context, idpSub string) (DomainUser, error) {
	if f.err != nil {
		return DomainUser{}, f.err
	}
	u, ok := f.bySub[idpSub]
	if !ok {
		return DomainUser{}, errUserNotFound
	}
	if !u.Activo {
		return u, errUserInactive
	}
	return u, nil
}

func TestLoadDomainUser_OK(t *testing.T) {
	gin.SetMode(gin.TestMode)
	clinica := "clinica-norte"
	prev := userStore
	userStore = &fakeUserStore{bySub: map[string]DomainUser{
		"sub-1": {
			UsuarioID:            "uuid-1",
			IdpSub:               "sub-1",
			Activo:               true,
			ClinicaIdentificador: &clinica,
			RolCodigo:            "recepcion",
		},
	}}
	t.Cleanup(func() { userStore = prev })

	r := gin.New()
	r.GET("/t", func(c *gin.Context) {
		c.Set(ctxIdentityKey, Identity{Sub: "sub-1", Username: "recep-norte", Roles: []string{"recepcion"}})
		c.Next()
	}, loadDomainUser(), func(c *gin.Context) {
		du, ok := domainUserFromContext(c)
		if !ok {
			t.Fatal("domain user missing")
		}
		c.JSON(http.StatusOK, gin.H{"usuarioId": du.UsuarioID, "rolCodigo": du.RolCodigo})
	})

	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestLoadDomainUser_NotFound403(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prev := userStore
	userStore = &fakeUserStore{bySub: map[string]DomainUser{}}
	t.Cleanup(func() { userStore = prev })

	r := gin.New()
	r.GET("/t", func(c *gin.Context) {
		c.Set(ctxIdentityKey, Identity{Sub: "unknown"})
		c.Next()
	}, loadDomainUser(), func(c *gin.Context) {
		t.Fatal("handler should not run")
	})

	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 body=%s", w.Code, w.Body.String())
	}
}

func TestLoadDomainUser_Inactive403(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prev := userStore
	userStore = &fakeUserStore{bySub: map[string]DomainUser{
		"sub-x": {UsuarioID: "u", IdpSub: "sub-x", Activo: false, RolCodigo: "recepcion"},
	}}
	t.Cleanup(func() { userStore = prev })

	r := gin.New()
	r.GET("/t", func(c *gin.Context) {
		c.Set(ctxIdentityKey, Identity{Sub: "sub-x"})
		c.Next()
	}, loadDomainUser(), func(c *gin.Context) {
		t.Fatal("handler should not run")
	})

	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 body=%s", w.Code, w.Body.String())
	}
}

func TestLoadDomainUser_StoreError500(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prev := userStore
	userStore = &fakeUserStore{err: errors.New("boom")}
	t.Cleanup(func() { userStore = prev })

	r := gin.New()
	r.GET("/t", func(c *gin.Context) {
		c.Set(ctxIdentityKey, Identity{Sub: "sub-1"})
		c.Next()
	}, loadDomainUser(), func(c *gin.Context) {
		t.Fatal("handler should not run")
	})

	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", w.Code, w.Body.String())
	}
}

func TestFakeStore_FindByIdpSub(t *testing.T) {
	clinica := "clinica-norte"
	store := &fakeUserStore{bySub: map[string]DomainUser{
		"abc": {
			UsuarioID:            "1",
			IdpSub:               "abc",
			Activo:               true,
			ClinicaIdentificador: &clinica,
			RolCodigo:            "recepcion",
		},
	}}
	u, err := store.FindByIdpSub(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if u.UsuarioID != "1" || u.RolCodigo != "recepcion" {
		t.Fatalf("unexpected user: %+v", u)
	}
	_, err = store.FindByIdpSub(context.Background(), "missing")
	if !errors.Is(err, errUserNotFound) {
		t.Fatalf("want errUserNotFound, got %v", err)
	}
}
