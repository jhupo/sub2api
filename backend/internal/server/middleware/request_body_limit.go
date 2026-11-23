package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const requestBodyLimitContextKey = "request_body_limit_bytes"

// RequestBodyLimit 使用 MaxBytesReader 限制请求体大小。
func RequestBodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(requestBodyLimitContextKey, maxBytes)
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}
