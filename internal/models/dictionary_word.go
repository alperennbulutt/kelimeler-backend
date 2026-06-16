package models

import "time"

// DictionaryWord, Firestore'daki dictionaryWords/{wordId} dökümanının karşılığıdır.
// ID, orijinal Firestore doc id'sidir (ör. "A1_1_1") — import idempotent kalsın diye korunur.
// SQL'de rezerve olan group/order, DB'de grp/word_order olarak adlandırılır ama
// JSON tag'leri sayesinde Flutter'ın gördüğü alan adları değişmez.
type DictionaryWord struct {
	ID        string     `gorm:"primaryKey" json:"id"`
	English   string     `gorm:"not null;default:''" json:"english"`
	Turkish   string     `gorm:"not null;default:''" json:"turkish"`
	Example   string     `gorm:"not null;default:''" json:"example"`
	Level     string     `gorm:"not null;index:idx_dict_level_grp_order,priority:1" json:"level"`
	Grp       int        `gorm:"column:grp;not null;index:idx_dict_level_grp_order,priority:2" json:"group"`
	WordOrder int        `gorm:"column:word_order;not null;index:idx_dict_level_grp_order,priority:3" json:"order"`
	CreatedAt *time.Time `json:"createdAt,omitempty"`
}
