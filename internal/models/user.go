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

	// Günlük kullanım sayaçları (her gün 00:00'da sıfırlanır).
	GroupsStartedTodayCount int   `gorm:"not null;default:0" json:"groupsStartedTodayCount"`
	GroupsUnlockedByAdToday int   `gorm:"not null;default:0" json:"groupsUnlockedByAdToday"`
	ReviewAdPassCountToday  int   `gorm:"not null;default:0" json:"reviewAdPassCountToday"`
	LastResetDate           int64 `gorm:"not null;default:0" json:"lastResetDate"` // unix millis

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
