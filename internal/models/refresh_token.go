package models

import "time"

// RefreshToken, iptal edilebilir oturumlar için opak refresh token'ların
// hash'ini saklar (logout / hesap silme oturumu sonlandırabilsin diye).
type RefreshToken struct {
	ID        uint64     `gorm:"primaryKey" json:"-"`
	UserID    string     `gorm:"type:uuid;not null;index" json:"-"`
	TokenHash string     `gorm:"not null;uniqueIndex" json:"-"`
	ExpiresAt time.Time  `gorm:"not null" json:"-"`
	RevokedAt *time.Time `json:"-"`
	CreatedAt time.Time  `json:"-"`

	User User `gorm:"constraint:OnDelete:CASCADE;" json:"-"`
}
