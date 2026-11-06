package auth

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"czapi/internal/pkg/clock"
	"czapi/internal/pkg/errs"
	"czapi/internal/pkg/idgen"
)

// Defaults applied by NewJWTIssuer when the config leaves them empty.
const (
	DefaultJWTIssuer = "czapi"
	DefaultJWTTTL    = 12 * time.Hour
)

// Role names understood by RequireRole.
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"
)

// JWTConfig configures the admin token issuer.
type JWTConfig struct {
	// Secret is the HS256 signing key. An empty secret makes Issue and Verify
	// fail rather than silently accepting everything.
	Secret string
	// Issuer goes into the iss claim and is required to match on Verify.
	// Empty means DefaultJWTIssuer.
	Issuer string
	// TTL is the token lifetime. ≤0 means DefaultJWTTTL.
	TTL time.Duration
	// Clock supplies iat/exp timestamps. nil means clock.System{}.
	Clock clock.Clock
}

// AdminClaims are the claims carried by an admin access token.
type AdminClaims struct {
	AdminID  uint64 `json:"sub"`
	Username string `json:"username"`
	Role     string `json:"role"`
	// Issuer, ExpiresAt, IssuedAt and ID mirror the registered claims.
	Issuer    string
	ExpiresAt time.Time
	IssuedAt  time.Time
	ID        string
}

// JWTIssuer issues and verifies HS256 admin tokens.
// Safe for concurrent use.
type JWTIssuer struct {
	secret string
	issuer string
	ttl    time.Duration
	clock  clock.Clock
}

// NewJWTIssuer builds a JWTIssuer, applying defaults for zero-valued options.
func NewJWTIssuer(cfg JWTConfig) *JWTIssuer {
	if cfg.Issuer == "" {
		cfg.Issuer = DefaultJWTIssuer
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultJWTTTL
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.System{}
	}
	return &JWTIssuer{secret: cfg.Secret, issuer: cfg.Issuer, ttl: cfg.TTL, clock: cfg.Clock}
}

// Issuer returns the configured iss value.
func (j *JWTIssuer) Issuer() string { return j.issuer }

// TTL returns the configured token lifetime.
func (j *JWTIssuer) TTL() time.Duration { return j.ttl }

// adminClaims is the wire representation of AdminClaims.
type adminClaims struct {
	Role     string `json:"role,omitempty"`
	Username string `json:"username,omitempty"`
	jwt.RegisteredClaims
}

// Issue mints a token for adminID and returns the token plus its expiry.
// Claims: sub (admin id), iss, exp, iat, jti, role.
func (j *JWTIssuer) Issue(adminID uint64, username, role string) (token string, expiresAt time.Time, err error) {
	if j == nil || j.secret == "" {
		return "", time.Time{}, errs.Internal("auth: admin JWT secret is not configured", nil)
	}
	if adminID == 0 {
		return "", time.Time{}, errs.Internal("auth: admin id must be positive", nil)
	}
	now := j.clock.Now()
	expiresAt = now.Add(j.ttl)
	claims := adminClaims{
		Role:     role,
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatUint(adminID, 10),
			Issuer:    j.issuer,
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ID:        idgen.ULID(),
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(j.secret))
	if err != nil {
		return "", time.Time{}, errs.Internal("auth: sign admin token", err)
	}
	return signed, expiresAt, nil
}

// Verify parses and validates token and returns its claims. Signature, algorithm,
// issuer and expiry failures all map to HTTP 401 with code invalid_api_key.
func (j *JWTIssuer) Verify(token string) (*AdminClaims, error) {
	if j == nil || j.secret == "" {
		return nil, errs.Internal("auth: admin JWT secret is not configured", nil)
	}
	if strings.TrimSpace(token) == "" {
		return nil, errs.InvalidAPIKey()
	}
	claims := &adminClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("auth: unexpected signing method")
		}
		return []byte(j.secret), nil
	},
		jwt.WithIssuer(j.issuer),
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(j.clock.Now),
	)
	if err != nil || parsed == nil || !parsed.Valid {
		return nil, errs.New(http.StatusUnauthorized, errs.CodeInvalidAPIKey, "Invalid admin token.")
	}
	id, convErr := strconv.ParseUint(claims.Subject, 10, 64)
	if convErr != nil || id == 0 {
		return nil, errs.New(http.StatusUnauthorized, errs.CodeInvalidAPIKey, "Invalid admin token.")
	}
	out := &AdminClaims{
		AdminID:  id,
		Username: claims.Username,
		Role:     claims.Role,
		Issuer:   claims.Issuer,
		ID:       claims.ID,
	}
	if claims.ExpiresAt != nil {
		out.ExpiresAt = claims.ExpiresAt.Time
	}
	if claims.IssuedAt != nil {
		out.IssuedAt = claims.IssuedAt.Time
	}
	return out, nil
}

// RequireRole checks that claims carry one of roles. An empty role list means
// "any authenticated admin". "admin" always satisfies the check because it is the
// superuser role. Missing or mismatching claims yield HTTP 403.
func RequireRole(claims *AdminClaims, roles ...string) error {
	if claims == nil {
		return errs.New(http.StatusUnauthorized, errs.CodeInvalidAPIKey, "Missing admin token.")
	}
	if len(roles) == 0 || claims.Role == RoleAdmin {
		return nil
	}
	for _, want := range roles {
		if want != "" && want == claims.Role {
			return nil
		}
	}
	return errs.New(http.StatusForbidden, errs.CodeInvalidRequest, "Insufficient privileges for this operation.")
}
