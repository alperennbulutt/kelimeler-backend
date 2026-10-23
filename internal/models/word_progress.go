package models

import "time"

// WordProgress, Firestore'daki users/{uid}/wordProgress/{wordId} alt dökümanıdır.
// Leitner sisteminin çekirdeğidir. JSON şekli Flutter'daki UserWord.fromMap ile uyumludur
// (id = wordId; tarihler ISO-8601 string olarak serialize edilir).
type WordProgress struct {
	ID     uint64 `gorm:"primaryKey" json:"-"`
	UserID string `gorm:"type:uuid;not null;uniqueIndex:uq_user_word,priority:1;index:idx_wp_user_box,priority:1;index:idx_wp_user_level_box,priority:1;index:idx_wp_user_box_next,priority:1" json:"-"`
	WordID string `gorm:"not null;uniqueIndex:uq_user_word,priority:2" json:"wordId"`

	Level string `gorm:"not null;index:idx_wp_user_level_box,priority:2" json:"level"`
	Grp   int    `gorm:"column:grp;not null" json:"group"`
	Box   int    `gorm:"not null;default:1;index:idx_wp_user_box,priority:2;index:idx_wp_user_level_box,priority:3;index:idx_wp_user_box_next,priority:2" json:"box"` // 1..6

	LastReviewDate *time.Time `json:"lastReviewDate"`
	NextReviewDate *time.Time `gorm:"index:idx_wp_user_box_next,priority:3" json:"nextReviewDate"`

	// Art arda verilen yanlış cevap sayısı; doğru cevap sıfırlar. Kutuyu
	// etkilemez, yalnızca istatistik içindir (kural: services.OnLeftSwipe).
	WrongCount int    `gorm:"not null;default:0" json:"wrongCount"`
	Learned    bool   `gorm:"not null;default:false" json:"learned"`
	English    string `gorm:"not null;default:''" json:"english"`
	Turkish    string `gorm:"not null;default:''" json:"turkish"`

	CreatedAt time.Time `json:"-"`
	UpdatedAt time.Time `json:"-"`

	User User `gorm:"constraint:OnDelete:CASCADE;" json:"-"`
}
