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
