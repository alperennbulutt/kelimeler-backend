package models

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// RefreshToken, iptal edilebilir oturumlar için opak refresh token'ların
// hash'ini saklar (logout / hesap silme oturumu sonlandırabilsin diye).
type RefreshToken struct {
	ID        uint64 `gorm:"primaryKey" json:"-"`
	UserID    string `gorm:"type:uuid;not null;index" json:"-"`
	TokenHash string `gorm:"not null;uniqueIndex" json:"-"`

	// FamilyID, aynı girişten rotasyonla türeyen token'ları bir oturumda
	// toplar; yeniden kullanım görülünce ailenin tamamı iptal edilir. Bu kolon
	// eklenmeden önce yazılan satırlarda boştur, ilk rotasyonda doldurulur.
	FamilyID *string `gorm:"size:32;index" json:"-"`

	// ReplacedByID, rotasyonla bu token'ın yerine geçen satırdır. Yanıtı
	// kaybolan bir yenilemenin tolerans süresinde yeniden denenebilmesi için
	// halefin hiç kullanılmadığını buradan anlarız. Logout ile iptal edilen
	// satırda boştur.
	ReplacedByID *uint64 `json:"-"`

	ExpiresAt time.Time  `gorm:"not null" json:"-"`
	RevokedAt *time.Time `json:"-"`
	CreatedAt time.Time  `json:"-"`

	User User `gorm:"constraint:OnDelete:CASCADE;" json:"-"`
}

// refreshTokenCleanupLockKey, temizlik işinin birden çok replikada aynı anda
// çalışmasını engelleyen advisory lock anahtarıdır (keyfi ama sabit).
const refreshTokenCleanupLockKey int64 = 0x6b656c696d6572 // "kelimer"

// MinRefreshTokenRetention, temizlikte iptal edilmiş satırların en az ne kadar
// tutulacağıdır. Rotasyon toleransı ve yeniden kullanım tespiti iptal edilmiş
// satırı okur; daha kısa saklamak ikisini de anlamsız kılardı.
const MinRefreshTokenRetention = time.Hour

// DeleteStaleRefreshTokens, süresi retention'dan uzun zaman önce dolmuş ya da
// retention'dan uzun zaman önce iptal edilmiş refresh token satırlarını siler
// ve silinen satır sayısını döner.
//
// Birden çok replika aynı anda çağırırsa yalnızca biri siler (transaction
// ömürlü advisory lock); kilidi alamayan (0, nil) döner. Saatlik bir
// zamanlayıcıdan çağrılmak üzere tasarlanmıştır.
func DeleteStaleRefreshTokens(ctx context.Context, db *gorm.DB, retention time.Duration) (int64, error) {
	if retention < MinRefreshTokenRetention {
		retention = MinRefreshTokenRetention
	}
	cutoff := time.Now().Add(-retention)

	var deleted int64
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked bool
		if err := tx.Raw("SELECT pg_try_advisory_xact_lock(?)", refreshTokenCleanupLockKey).
			Scan(&locked).Error; err != nil {
			return err
		}
		if !locked {
			return nil // başka bir replika temizliyor
		}
		res := tx.Where("expires_at < ? OR revoked_at < ?", cutoff, cutoff).
			Delete(&RefreshToken{})
		deleted = res.RowsAffected
		return res.Error
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}
