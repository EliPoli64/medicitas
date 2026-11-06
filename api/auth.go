package main

// Autenticación y autorización (RF17 — núcleo).
//
// La identidad se delega en Keycloak: la API valida la firma del token OIDC
// contra las llaves públicas (JWKS), comprueba iss/exp y lee roles + clinica_id.
// La clínica se deriva del token, nunca de un parámetro del cliente.

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// KC_ISSUER_URL = host externo (localhost): es el `iss` de los tokens.
// KC_JWKS_URL = nombre DNS interno (keycloak): de ahí se leen las llaves.
var (
	kcIssuer  = os.Getenv("KC_ISSUER_URL")
	kcJWKSURL = os.Getenv("KC_JWKS_URL")
)

var jwksCache = struct {
	sync.RWMutex
	keys       map[string]*rsa.PublicKey
	lastFetch  time.Time
	httpClient *http.Client
}{
	keys:       make(map[string]*rsa.PublicKey),
	httpClient: &http.Client{Timeout: 5 * time.Second},
}

const (
	jwksRefreshInterval = 10 * time.Minute
	tokenLeeway         = 60 * time.Second

	ctxIdentityKey = "identity"
)

// Identity es lo que las rutas leen del JWT ya validado.
type Identity struct {
	Sub      string   `json:"sub"`
	Username string   `json:"username"`
	Roles    []string `json:"roles"`
	ClinicaID string  `json:"clinicaId"` // vacío si adm-plataforma
}

type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type jwksDoc struct {
	Keys []jwk `json:"keys"`
}

func fetchJWKS() error {
	if kcJWKSURL == "" {
		return fmt.Errorf("KC_JWKS_URL no está configurado")
	}
	resp, err := jwksCache.httpClient.Get(kcJWKSURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("JWKS respondió %d", resp.StatusCode)
	}
	var doc jwksDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return err
	}
	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		e := new(big.Int).SetBytes(eBytes).Int64()
		if e <= 0 {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{
			N: new(big.Int).SetBytes(nBytes),
			E: int(e),
		}
	}
	if len(keys) == 0 {
		return fmt.Errorf("JWKS no trajo llaves RSA utilizables")
	}
	jwksCache.Lock()
	jwksCache.keys = keys
	jwksCache.lastFetch = time.Now()
	jwksCache.Unlock()
	return nil
}

func jwksKey(kid string) (*rsa.PublicKey, error) {
	jwksCache.RLock()
	key, ok := jwksCache.keys[kid]
	fresh := time.Since(jwksCache.lastFetch) < jwksRefreshInterval
	jwksCache.RUnlock()
	if ok && fresh {
		return key, nil
	}
	if err := fetchJWKS(); err != nil {
		if ok {
			log.Printf("auth: no se pudo refrescar JWKS (%v), usando caché", err)
			return key, nil
		}
		return nil, err
	}
	jwksCache.RLock()
	defer jwksCache.RUnlock()
	key, ok = jwksCache.keys[kid]
	if !ok {
		return nil, fmt.Errorf("kid desconocido: %s", kid)
	}
	return key, nil
}

func verifyToken(tokenString string) (jwt.MapClaims, error) {
	if kcIssuer == "" {
		return nil, fmt.Errorf("KC_ISSUER_URL no está configurado")
	}
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(tokenString, claims,
		func(t *jwt.Token) (any, error) {
			kid, _ := t.Header["kid"].(string)
			if kid == "" {
				return nil, fmt.Errorf("token sin kid")
			}
			return jwksKey(kid)
		},
		jwt.WithLeeway(tokenLeeway),
		jwt.WithIssuer(kcIssuer),
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
	)
	if err != nil {
		return nil, err
	}
	return claims, nil
}

func hasRole(claims jwt.MapClaims, role string) bool {
	for _, r := range rolesFromClaims(claims) {
		if r == role {
			return true
		}
	}
	return false
}

func rolesFromClaims(claims jwt.MapClaims) []string {
	access, ok := claims["realm_access"].(map[string]any)
	if !ok {
		return nil
	}
	roles, ok := access["roles"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		if s, ok := r.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func identityFromClaims(claims jwt.MapClaims) Identity {
	id := Identity{
		Roles: rolesFromClaims(claims),
	}
	if s, ok := claims["sub"].(string); ok {
		id.Sub = s
	}
	if s, ok := claims["preferred_username"].(string); ok {
		id.Username = s
	}
	// Mapper oidc-usermodel-attribute: string simple; a veces llega como []any.
	switch v := claims["clinica_id"].(type) {
	case string:
		id.ClinicaID = v
	case []any:
		if len(v) > 0 {
			if s, ok := v[0].(string); ok {
				id.ClinicaID = s
			}
		}
	}
	return id
}

func parseBearer(c *gin.Context) (jwt.MapClaims, error) {
	h := c.GetHeader("Authorization")
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" || parts[1] == "" {
		return nil, fmt.Errorf("se requiere token Bearer")
	}
	return verifyToken(parts[1])
}

// authRequired exige un JWT válido (cualquier rol). 401 si falta o es inválido.
// Deja Identity en el contexto Gin.
func authRequired() gin.HandlerFunc {
	if kcIssuer == "" || kcJWKSURL == "" {
		log.Printf("auth: falta KC_ISSUER_URL o KC_JWKS_URL en el entorno")
	}
	return func(c *gin.Context) {
		claims, err := parseBearer(c)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "token inválido o ausente"})
			return
		}
		c.Set(ctxIdentityKey, identityFromClaims(claims))
		c.Next()
	}
}

// requireRole exige JWT válido y el rol de realm indicado. 401 / 403.
func requireRole(requiredRole string) gin.HandlerFunc {
	if kcIssuer == "" || kcJWKSURL == "" {
		log.Printf("auth: falta KC_ISSUER_URL o KC_JWKS_URL en el entorno")
	}
	return func(c *gin.Context) {
		claims, err := parseBearer(c)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "token inválido o ausente"})
			return
		}
		if !hasRole(claims, requiredRole) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "sin el rol requerido"})
			return
		}
		c.Set(ctxIdentityKey, identityFromClaims(claims))
		c.Next()
	}
}

func identityFromContext(c *gin.Context) (Identity, bool) {
	v, ok := c.Get(ctxIdentityKey)
	if !ok {
		return Identity{}, false
	}
	id, ok := v.(Identity)
	return id, ok
}

func authConfigured() bool {
	return kcIssuer != "" && kcJWKSURL != ""
}
