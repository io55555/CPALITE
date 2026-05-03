package capture

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
)

type responseCaptureWriter struct {
	gin.ResponseWriter
	body bytes.Buffer
	limit int
}

func (w *responseCaptureWriter) Write(data []byte) (int, error) {
	if w.limit <= 0 || w.body.Len() < w.limit {
		remaining := w.limit - w.body.Len()
		if w.limit <= 0 || remaining > len(data) {
			remaining = len(data)
		}
		if remaining > 0 {
			_, _ = w.body.Write(data[:remaining])
		}
	}
	return w.ResponseWriter.Write(data)
}

func RequestCaptureMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if strings.HasPrefix(path, "/v0/management") || path == "/management.html" {
			c.Next()
			return
		}
		store := DefaultStore()
		if store == nil || !store.Settings().Enabled {
			c.Next()
			return
		}
		settings := store.Settings()
		requestID := logging.GetGinRequestID(c)
		session := NewSession(c.Request, requestID, settings.MaxBodyBytes)
		if principal, ok := c.Get("apiKey"); ok {
			session.SetAccessInfo(stringValue(c.GetString("accessProvider")), stringValue(principal), stringValue(principal))
		}
		c.Request = c.Request.WithContext(WithSession(c.Request.Context(), session))
		writer := &responseCaptureWriter{ResponseWriter: c.Writer, limit: settings.MaxBodyBytes}
		c.Writer = writer
		c.Next()
		if principal, ok := c.Get("apiKey"); ok {
			session.SetAccessInfo(stringValue(c.GetString("accessProvider")), stringValue(principal), stringValue(principal))
		}
		record := session.Finalize(c.Writer.Status(), cloneHeader(c.Writer.Header()), writer.body.String(), settings.MaxBodyBytes)
		if !record.Success && record.ErrorText == "" {
			record.ErrorText = strings.TrimSpace(c.Errors.ByType(gin.ErrorTypeAny).String())
		}
		_ = store.Insert(c.Request.Context(), record)
	}
}

func cloneHeader(header http.Header) http.Header {
	if len(header) == 0 {
		return nil
	}
	out := make(http.Header, len(header))
	for key, values := range header {
		copied := make([]string, len(values))
		copy(copied, values)
		out[key] = copied
	}
	return out
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return strings.TrimSpace(strings.ReplaceAll(strings.TrimSpace(fmt.Sprint(value)), "\n", " "))
	}
}
