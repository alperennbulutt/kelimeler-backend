package dto

import "time"

type BadgeDTO struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	IsEarned bool   `json:"isEarned"`
	IconKey  string `json:"iconKey"`
	Category string `json:"category"` // level|streak|wordCount|boxMastery|behavior
}

type LearnedWordDTO struct {
	WordID         string     `json:"wordId"`
	English        string     `json:"english"`
	Turkish        string     `json:"turkish"`
	Box            int        `json:"box"`
	Level          string     `json:"level"`
	LastReviewDate *time.Time `json:"lastReviewDate"`
}

// StatsSummaryDTO, stats_repository.StatsSummary'nin birebir karşılığı.
type StatsSummaryDTO struct {
	TotalLearned      int              `json:"totalLearned"`
	CurrentStreak     int              `json:"currentStreak"`
	LearnedByLevel    map[string]int   `json:"learnedByLevel"` // box>=2 by level (UI "ilerleme")
	Box6ByLevel       map[string]int   `json:"box6ByLevel"`
	Box2Count         int              `json:"box2Count"`
	Box3Count         int              `json:"box3Count"`
	Box4Count         int              `json:"box4Count"`
	Box5Count         int              `json:"box5Count"`
	WeeklyLoginData   map[string]bool  `json:"weeklyLoginData"`
	Badges            []BadgeDTO       `json:"badges"`
	LastLearnedWords  []LearnedWordDTO `json:"lastLearnedWords"`
	TotalWordsByLevel map[string]int   `json:"totalWordsByLevel"`
	NewlyEarnedBadges []BadgeDTO       `json:"newlyEarnedBadges"`
}

type LearnedWordsPageDTO struct {
	Items []LearnedWordDTO `json:"items"`
	Total int              `json:"total"`
}
