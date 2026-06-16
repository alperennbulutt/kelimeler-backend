package models

import "time"

// DailyLogin, Firestore'daki users/{uid}/dailyLogins/{YYYY-MM-DD} alt dökümanıdır.
// Streak (üst üste giriş) hesabı için kullanılır.
type DailyLogin struct {
	ID        uint64    `gorm:"primaryKey" json:"-"`
	UserID    string    `gorm:"type:uuid;not null;uniqueIndex:uq_user_date,priority:1;index:idx_dl_user_date,priority:1,sort:desc" json:"-"`
	LoginDate time.Time `gorm:"type:date;not null;uniqueIndex:uq_user_date,priority:2;index:idx_dl_user_date,priority:2,sort:desc" json:"date"`
	CreatedAt time.Time `json:"createdAt"`

	User User `gorm:"constraint:OnDelete:CASCADE;" json:"-"`
}
