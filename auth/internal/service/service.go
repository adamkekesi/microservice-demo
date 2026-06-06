// Package service holds the Auth business logic. It has no Gin/HTTP types so it
// is unit-testable with a mocked repository (technical plan §2.3).
package service

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/adamkekesi/microservice-demo/auth/internal/model"
	"github.com/adamkekesi/microservice-demo/auth/internal/repository"
	"github.com/adamkekesi/microservice-demo/platform/apperror"
	"github.com/adamkekesi/microservice-demo/platform/authn"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const minPasswordLen = 8

// Service implements the Auth use cases.
type Service struct {
	users      repository.UserRepository
	signer     *authn.Signer
	bcryptCost int
}

// New builds an Auth service.
func New(users repository.UserRepository, signer *authn.Signer) *Service {
	return &Service{users: users, signer: signer, bcryptCost: bcrypt.DefaultCost}
}

// Signer exposes the signer so the router can publish JWKS / build a verifier.
func (s *Service) Signer() *authn.Signer { return s.signer }

// Register creates a customer account.
func (s *Service) Register(ctx context.Context, req model.RegisterRequest) (*model.UserResponse, error) {
	email, err := normalizeEmail(req.Email)
	if err != nil {
		return nil, err
	}
	if err := validatePassword(req.Password); err != nil {
		return nil, err
	}
	return s.create(ctx, email, req.Password, authn.RoleCustomer)
}

// CreateUser creates a user with an explicit role (admin-only; enforced by the
// route's role guard).
func (s *Service) CreateUser(ctx context.Context, req model.CreateUserRequest) (*model.UserResponse, error) {
	email, err := normalizeEmail(req.Email)
	if err != nil {
		return nil, err
	}
	if err := validatePassword(req.Password); err != nil {
		return nil, err
	}
	role := authn.Role(req.Role)
	if !role.Valid() {
		return nil, apperror.Validation("invalid role", map[string]any{
			"role": "must be one of customer, operator, admin",
		})
	}
	return s.create(ctx, email, req.Password, role)
}

func (s *Service) create(ctx context.Context, email, password string, role authn.Role) (*model.UserResponse, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.bcryptCost)
	if err != nil {
		return nil, apperror.Internal("could not hash password").Wrap(err)
	}
	u := &model.User{
		ID:           uuid.NewString(),
		Email:        email,
		PasswordHash: string(hash),
		Role:         role,
	}
	if err := s.users.Create(ctx, u); err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, apperror.Conflict("EMAIL_TAKEN", "an account with this email already exists")
		}
		return nil, apperror.Internal("could not create user").Wrap(err)
	}
	resp := u.ToResponse()
	return &resp, nil
}

// Login verifies credentials and returns a signed access token. Unknown email
// and wrong password produce an identical INVALID_CREDENTIALS error.
func (s *Service) Login(ctx context.Context, req model.LoginRequest) (*model.LoginResponse, error) {
	email, err := normalizeEmail(req.Email)
	if err != nil {
		// Treat a malformed email as bad credentials, not a validation error,
		// to avoid revealing which inputs are well-formed.
		return nil, invalidCredentials()
	}
	u, err := s.users.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, invalidCredentials()
		}
		return nil, apperror.Internal("could not load user").Wrap(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)) != nil {
		return nil, invalidCredentials()
	}
	token, expiresIn, err := s.signer.Sign(u.ID, u.Role)
	if err != nil {
		return nil, apperror.Internal("could not sign token").Wrap(err)
	}
	return &model.LoginResponse{AccessToken: token, TokenType: "Bearer", ExpiresIn: expiresIn}, nil
}

// Me returns the authenticated user's profile.
func (s *Service) Me(ctx context.Context, userID string) (*model.UserResponse, error) {
	u, err := s.users.GetByID(ctx, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperror.NotFound("user not found")
		}
		return nil, apperror.Internal("could not load user").Wrap(err)
	}
	resp := u.ToResponse()
	return &resp, nil
}

