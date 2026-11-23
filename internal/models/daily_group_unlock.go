package models

import "time"

// DailyGroupUnlock, bir grubun bir kullanım gününde açıldığının kaydıdır
// (kullanıcı, gün, seviye, grup başına bir satır). users.unlocked_groups_today
// yalnızca bugünü tutar; bu tablo günlük geçmiştir. Çevrimdışı verilmiş
// geçmiş günün öğrenme cevapları o günün kayıtlarına göre denetlenir.
//
// usage_date bir "date" kolonudur; PreferSimpleProtocol yüzünden gün
// "YYYY-MM-DD" metni olarak yazılır (CAST(? AS date), bkz. services.UsageDate).
type DailyGroupUnlock struct {
	ID        uint64    `gorm:"primaryKey"`
	UserID    string    `gorm:"type:uuid;not null;uniqueIndex:uq_daily_group_unlock,priority:1"`
	UsageDate time.Time `gorm:"type:date;not null;uniqueIndex:uq_daily_group_unlock,priority:2"`
	Level     string    `gorm:"not null;uniqueIndex:uq_daily_group_unlock,priority:3"`
	Grp       int       `gorm:"column:grp;not null;uniqueIndex:uq_daily_group_unlock,priority:4"`
	// Via, grubun nasıl açıldığıdır (UnlockVia...).
	Via       string    `gorm:"not null;check:chk_daily_group_unlock_via,via IN ('free','ad','nofill','offline_free')"`
	CreatedAt time.Time `gorm:"not null"`

	User User `gorm:"constraint:OnDelete:CASCADE;"`
}

// DailyGroupUnlock.Via değerleri.
const (
	// UnlockViaFree: günün ücretsiz grubu (POST /users/me/usage/group-started).
	UnlockViaFree = "free"
	// UnlockViaAd: reklamla açılan grup (POST /users/me/usage/group-unlocked-by-ad).
	UnlockViaAd = "ad"
	// UnlockViaNoFill: reklam yüklenemediği için tolerans geçişiyle açılan
	// grup. Yayındaki istemci no-fill geçişinden sonra da group-unlocked-by-ad
	// çağırır; sunucu ikisini ayıramadığı için bugün yazılmaz, ileride
	// kullanılmak üzere ayrılmıştır.
	UnlockViaNoFill = "nofill"
	// UnlockViaOfflineFree: çevrimdışı geçmiş bir günde açılmış ücretsiz grup
	// (POST /progress/sync, group_started).
	UnlockViaOfflineFree = "offline_free"
)
