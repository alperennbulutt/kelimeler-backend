package handlers

import (
	"strings"

	"kelimeler-backend/internal/dictlang"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
)

// CourseHeader, isteğin kursunu (öğrenilen dilin kodu, bkz. dictlang) taşıyan
// başlıktır. Başlık yoksa ?course= sorgu parametresine bakılır.
const CourseHeader = "X-Course"

// courseQueryParam, başlık gönderilemeyen istemciler için yedek sorgu
// parametresidir.
const courseQueryParam = "course"

var errInvalidCourse = apiErr(fiber.StatusBadRequest, "invalid_course", "desteklenmeyen kurs")

// requestCourse, isteğin kursunu döner: önce X-Course başlığı, boşsa ?course=.
// İkisi de yoksa İngilizce kursudur (services.DefaultCourse): kurs göndermeyen
// yayındaki eski istemci yalnızca İngilizce öğretir ve ilerlemesi o kurstadır.
// Desteklenmeyen kod 400 invalid_course döner.
//
// İlerleme okuyan ya da yazan her uç kursu buradan okur; her kursun
// ilerlemesi (kutular, harita, tekrar listeleri, istatistik) ayrıdır. Bilinen
// dil (dictionary uçlarındaki know) ilerlemeyi etkilemez.
func requestCourse(c *fiber.Ctx) (string, error) {
	raw := strings.TrimSpace(c.Get(CourseHeader))
	if raw == "" {
		raw = strings.TrimSpace(c.Query(courseQueryParam))
	}
	if raw == "" {
		return services.DefaultCourse, nil
	}
	course := strings.ToLower(raw)
	if !dictlang.Supported(course) {
		return "", errInvalidCourse
	}
	return course, nil
}

var errCourseNotSupported = apiErr(fiber.StatusBadRequest, "course_not_supported",
	"kaldığın grup işaretçisi yalnızca İngilizce kursunda tutulur")

// pointerCourse, "kaldığın grup" işaretçisi uçlarının (PUT
// /users/me/current-group, POST /users/me/advance-group, GET
// /home/current-group) kursudur. İşaretçi users satırındaki tek bir değerdir
// (current_level, current_group) ve kurs öncesinden kalma olarak İngilizce
// kursunundur. Başka bir kursun ilerlemesiyle ilerletilseydi yayındaki eski
// istemcinin (İngilizce) "kaldığın yer"i bozulurdu. Bu yüzden İngilizce dışı
// kurs 400 course_not_supported döner; o kurslarda istemci yeri haritadan
// (/map/levels/:level/groups, /map/open-groups) çıkarır. Geçersiz kurs
// 400 invalid_course'tur.
func pointerCourse(c *fiber.Ctx) (string, error) {
	course, err := requestCourse(c)
	if err != nil {
		return "", err
	}
	if course != services.DefaultCourse {
		return "", errCourseNotSupported
	}
	return course, nil
}
