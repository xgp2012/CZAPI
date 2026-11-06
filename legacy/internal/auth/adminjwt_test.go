package auth

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"czapi/internal/pkg/clock"
	"czapi/internal/pkg/errs"
)

const testJWTSecret = "test-jwt-secret-please-change"

func newJWT(t *testing.T) (*JWTIssuer, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	return NewJWTIssuer(JWTConfig{Secret: testJWTSecret, Clock: clk}), clk
}

func TestNewJWTIssuerDefaults(t *testing.T) {
	j := NewJWTIssuer(JWTConfig{Secret: "s"})
	assert.Equal(t, DefaultJWTIssuer, j.Issuer())
	assert.Equal(t, DefaultJWTTTL, j.TTL())

	j2 := NewJWTIssuer(JWTConfig{Issuer: "custom", TTL: time.Hour})
	assert.Equal(t, "custom", j2.Issuer())
	assert.Equal(t, time.Hour, j2.TTL())

	// A negative TTL is treated as "unset".
	assert.Equal(t, DefaultJWTTTL, NewJWTIssuer(JWTConfig{TTL: -time.Second}).TTL())
}

func TestJWTRoundTrip(t *testing.T) {
	j, clk := newJWT(t)
	token, exp, err := j.Issue(42, "alice", RoleOperator)
	require.NoError(t, err)
	require.NotEmpty(t, token)
	assert.Equal(t, clk.Now().Add(DefaultJWTTTL), exp)

	claims, err := j.Verify(token)
	require.NoError(t, err)
	require.NotNil(t, claims)
	assert.Equal(t, uint64(42), claims.AdminID)
	assert.Equal(t, "alice", claims.Username)
	assert.Equal(t, RoleOperator, claims.Role)
	assert.Equal(t, DefaultJWTIssuer, claims.Issuer)
	assert.Equal(t, exp.Unix(), claims.ExpiresAt.Unix())
	assert.Equal(t, clk.Now().Unix(), claims.IssuedAt.Unix())
	assert.NotEmpty(t, claims.ID, "jti must be present")
}

func TestJWTUniqueJTI(t *testing.T) {
	j, _ := newJWT(t)
	t1, _, err := j.Issue(1, "a", RoleAdmin)
	require.NoError(t, err)
	t2, _, err := j.Issue(1, "a", RoleAdmin)
	require.NoError(t, err)
	assert.NotEqual(t, t1, t2, "each token must carry a distinct jti/iat pair")

	c1, err := j.Verify(t1)
	require.NoError(t, err)
	c2, err := j.Verify(t2)
	require.NoError(t, err)
	assert.NotEqual(t, c1.ID, c2.ID)
}

func TestJWTWrongSecret(t *testing.T) {
	j, _ := newJWT(t)
	token, _, err := j.Issue(7, "bob", RoleAdmin)
	require.NoError(t, err)

	other := NewJWTIssuer(JWTConfig{Secret: "a-different-secret", Clock: clock.System{}})
	claims, err := other.Verify(token)
	require.Error(t, err)
	assert.Nil(t, claims)
	e := errs.As(err)
	assert.Equal(t, http.StatusUnauthorized, e.Status)
	assert.Equal(t, errs.CodeInvalidAPIKey, e.Code)
}

func TestJWTExpired(t *testing.T) {
	j, clk := newJWT(t)
	token, _, err := j.Issue(7, "bob", RoleViewer)
	require.NoError(t, err)

	_, err = j.Verify(token)
	require.NoError(t, err, "token must be valid before expiry")

	clk.Advance(DefaultJWTTTL + time.Minute)
	claims, err := j.Verify(token)
	require.Error(t, err)
	assert.Nil(t, claims)
	e := errs.As(err)
	assert.Equal(t, http.StatusUnauthorized, e.Status)
	assert.Equal(t, errs.CodeInvalidAPIKey, e.Code)
}

func TestJWTWrongIssuer(t *testing.T) {
	signer := NewJWTIssuer(JWTConfig{
		Secret: testJWTSecret,
		Issuer: "not-czapi",
		Clock:  clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)),
	})
	token, _, err := signer.Issue(7, "bob", RoleAdmin)
	require.NoError(t, err)

	j, _ := newJWT(t) // expects iss "czapi"
	claims, err := j.Verify(token)
	require.Error(t, err)
	assert.Nil(t, claims)
	assert.Equal(t, http.StatusUnauthorized, errs.As(err).Status)
}

func TestJWTMalformedAndEmpty(t *testing.T) {
	j, _ := newJWT(t)
	for _, tok := range []string{"", "   ", "not-a-jwt", "a.b.c"} {
		claims, err := j.Verify(tok)
		require.Error(t, err, "token %q must be rejected", tok)
		assert.Nil(t, claims)
		assert.Equal(t, http.StatusUnauthorized, errs.As(err).Status)
	}
}

func TestJWTAlgNoneRejected(t *testing.T) {
	j, _ := newJWT(t)
	// {"alg":"none","typ":"JWT"} / {"sub":"1","iss":"czapi","exp":9999999999}
	unsigned := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJzdWIiOiIxIiwiaXNzIjoiY3phcGkiLCJleHAiOjk5OTk5OTk5OTl9."
	claims, err := j.Verify(unsigned)
	require.Error(t, err)
	assert.Nil(t, claims)
	assert.Equal(t, http.StatusUnauthorized, errs.As(err).Status)
}

func TestIssueRejectsBadInput(t *testing.T) {
	_, _, err := NewJWTIssuer(JWTConfig{}).Issue(1, "a", RoleAdmin)
	require.Error(t, err, "empty secret must fail closed")
	assert.Equal(t, errs.CodeInternal, errs.As(err).Code)

	j, _ := newJWT(t)
	_, _, err = j.Issue(0, "a", RoleAdmin)
	require.Error(t, err)

	var nilIssuer *JWTIssuer
	_, _, err = nilIssuer.Issue(1, "a", RoleAdmin)
	require.Error(t, err)
	_, err = nilIssuer.Verify("x")
	require.Error(t, err)
}

func TestRequireRole(t *testing.T) {
	tests := []struct {
		name    string
		claims  *AdminClaims
		roles   []string
		wantErr bool
		status  int
	}{
		{name: "nil claims always fails", claims: nil, roles: []string{RoleAdmin}, wantErr: true, status: http.StatusUnauthorized},
		{name: "no restriction passes", claims: &AdminClaims{Role: RoleViewer}, roles: nil},
		{name: "matching role passes", claims: &AdminClaims{Role: RoleOperator}, roles: []string{RoleOperator}},
		{name: "one of several passes", claims: &AdminClaims{Role: RoleViewer}, roles: []string{RoleOperator, RoleViewer}},
		{name: "admin is superuser", claims: &AdminClaims{Role: RoleAdmin}, roles: []string{RoleOperator}, wantErr: false},
		{name: "mismatch fails", claims: &AdminClaims{Role: RoleViewer}, roles: []string{RoleOperator}, wantErr: true, status: http.StatusForbidden},
		{name: "empty claim role fails", claims: &AdminClaims{}, roles: []string{RoleAdmin}, wantErr: true, status: http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := RequireRole(tc.claims, tc.roles...)
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tc.status, errs.As(err).Status)
		})
	}
}
