package models

import "time"

// DictionaryPatchApplied, açılışta uygulanmış bir sözlük yamasının kaydıdır
// (bkz. db.ApplyDictionaryPatches). Satır, yamanın kelime güncellemeleriyle
// aynı transaction'da yazılır: kayıt varsa yamanın tamamı uygulanmıştır ve
// yama bir daha uygulanmaz.
type DictionaryPatchApplied struct {
	// Name, yama dosyasının uzantısız adıdır (ör. "0001_kritik_ve_a1").
	Name string `gorm:"primaryKey"`
	// Checksum, uygulandığı andaki dosya içeriğinin SHA-256'sıdır (hex).
	// Uygulanmış bir yamanın dosyası sonradan değişirse açılışta uyarı verilir.
	Checksum string `gorm:"not null"`
	// Words, yamadaki kart sayısı; Updated, uygulanırken içeriği gerçekten
	// değişen kart sayısıdır (seed zaten yeni değerleri taşıyorsa 0).
	Words     int       `gorm:"not null"`
	Updated   int       `gorm:"not null"`
	AppliedAt time.Time `gorm:"not null"`
}

// TableName, GORM'un çoğul ad üretimini ("dictionary_patch_applieds") ezer.
func (DictionaryPatchApplied) TableName() string { return "dictionary_patches_applied" }