// DeleteUser removes a user by id (admin-only; enforced by the route guard).
// Admin accounts are protected so the seed admin can't be locked out.
func (s *Service) DeleteUser(ctx context.Context, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return apperror.NotFound("user not found")
	}
	u, err := s.users.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperror.NotFound("user not found")
		}
		return apperror.Internal("could not load user").Wrap(err)
	}
	if u.Role == authn.RoleAdmin {
		return apperror.Conflict("ADMIN_PROTECTED", "cannot delete an admin user")
	}
	if err := s.users.DeleteByID(ctx, id); err != nil {
		return apperror.Internal("could not delete user").Wrap(err)
	}
	return nil
}

// PurgeUsers bulk-deletes customer accounts created before `before` (RFC3339).
// Admin-only (enforced by the route guard). Operators and admins are never
// touched. This is the hourly retention sweep for throwaway load-test users.
func (s *Service) PurgeUsers(ctx context.Context, before string) (*model.PurgeResponse, error) {
	cutoff, err := time.Parse(time.RFC3339, before)
	if err != nil {
		return nil, apperror.Validation("before must be an RFC3339 timestamp",
			map[string]any{"before": "required, RFC3339"})
	}
	n, err := s.users.PurgeCustomersBefore(ctx, cutoff)
	if err != nil {
		return nil, apperror.Internal("could not purge users").Wrap(err)
	}
	return &model.PurgeResponse{Deleted: n}, nil
}

// EnsureAdmin creates the seed admin if none exists (Feature Spec §3.3).
func (s *Service) EnsureAdmin(ctx context.Context, email, password string) error {
	if email == "" || password == "" {
		return nil
	}
	exists, err := s.users.AdminExists(ctx)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	normalized, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	if _, err := s.create(ctx, normalized, password, authn.RoleAdmin); err != nil {
		// A pre-existing (non-admin) account with this email is not fatal at boot.
		var ae *apperror.AppError
		if errors.As(err, &ae) && ae.Code == "EMAIL_TAKEN" {
			return nil
		}
		return err
	}
	return nil
}

func invalidCredentials() error {
	return apperror.Unauthorized("INVALID_CREDENTIALS", "invalid email or password")
}

func normalizeEmail(raw string) (string, error) {
	email := strings.TrimSpace(strings.ToLower(raw))
	if email == "" {
		return "", apperror.Validation("email is required", map[string]any{"email": "required"})
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return "", apperror.Validation("invalid email format", map[string]any{"email": "must be a valid email address"})
	}
	return email, nil
}

func validatePassword(pw string) error {
	if len(pw) < minPasswordLen {
		return apperror.Validation("password too short", map[string]any{
			"password": "must be at least 8 characters",
		})
	}
	return nil
}

// ResolveSigner returns a Signer, in priority order: (1) a configured private
// key PEM, (2) the active key persisted in the DB, (3) a freshly generated key
// that is then persisted — so the kid and public key stay stable across
// restarts (Feature Spec §2.6 note).
func ResolveSigner(
	ctx context.Context,
	keys repository.SigningKeyRepository,
	configuredPEM, kid, issuer string,
	ttl time.Duration,
) (*authn.Signer, error) {
	if strings.TrimSpace(configuredPEM) != "" {
		key, err := authn.ParseRSAPrivateKeyPEM(configuredPEM)
		if err != nil {
			return nil, err
		}
		return authn.NewSigner(key, kid, issuer, ttl), nil
	}

	existing, err := keys.GetActive(ctx)
	if err == nil {
		key, perr := authn.ParseRSAPrivateKeyPEM(existing.PrivateKeyPEM)
		if perr != nil {
			return nil, perr
		}
		return authn.NewSigner(key, existing.Kid, issuer, ttl), nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// No key anywhere: generate and persist one.
	key, err := authn.GenerateRSAKey(2048)
	if err != nil {
		return nil, err
	}
	pubPEM, err := authn.EncodePublicKeyPEM(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	record := &model.SigningKey{
		ID:            uuid.NewString(),
		Kid:           kid,
		PrivateKeyPEM: authn.EncodePrivateKeyPEM(key),
		PublicKeyPEM:  pubPEM,
		Active:        true,
	}
	if err := keys.Create(ctx, record); err != nil {
		return nil, err
	}
	return authn.NewSigner(key, kid, issuer, ttl), nil
}
