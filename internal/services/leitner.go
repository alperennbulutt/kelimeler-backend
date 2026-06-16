package services

import (
	"time"

	"kelimeler-backend/internal/models"
)

// ReviewDaysForBox, her kutunun tekrar aralığı (box_logic.dart birebir portu).
func ReviewDaysForBox(box int) int {
	switch box {
	case 2:
		return 3
	case 3:
		return 7
	case 4:
		return 15
	case 5:
		return 30
	default:
		return 0 // kutu 1 & kutu 6 için tekrar yok
	}
}

func nextReview(box int, from time.Time) *time.Time {
	t := from.AddDate(0, 0, ReviewDaysForBox(box))
	return &t
}

// LearnNewWord: yeni kelime (kutu 1 → kutu 2).
func LearnNewWord(wp *models.WordProgress) {
	now := time.Now()
	wp.Box = 2
	wp.LastReviewDate = &now
	wp.NextReviewDate = nextReview(2, now)
	wp.WrongCount = 0
	wp.Learned = false
}

// OnRightSwipe: doğru cevap, bir üst kutuya.
func OnRightSwipe(wp *models.WordProgress) {
	now := time.Now()
	if wp.Box >= 5 {
		wp.Box = 6
		wp.Learned = true
		wp.WrongCount = 0
		wp.LastReviewDate = &now
		wp.NextReviewDate = nil
		return
	}
	wp.Box++
	wp.WrongCount = 0
	wp.LastReviewDate = &now
	wp.NextReviewDate = nextReview(wp.Box, now)
}

// OnLeftSwipe: yanlış cevap, wrongCount++ ve 3'e ulaşınca bir alt kutuya (kutu 2 tabanı).
func OnLeftSwipe(wp *models.WordProgress) {
	now := time.Now()
	newWrong := wp.WrongCount + 1
	if newWrong >= 3 && wp.Box > 2 {
		wp.Box--
		wp.WrongCount = 0
		wp.LastReviewDate = &now
		wp.NextReviewDate = nextReview(wp.Box, now)
		return
	}
	wp.WrongCount = newWrong
	wp.LastReviewDate = &now
	wp.NextReviewDate = nextReview(wp.Box, now)
}
