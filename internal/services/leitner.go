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

// OnLeftSwipe: yanlış cevap (bkz. onLeftSwipe).
func OnLeftSwipe(wp *models.WordProgress) {
	onLeftSwipe(wp, time.Now())
}

// onLeftSwipe, yanlış cevabı now anına göre uygular.
//
// Ürün kuralı: tekrardaki kelime (kutu 2 ve üstü) bir alt kutuya iner, en alt
// kutu 2'dir (kutu 1 "henüz öğrenilmedi" demektir, yanlış cevap kelimeyi
// oraya geri atmaz). Kelime, aradaki kutunun aralığını beklemeden ertesi
// kullanım gününün başında (UsageLocation gece yarısı) yeniden sorulur.
//
// wrongCount art arda verilen yanlış cevap sayısıdır; kutuyu etkilemez,
// doğru cevap sıfırlar. Kutu 1'deki kelime (öğrenme ekranındaki "Bilmiyorum")
// kutusunda kalır.
func onLeftSwipe(wp *models.WordProgress, now time.Time) {
	wp.WrongCount++
	wp.LastReviewDate = &now
	if wp.Box <= 1 {
		wp.NextReviewDate = nextReview(wp.Box, now)
		return
	}
	if wp.Box > 2 {
		wp.Box--
	}
	wp.Learned = false
	tomorrow := UsageDayStart(now).AddDate(0, 0, 1)
	wp.NextReviewDate = &tomorrow
}
