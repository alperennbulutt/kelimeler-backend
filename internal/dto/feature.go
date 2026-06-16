package dto

// --- Progress ---

type LearnRequest struct {
	WordID  string `json:"wordId"`
	Level   string `json:"level"`
	Group   int    `json:"group"`
	English string `json:"english"`
	Turkish string `json:"turkish"`
}

type SwipeRequest struct {
	WordID    string `json:"wordId"`
	Direction string `json:"direction"` // "right" | "left"
}

// UpcomingCounts, UserWordRepository.getUpcomingCountsForBox dönüşünün karşılığı.
// dailyCounts JSON'da string-anahtarlı olur (Flutter int'e parse eder).
type UpcomingCounts struct {
	Today           int            `json:"today"`
	Tomorrow        int            `json:"tomorrow"`
	Later           int            `json:"later"`
	DailyCounts     map[string]int `json:"dailyCounts"`
	TotalMonthCount int            `json:"totalMonthCount"`
	BusiestDayLabel string         `json:"busiestDayLabel"`
	BusiestDayCount int            `json:"busiestDayCount"`
}

// --- Users ---

type UpdateProfileRequest struct {
	DisplayName *string `json:"displayName"`
}

type SetCurrentGroupRequest struct {
	Level       string  `json:"level"`
	Group       int     `json:"group"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	TotalWords  *int    `json:"totalWords"`
}

type DailyUsageDTO struct {
	GroupsStartedTodayCount int   `json:"groupsStartedTodayCount"`
	GroupsUnlockedByAdToday int   `json:"groupsUnlockedByAdToday"`
	ReviewAdPassCountToday  int   `json:"reviewAdPassCountToday"`
	LastResetDate           int64 `json:"lastResetDate"`
}

type CurrentGroupDTO struct {
	Level       string  `json:"level"`
	Group       int     `json:"group"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
	TotalWords  int     `json:"totalWords"`
}

// --- Map ---

type LevelGroupDTO struct {
	GroupNumber     int  `json:"groupNumber"`
	TotalWords      int  `json:"totalWords"`
	RemainingInBox1 int  `json:"remainingInBox1"`
	IsCompleted     bool `json:"isCompleted"`
	IsUnlocked      bool `json:"isUnlocked"`
	HasStarted      bool `json:"hasStarted"`
}
