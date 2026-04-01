package repository

import (
	"github.com/exbanka/auth-service/internal/model"
	"gorm.io/gorm"
)

type MobileActivationRepository struct {
	db *gorm.DB
}

func NewMobileActivationRepository(db *gorm.DB) *MobileActivationRepository {
	return &MobileActivationRepository{db: db}
}

func (r *MobileActivationRepository) Create(code *model.MobileActivationCode) error {
	return r.db.Create(code).Error
}

func (r *MobileActivationRepository) GetLatestByEmail(email string) (*model.MobileActivationCode, error) {
	var code model.MobileActivationCode
	if err := r.db.Where("email = ? AND used = false", email).
		Order("id DESC").First(&code).Error; err != nil {
		return nil, err
	}
	return &code, nil
}

func (r *MobileActivationRepository) IncrementAttempts(id int64) error {
	return r.db.Model(&model.MobileActivationCode{}).
		Where("id = ?", id).
		Update("attempts", gorm.Expr("attempts + 1")).Error
}

func (r *MobileActivationRepository) MarkUsed(id int64) error {
	return r.db.Model(&model.MobileActivationCode{}).
		Where("id = ?", id).
		Update("used", true).Error
}
