// Package repository owns all GORM data access for the Auth service. It returns
// raw GORM errors (gorm.ErrRecordNotFound, gorm.ErrDuplicatedKey); the service
// layer translates them into apperror values.
package repository

import (
	"context"

	"github.com/adamkekesi/microservice-demo/auth/internal/model"
	"github.com/adamkekesi/microservice-demo/platform/authn"
	"gorm.io/gorm"
)

// UserRepository abstracts user persistence so the service layer is unit-testable.
type UserRepository interface {
	Create(ctx context.Context, u *model.User) error
	GetByEmail(ctx context.Context, email string) (*model.User, error)
	GetByID(ctx context.Context, id string) (*model.User, error)
	AdminExists(ctx context.Context) (bool, error)
}

// SigningKeyRepository abstracts persistence of the active signing keypair.
type SigningKeyRepository interface {
	GetActive(ctx context.Context) (*model.SigningKey, error)
	Create(ctx context.Context, k *model.SigningKey) error
}

type userRepo struct{ db *gorm.DB }

// NewUserRepository builds a GORM-backed UserRepository.
func NewUserRepository(db *gorm.DB) UserRepository { return &userRepo{db: db} }

func (r *userRepo) Create(ctx context.Context, u *model.User) error {
	return r.db.WithContext(ctx).Create(u).Error
}

func (r *userRepo) GetByEmail(ctx context.Context, email string) (*model.User, error) {
	var u model.User
	if err := r.db.WithContext(ctx).Where("email = ?", email).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *userRepo) GetByID(ctx context.Context, id string) (*model.User, error) {
	var u model.User
	if err := r.db.WithContext(ctx).First(&u, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *userRepo) AdminExists(ctx context.Context) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&model.User{}).
		Where("role = ?", authn.RoleAdmin).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

type signingKeyRepo struct{ db *gorm.DB }

// NewSigningKeyRepository builds a GORM-backed SigningKeyRepository.
func NewSigningKeyRepository(db *gorm.DB) SigningKeyRepository { return &signingKeyRepo{db: db} }

func (r *signingKeyRepo) GetActive(ctx context.Context) (*model.SigningKey, error) {
	var k model.SigningKey
	if err := r.db.WithContext(ctx).Where("active = ?", true).
		Order("created_at DESC").First(&k).Error; err != nil {
		return nil, err
	}
	return &k, nil
}

func (r *signingKeyRepo) Create(ctx context.Context, k *model.SigningKey) error {
	return r.db.WithContext(ctx).Create(k).Error
}
