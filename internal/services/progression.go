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
//
// Konum değişmeyecekse girdiyi aynen döndürür: C2 tamamlanmışsa (group<=0)
// başa sarılmaz, tanınmayan seviyede de işaretçiye dokunulmaz.
func AdvanceGroup(currentLevel string, currentGroup, maxGroupInCurrentLevel int) (nextLevel string, nextGroup int, levelChanged bool) {
	idx := IndexOfLevel(currentLevel)
	if idx < 0 || IsAllLevelsCompleted(currentLevel, currentGroup) {
		return currentLevel, currentGroup, false
	}
	nextLevel = currentLevel
	nextGroup = currentGroup + 1
	if nextGroup < 1 {
		nextGroup = 1
	}

	if nextGroup > maxGroupInCurrentLevel {
		if idx < len(Levels)-1 {
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

// IsAllLevelsCompleted, işaretçinin "tüm seviyeler bitti" (son seviye, grup 0)
// durumunda olup olmadığını söyler.
func IsAllLevelsCompleted(level string, group int) bool {
	return level == Levels[len(Levels)-1] && group <= 0
}

// CompareGroupPositions, iki işaretçiyi seviye sırasına, sonra gruba göre
// karşılaştırır: a gerideyse -1, aynıysa 0, ilerideyse 1.
//
// "Tüm seviyeler bitti" her konumun ötesindedir. Tanınmayan seviye her
// konumun gerisindedir (eski, doğrulamasız yazımlardan kalmış olabilir).
func CompareGroupPositions(aLevel string, aGroup int, bLevel string, bGroup int) int {
	al, ag := groupPosition(aLevel, aGroup)
	bl, bg := groupPosition(bLevel, bGroup)
	switch {
	case al != bl:
		return sign(al - bl)
	default:
		return sign(ag - bg)
	}
}

func groupPosition(level string, group int) (levelRank, groupRank int) {
	if IsAllLevelsCompleted(level, group) {
		return len(Levels), 0
	}
	return IndexOfLevel(level), group
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	default:
		return 0
	}
}
