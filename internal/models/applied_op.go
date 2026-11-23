package models

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// AppliedOp, istemcinin kimlikli (opId) bir işleminin uygulandığının
// kaydıdır. Satır, işlemin kendi yazımlarıyla aynı transaction'da yazılır:
// kayıt varsa işlem uygulanmıştır ve aynı opId bir daha uygulanmaz; ikinci
// gelişte Result olduğu gibi döner.
//
// Çevrimdışı mod (bkz. POST /progress/sync) ve ağ hatasıyla yeniden gönderilen
// istekler için vardır: yanıtı kaybolan bir cevap aynı kimlikle yeniden
// gelirse kelime ikinci kez ilerlemez, sayaç ikinci kez artmaz.
type AppliedOp struct {
	UserID string `gorm:"type:uuid;primaryKey" json:"-"`
	OpID   string `gorm:"type:uuid;primaryKey" json:"opId"`
	// Kind, işlemin türüdür: learn, unknown, swipe, group_started,
	// advance_group, daily_login.
	Kind      string    `gorm:"not null" json:"kind"`
	AppliedAt time.Time `gorm:"not null;index:idx_applied_ops_applied_at" json:"appliedAt"`
	// Result, işlemin ilk sonucudur (JSON). PreferSimpleProtocol ile metin
	// olarak gider gelir; kolon jsonb'dir.
	Result string `gorm:"type:jsonb;not null" json:"-"`
	// ReviewDay, işlem bir tekrar cevabıysa (swipe öncesi kutu 2..5, uygulandı)
	// cevabın kullanım günüdür ("YYYY-MM-DD"); değilse boştur. Geçmiş bir günün
	// tekrar cevabı sayısını (raporlama sınırı) buradan sayarız.
	ReviewDay *string `gorm:"type:date" json:"-"`

	User User `gorm:"constraint:OnDelete:CASCADE;" json:"-"`
}

// AppliedOpRetention, uygulanmış işlem kayıtlarının saklandığı süredir.
// İstemci kuyruğu en çok birkaç gün bekler (answeredAt 7 günden eskiyse
// kırpılır); 30 gün, kaybolan yanıtların yeniden gönderimine geniş pay bırakır.
const AppliedOpRetention = 30 * 24 * time.Hour

// appliedOpsCleanupLockKey, temizliğin birden çok replikada aynı anda
// çalışmasını engelleyen advisory lock anahtarıdır (keyfi ama sabit).
const appliedOpsCleanupLockKey int64 = 0x6b656c2d6f7073 // "kel-ops"

// DeleteOldAppliedOps, retention'dan eski işlem kayıtlarını siler ve silinen
// satır sayısını döner. Birden çok replika aynı anda çağırırsa yalnızca biri
// siler (transaction ömürlü advisory lock); kilidi alamayan (0, nil) döner.
func DeleteOldAppliedOps(ctx context.Context, db *gorm.DB, retention time.Duration) (int64, error) {
	cutoff := time.Now().Add(-retention)
	var deleted int64
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked bool
		if err := tx.Raw("SELECT pg_try_advisory_xact_lock(?)", appliedOpsCleanupLockKey).
			Scan(&locked).Error; err != nil {
			return err
		}
		if !locked {
			return nil
		}
		res := tx.Where("applied_at < ?", cutoff).Delete(&AppliedOp{})
		deleted = res.RowsAffected
		return res.Error
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}
