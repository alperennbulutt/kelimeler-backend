package middleware

import "github.com/gofiber/fiber/v2"

// APIError, istemciye dönecek yapılandırılmış hata. Code, Flutter tarafının
// switch'lediği makine-okunur koddur (ör. "email_in_use", "invalid_credentials").
type APIError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"error"`
}

func (e *APIError) Error() string { return e.Message }

func NewAPIError(status int, code, message string) *APIError {
	return &APIError{Status: status, Code: code, Message: message}
}

// ErrorHandler, Fiber'ın merkezi hata işleyicisidir; her hatayı {code, error} JSON'a çevirir.
func ErrorHandler(c *fiber.Ctx, err error) error {
	if apiErr, ok := err.(*APIError); ok {
		return c.Status(apiErr.Status).JSON(fiber.Map{
			"code":  apiErr.Code,
			"error": apiErr.Message,
		})
	}
	if fe, ok := err.(*fiber.Error); ok {
		return c.Status(fe.Code).JSON(fiber.Map{
			"code":  "error",
			"error": fe.Message,
		})
	}
	return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
		"code":  "internal",
		"error": "beklenmeyen bir hata oluştu",
	})
}

// StatusFor, ErrorHandler'ın verilen hata için yazacağı HTTP durum kodudur.
func StatusFor(err error) int {
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.Status
	}
	if fe, ok := err.(*fiber.Error); ok {
		return fe.Code
	}
	return fiber.StatusInternalServerError
}

// WriteErrorStatus, zincirden dönen hatanın (ya da panic'in) durum kodunu yanıta
// erkenden yazar; gövdeyi yine ErrorHandler üretir.
//
// Hata, zincirin en dışındaki katmanda yanıta çevrilir. Daha içte çalışan bir
// middleware (Sentry) durum kodunu o andan önce okur ve hatalı isteği 200
// olarak görür; bu middleware onun hemen içine konur.
func WriteErrorStatus(c *fiber.Ctx) error {
	defer func() {
		if r := recover(); r != nil {
			c.Status(fiber.StatusInternalServerError)
			panic(r)
		}
	}()
	err := c.Next()
	if err != nil {
		c.Status(StatusFor(err))
	}
	return err
}
