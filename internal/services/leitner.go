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

// AcceptsSwipe, swipe'ın kelimeye uygulanıp uygulanmayacağını söyler;
// endOfDay, cevabın verildiği kullanım gününün bitişidir (ertesi gün 00:00,
// hariç). Çevrimiçi cevapta bu bugünün bitişidir; çevrimdışı verilmiş bir
// cevapta cevabın gününün bitişidir: kelime o gün vadesi gelmişse cevap
// geçerlidir.
//
//   - Kutu 1: öğrenme ekranındaki kelime, her zaman.
//   - Kutu 2..5: yalnızca vadesi geldiyse (sonraki tekrar tarihi yok ya da
//     endOfDay'den önce). Tekrar listesinin "bugün" koşuluyla aynıdır.
//   - Kutu 6: öğrenilmiş kelime bir daha sorulmaz, hiçbir zaman.
//
// Kabul edilmeyen swipe no-op'tur: yanıtı kaybolup yeniden gönderilen ya da
// iki cihazdan aynı anda gelen cevap kelimeyi ikinci kez ilerletmez.
func AcceptsSwipe(wp *models.WordProgress, endOfDay time.Time) bool {
	switch {
	case wp.Box <= 1:
		return true
	case wp.Box <= 5:
		return wp.NextReviewDate == nil || wp.NextReviewDate.Before(endOfDay)
	default:
		return false
	}
}

// LearnNewWord: yeni kelime (kutu 1 → kutu 2), now anında öğrenildi.
func LearnNewWord(wp *models.WordProgress, now time.Time) {
	wp.Box = 2
	wp.LastReviewDate = &now
	wp.NextReviewDate = nextReview(2, now)
	wp.WrongCount = 0
	wp.Learned = false
}

// OnRightSwipe: doğru cevap (now anında), bir üst kutuya.
func OnRightSwipe(wp *models.WordProgress, now time.Time) {
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

// OnLeftSwipe, yanlış cevabı now anına göre uygular.
//
// Ürün kuralı: tekrardaki kelime (kutu 2 ve üstü) bir alt kutuya iner, en alt
// kutu 2'dir (kutu 1 "henüz öğrenilmedi" demektir, yanlış cevap kelimeyi
// oraya geri atmaz). Kelime, aradaki kutunun aralığını beklemeden ertesi
// kullanım gününün başında (UsageLocation gece yarısı) yeniden sorulur. now
// geçmiş bir günse (çevrimdışı cevap) "ertesi gün" o günün ertesidir.
//
// wrongCount art arda verilen yanlış cevap sayısıdır; kutuyu etkilemez,
// doğru cevap sıfırlar. Kutu 1'deki kelime (öğrenme ekranındaki "Bilmiyorum")
// kutusunda kalır.
func OnLeftSwipe(wp *models.WordProgress, now time.Time) {
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
