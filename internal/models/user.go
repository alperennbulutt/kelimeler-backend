package models

import "time"

// User, Firestore'daki users/{uid} dökümanının karşılığıdır.
// ID (UUID), Firebase uid'in yerini alır ve JWT sub claim'inde taşınır.
type User struct {
	ID    string `gorm:"type:uuid;default:gen_random_uuid();primaryKey" json:"id"`
	Email string `gorm:"type:citext;uniqueIndex;not null" json:"email"`

	DisplayName             string  `gorm:"not null;default:''" json:"displayName"`
	CurrentLevel            string  `gorm:"not null;default:'A1'" json:"currentLevel"`
	CurrentGroup            int     `gorm:"not null;default:1" json:"currentGroup"` // 0 = tüm seviyeler bitti
	CurrentGroupTitle       *string `json:"currentGroupTitle,omitempty"`
	CurrentGroupDescription *string `json:"currentGroupDescription,omitempty"`
	CurrentGroupTotalWords  int     `gorm:"not null;default:50" json:"currentGroupTotalWords"`
	TotalLearned            int     `gorm:"not null;default:0" json:"totalLearned"`

	// Günlük kullanım sayaçları. Gün, services.UsageDayStart'a göre döner
	// (varsayılan Europe/Istanbul gece yarısı); sıfırlama zamanlanmış bir iş
	// değildir, gün dönümünden sonraki ilk kullanım isteğinde yapılır.
	GroupsStartedTodayCount int `gorm:"not null;default:0" json:"groupsStartedTodayCount"`
	GroupsUnlockedByAdToday int `gorm:"not null;default:0" json:"groupsUnlockedByAdToday"`
	ReviewAdPassCountToday  int `gorm:"not null;default:0" json:"reviewAdPassCountToday"`

	// Bugün verilen tekrar cevabı sayısı (swipe öncesi kutu 2..5).
	ReviewAnswersToday int `gorm:"not null;default:0" json:"reviewAnswersToday"`

	// Bugün açılmış (ücretsiz ya da reklamla) gruplar: virgülle ayrılmış
	// "SEVIYE:GRUP" anahtarları, ör. "A1:3,A1:4". İstemciye dizi olarak
	// dto.DailyUsageDTO üzerinden döner.
	UnlockedGroupsToday string `gorm:"not null;default:''" json:"-"`

	// Reklam hiç yüklenemediğinde (no-fill) verilen tolerans geçişleri.
	// Günlük üst sınırı services.MaxNoFillPassesPerDay belirler.
	NoFillPassesToday int `gorm:"not null;default:0" json:"noFillPassesToday"`

	LastResetDate int64 `gorm:"not null;default:0" json:"lastResetDate"` // unix millis

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
