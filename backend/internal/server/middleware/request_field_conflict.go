package middleware

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/requestmodel"
	"github.com/gin-gonic/gin"
)

// RejectConflictingRequestFields runs before routing or policy rewrites so
// admission, billing and upstream parsers cannot select different field values.
func RejectConflictingRequestFields() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil || c.Request.Body == nil || c.Request.Body == http.NoBody {
			c.Next()
			return
		}
		switch c.Request.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
		default:
			c.Next()
			return
		}
		body, err := httputil.ReadRequestBodyWithPrealloc(c.Request)
		if err == nil {
			if value, ok := c.Get(requestBodyLimitContextKey); ok {
				if limit, ok := value.(int64); ok && limit > 0 && int64(len(body)) > limit {
					err = &http.MaxBytesError{Limit: limit}
				}
			}
		}
		if err != nil {
			failure := httputil.DescribeRequestBodyReadFailure(c.Request, err)
			logger.FromContext(c.Request.Context()).Warn("gateway request body read failed", failure.LogFields()...)
			writeGatewayInvalidRequest(c, failure.Status, failure.ClientMessage())
			c.Abort()
			return
		}
		if doc := requestmodel.TrimUTF8BOM(body); len(doc) != len(body) && requestmodel.LooksLikeJSON(doc) {
			body = doc
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Request.ContentLength = int64(len(body))
		c.Request.Header.Set("Content-Length", strconv.Itoa(len(body)))
		if field, conflict := requestmodel.ConflictingField(c.GetHeader("Content-Type"), body); conflict {
			MarkIngressRejected(c, IngressRejectConflictingRequestFields)
			writeGatewayInvalidRequest(c, http.StatusBadRequest, fmt.Sprintf("Request body declares field %.64q more than once", field))
			c.Abort()
			return
		}
		c.Next()
	}
}

func writeGatewayInvalidRequest(c *gin.Context, status int, message string) {
	path := c.Request.URL.Path
	switch {
	case strings.HasPrefix(path, "/v1beta") || strings.HasPrefix(path, "/antigravity/v1beta"):
		GoogleErrorWriter(c, status, message)
	case strings.Contains(path, "/messages"):
		c.JSON(status, gin.H{"type": "error", "error": gin.H{"type": "invalid_request_error", "message": message}})
	default:
		c.JSON(status, gin.H{"error": gin.H{"type": "invalid_request_error", "message": message}})
	}
}
