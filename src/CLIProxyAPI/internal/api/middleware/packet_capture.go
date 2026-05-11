package middleware

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/packetcapture"
)

const maxPacketCaptureClientResponseBytes = 512 * 1024

type packetCaptureWriter struct {
	gin.ResponseWriter
	statusCode int
	body       bytes.Buffer
	truncated  bool
}

func (w *packetCaptureWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *packetCaptureWriter) Write(data []byte) (int, error) {
	w.capture(data)
	return w.ResponseWriter.Write(data)
}

func (w *packetCaptureWriter) WriteString(data string) (int, error) {
	w.capture([]byte(data))
	return w.ResponseWriter.WriteString(data)
}

func (w *packetCaptureWriter) capture(data []byte) {
	if len(data) == 0 || w.body.Len() >= maxPacketCaptureClientResponseBytes {
		if len(data) > 0 {
			w.truncated = true
		}
		return
	}
	remain := maxPacketCaptureClientResponseBytes - w.body.Len()
	if len(data) > remain {
		w.body.Write(data[:remain])
		w.truncated = true
		return
	}
	w.body.Write(data)
}

func (w *packetCaptureWriter) responsePacket() string {
	status := w.statusCode
	if status == 0 {
		status = w.ResponseWriter.Status()
	}
	if status <= 0 {
		status = http.StatusOK
	}
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP/1.1 %d %s\n", status, http.StatusText(status))
	for key, values := range w.ResponseWriter.Header() {
		for _, value := range values {
			b.WriteString(key)
			b.WriteString(": ")
			b.WriteString(value)
			b.WriteByte('\n')
		}
	}
	b.WriteByte('\n')
	if w.body.Len() > 0 {
		b.Write(w.body.Bytes())
		if w.truncated {
			b.WriteString("\n[packet capture truncated]")
		}
	}
	return b.String()
}

// PacketCaptureMiddleware is an independent capture layer. It is enabled only by
// the packet-capture switch and does not depend on request logging, usage, or monitor.
func PacketCaptureMiddleware(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c == nil || c.Request == nil || shouldSkipPacketCapture(c.Request) || !packetcapture.Enabled(c.Request.Context()) {
			c.Next()
			return
		}

		requestInfo, err := captureRequestInfo(c, true)
		if err != nil {
			c.Next()
			return
		}
		rawClientRequest := buildCapturedRawRequest(c.Request, requestInfo)
		if strings.TrimSpace(rawClientRequest) != "" {
			c.Set("USAGE_RAW_REQUEST", rawClientRequest)
		}

		writer := &packetCaptureWriter{ResponseWriter: c.Writer}
		c.Writer = writer
		c.Next()

		clientResponse := writer.responsePacket()
		if strings.TrimSpace(clientResponse) != "" {
			c.Set("USAGE_CLIENT_RESPONSE", clientResponse)
			logCPASentClientResponseFromMiddleware(c, clientResponse, cfg)
		}
		packetcapture.CaptureFromGin(c, rawClientRequest)
		packetcapture.FlushPendingRecords(c)
	}
}

func shouldSkipPacketCapture(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return true
	}
	path := req.URL.Path
	if strings.HasPrefix(path, "/v0/management") ||
		strings.HasPrefix(path, "/management") ||
		strings.HasPrefix(path, "/static/") ||
		path == "/favicon.ico" {
		return true
	}
	return req.Method == http.MethodOptions || req.Method == http.MethodHead
}
