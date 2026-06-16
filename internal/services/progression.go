package services

// Levels, seviye sırası (advanceToNextGroup ve kilit mantığı için).
var Levels = []string{"A1", "A2", "B1", "B2", "C1", "C2"}

func IndexOfLevel(level string) int {
	for i, l := range Levels {
		if l == level {
			return i
		}
	}
	return -1
}

// AdvanceGroup, mevcut seviye/grup ve seviyedeki max grup verildiğinde
// bir sonraki seviye/grup'u hesaplar (user_profile_repository.advanceToNextGroup portu).
// C2'nin son grubu bitince group=0 döner (tüm seviyeler tamamlandı işareti).
func AdvanceGroup(currentLevel string, currentGroup, maxGroupInCurrentLevel int) (nextLevel string, nextGroup int, levelChanged bool) {
	nextLevel = currentLevel
	nextGroup = currentGroup + 1

	if nextGroup > maxGroupInCurrentLevel {
		idx := IndexOfLevel(currentLevel)
		if idx >= 0 && idx < len(Levels)-1 {
			nextLevel = Levels[idx+1]
			nextGroup = 1
			levelChanged = true
		} else {
			// C2 bitti
			nextLevel = currentLevel
			nextGroup = 0
		}
	}
	return nextLevel, nextGroup, levelChanged
}
