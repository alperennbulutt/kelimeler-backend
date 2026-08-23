package models

import "time"

// AuthIdentity, bir kullanıcının her giriş yöntemi için bir satır tutar.
// Firebase Auth'taki providerData'nın karşılığıdır.
type AuthIdentity struct {
	ID     uint64 `gorm:"primaryKey" json:"id"`
	UserID string `gorm:"type:uuid;not null;index;uniqueIndex:uq_user_provider,priority:1" json:"userId"`

	// 'password' | 'google' | 'apple'
	Provider string `gorm:"not null;uniqueIndex:uq_user_provider,priority:2;uniqueIndex:uq_provider_subject,priority:1" json:"provider"`

	// Google/Apple 'sub' değeri. password için null.
	ProviderSubject *string `gorm:"uniqueIndex:uq_provider_subject,priority:2" json:"providerSubject,omitempty"`

	// bcrypt hash, yalnızca provider='password' için dolu.
	PasswordHash *string `json:"-"`

	// Sağlayıcının bildirdiği email.
	Email *string `gorm:"type:citext" json:"email,omitempty"`

	// Apple'ın refresh token'ı, yalnızca provider='apple' için dolu.
	// Hesap silinirken Apple'a revoke isteği göndermek için saklanır;
	// istemciye asla dönülmez.
	AppleRefreshToken *string `json:"-"`

	CreatedAt time.Time `json:"createdAt"`

	// CASCADE: kullanıcı silinince kimlikleri de silinir.
	User User `gorm:"constraint:OnDelete:CASCADE;" json:"-"`
}
