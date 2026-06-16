package models

import "time"

// EarnedBadge, Firestore'daki users/{uid}/earnedBadges/{badgeId} alt dökümanıdır.
// badge_id örnekleri: "level_A1", "streak_7", "words_100", "box_mastery_10", "first_day".
type EarnedBadge struct {
	ID       uint64    `gorm:"primaryKey" json:"-"`
	UserID   string    `gorm:"type:uuid;not null;uniqueIndex:uq_user_badge,priority:1" json:"-"`
	BadgeID  string    `gorm:"not null;uniqueIndex:uq_user_badge,priority:2" json:"id"`
	EarnedAt time.Time `json:"earnedAt"`

	User User `gorm:"constraint:OnDelete:CASCADE;" json:"-"`
}
