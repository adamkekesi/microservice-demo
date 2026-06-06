package service

import (
	"context"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/adamkekesi/microservice-demo/auth/internal/model"
	"github.com/adamkekesi/microservice-demo/platform/apperror"
	"github.com/adamkekesi/microservice-demo/platform/authn"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// mockUserRepo is an in-memory UserRepository for unit tests.
type mockUserRepo struct {
	users map[string]*model.User
}

func newMockUserRepo() *mockUserRepo { return &mockUserRepo{users: map[string]*model.User{}} }

func (m *mockUserRepo) Create(_ context.Context, u *model.User) error {
	for _, e := range m.users {
		if e.Email == u.Email {
			return gorm.ErrDuplicatedKey
		}
	}
	cp := *u
	m.users[u.ID] = &cp
	return nil
}

func (m *mockUserRepo) GetByEmail(_ context.Context, email string) (*model.User, error) {
	for _, u := range m.users {
		if u.Email == email {
			cp := *u
			return &cp, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (m *mockUserRepo) GetByID(_ context.Context, id string) (*model.User, error) {
	if u, ok := m.users[id]; ok {
		cp := *u
		return &cp, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (m *mockUserRepo) AdminExists(_ context.Context) (bool, error) {
	for _, u := range m.users {
		if u.Role == authn.RoleAdmin {
			return true, nil
		}
	}
	return false, nil
}

func (m *mockUserRepo) DeleteByID(_ context.Context, id string) error {
	delete(m.users, id)
	return nil
}

func (m *mockUserRepo) PurgeCustomersBefore(_ context.Context, before time.Time) (int64, error) {
	var n int64
	for id, u := range m.users {
		if u.Role == authn.RoleCustomer && u.CreatedAt.Before(before) {
			delete(m.users, id)
			n++
		}
	}
	return n, nil
}

func newTestService(t *testing.T) (*Service, *authn.Signer) {
	t.Helper()
	key, err := authn.GenerateRSAKey(2048)
	require.NoError(t, err)
	signer := authn.NewSigner(key, "test-key", "auth-service", time.Minute)
	svc := New(newMockUserRepo(), signer)
	svc.bcryptCost = 4 // speed up hashing in tests
	return svc, signer
}

func requireAppError(t *testing.T, err error, code string, status int) {
	t.Helper()
	var ae *apperror.AppError
	require.ErrorAs(t, err, &ae)
	require.Equal(t, code, ae.Code)
	require.Equal(t, status, ae.Status)
}

func TestRegister_Success(t *testing.T) {
	svc, _ := newTestService(t)
	resp, err := svc.Register(context.Background(), model.RegisterRequest{Email: "Alice@Example.com", Password: "supersecret"})
	require.NoError(t, err)
	require.NotEmpty(t, resp.ID)
	require.Equal(t, "alice@example.com", resp.Email, "email must be lowercased")
	require.Equal(t, "customer", resp.Role, "register always assigns customer")
}

func TestRegister_DuplicateEmail(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Register(context.Background(), model.RegisterRequest{Email: "a@b.com", Password: "supersecret"})
	require.NoError(t, err)
	_, err = svc.Register(context.Background(), model.RegisterRequest{Email: "a@b.com", Password: "anothersecret"})
	requireAppError(t, err, "EMAIL_TAKEN", 409)
}

func TestRegister_Validation(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Register(context.Background(), model.RegisterRequest{Email: "not-an-email", Password: "supersecret"})
	requireAppError(t, err, apperror.CodeValidation, 400)

	_, err = svc.Register(context.Background(), model.RegisterRequest{Email: "a@b.com", Password: "short"})
	requireAppError(t, err, apperror.CodeValidation, 400)
}

func TestLogin_SuccessProducesVerifiableToken(t *testing.T) {
	svc, signer := newTestService(t)
	reg, err := svc.Register(context.Background(), model.RegisterRequest{Email: "c@d.com", Password: "supersecret"})
	require.NoError(t, err)

	resp, err := svc.Login(context.Background(), model.LoginRequest{Email: "c@d.com", Password: "supersecret"})
	require.NoError(t, err)
	require.NotEmpty(t, resp.AccessToken)
	require.Equal(t, "Bearer", resp.TokenType)
	require.Equal(t, 60, resp.ExpiresIn)

	// The token must verify with the signer's public key and carry sub + role.
	v := authn.NewLocalVerifier("auth-service", map[string]*rsa.PublicKey{
		signer.KeyID(): signer.PublicKey(),
	})
	claims, err := v.Verify(context.Background(), resp.AccessToken)
	require.NoError(t, err)
	require.Equal(t, reg.ID, claims.Subject())
	require.Equal(t, authn.RoleCustomer, claims.Role)
}

func TestLogin_InvalidCredentialsIdentical(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Register(context.Background(), model.RegisterRequest{Email: "e@f.com", Password: "supersecret"})
	require.NoError(t, err)

	_, errUnknown := svc.Login(context.Background(), model.LoginRequest{Email: "nobody@f.com", Password: "supersecret"})
	_, errWrongPw := svc.Login(context.Background(), model.LoginRequest{Email: "e@f.com", Password: "wrongpassword"})

	requireAppError(t, errUnknown, "INVALID_CREDENTIALS", 401)
	requireAppError(t, errWrongPw, "INVALID_CREDENTIALS", 401)
	require.Equal(t, errUnknown.Error(), errWrongPw.Error(), "unknown email and wrong password must be indistinguishable")
}

func TestCreateUser_RolesAndValidation(t *testing.T) {
	svc, _ := newTestService(t)
	for _, role := range []string{"customer", "operator", "admin"} {
		resp, err := svc.CreateUser(context.Background(), model.CreateUserRequest{
			Email: role + "@x.com", Password: "supersecret", Role: role,
		})
		require.NoError(t, err)
		require.Equal(t, role, resp.Role)
	}
	_, err := svc.CreateUser(context.Background(), model.CreateUserRequest{Email: "z@x.com", Password: "supersecret", Role: "superuser"})
	requireAppError(t, err, apperror.CodeValidation, 400)
}

func TestMe_NotFound(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Me(context.Background(), "00000000-0000-0000-0000-000000000000")
	requireAppError(t, err, apperror.CodeNotFound, 404)
}

func TestEnsureAdmin(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	require.NoError(t, svc.EnsureAdmin(ctx, "", ""), "no creds -> no-op")
	exists, _ := svc.users.AdminExists(ctx)
	require.False(t, exists)

	require.NoError(t, svc.EnsureAdmin(ctx, "admin@x.com", "supersecret"))
	exists, _ = svc.users.AdminExists(ctx)
	require.True(t, exists, "admin must be created when none exists")

	// Idempotent: a second call must not create a duplicate or error.
	require.NoError(t, svc.EnsureAdmin(ctx, "admin@x.com", "supersecret"))
}

// --- delete / purge ---

func TestDeleteUser_Success(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	u, err := svc.Register(ctx, model.RegisterRequest{Email: "c@x.com", Password: "supersecret"})
	require.NoError(t, err)

	require.NoError(t, svc.DeleteUser(ctx, u.ID))
	_, err = svc.Me(ctx, u.ID)
	requireAppError(t, err, apperror.CodeNotFound, 404)
}

func TestDeleteUser_NotFound(t *testing.T) {
	svc, _ := newTestService(t)
	err := svc.DeleteUser(context.Background(), "00000000-0000-0000-0000-000000000000")
	requireAppError(t, err, apperror.CodeNotFound, 404)
}

func TestDeleteUser_AdminProtected(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	admin, err := svc.CreateUser(ctx, model.CreateUserRequest{Email: "a@x.com", Password: "supersecret", Role: "admin"})
	require.NoError(t, err)

	err = svc.DeleteUser(ctx, admin.ID)
	requireAppError(t, err, "ADMIN_PROTECTED", 409)
	_, err = svc.Me(ctx, admin.ID)
	require.NoError(t, err, "admin must survive a delete attempt")
}

func TestPurgeUsers_InvalidBefore(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.PurgeUsers(context.Background(), "not-a-time")
	requireAppError(t, err, apperror.CodeValidation, 400)
}

func TestPurgeUsers_DeletesCustomersNotPrivileged(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	_, err := svc.Register(ctx, model.RegisterRequest{Email: "c1@x.com", Password: "supersecret"})
	require.NoError(t, err)
	_, err = svc.Register(ctx, model.RegisterRequest{Email: "c2@x.com", Password: "supersecret"})
	require.NoError(t, err)
	admin, err := svc.CreateUser(ctx, model.CreateUserRequest{Email: "a@x.com", Password: "supersecret", Role: "admin"})
	require.NoError(t, err)

	resp, err := svc.PurgeUsers(ctx, time.Now().Add(time.Hour).Format(time.RFC3339))
	require.NoError(t, err)
	require.EqualValues(t, 2, resp.Deleted, "both customers purged")
	_, err = svc.Me(ctx, admin.ID)
	require.NoError(t, err, "admin must not be purged")
}
