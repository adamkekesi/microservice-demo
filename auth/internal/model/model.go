// Package model holds the Auth service's GORM models and request/response DTOs.
package model

import (
	"time"

	"github.com/adamkekesi/microservice-demo/platform/authn"
)

// User is a registered account. Emails are stored lowercased and unique.
type User struct {
	ID           string     `gorm:"type:uuid;primaryKey"`
	Email        string     `gorm:"uniqueIndex;not null"`
	PasswordHash string     `gorm:"not null"`
	Role         authn.Role `gorm:"type:text;not null"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TableName pins the table name (GORM would otherwise pluralise).
func (User) TableName() string { return "users" }

// SigningKey persists the RSA keypair so the kid and public key survive
// restarts (Feature Spec §3.1 / §2.6).
type SigningKey struct {
	ID            string `gorm:"type:uuid;primaryKey"`
	Kid           string `gorm:"uniqueIndex;not null"`
	PrivateKeyPEM string `gorm:"not null"`
	PublicKeyPEM  string `gorm:"not null"`
	Active        bool   `gorm:"not null;default:true"`
	CreatedAt     time.Time
}

func (SigningKey) TableName() string { return "signing_keys" }

// --- DTOs ---

type RegisterRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type CreateUserRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

type UserResponse struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

type LoginResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// ToResponse projects a User to its public representation.
func (u *User) ToResponse() UserResponse {
	return UserResponse{ID: u.ID, Email: u.Email, Role: string(u.Role)}
}
