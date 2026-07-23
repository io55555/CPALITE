package packetcapture

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	cooldownAuditMu      sync.Mutex
	cooldownAuditPath    string
	cooldownAuditEnabled = true
)

// SetCooldownAuditLog configures cooldown audit log next to packet_capture.db.
// path 为空则仅禁用文件输出；enabled=false 关闭。
func SetCooldownAuditLog(path string, enabled bool) {
	cooldownAuditMu.Lock()
	defer cooldownAuditMu.Unlock()
	cooldownAuditEnabled = enabled
	cooldownAuditPath = strings.TrimSpace(path)
}

// AppendCooldownAudit writes one line to cooldown-audit.log (best-effort).
func AppendCooldownAudit(format string, args ...any) {
	cooldownAuditMu.Lock()
	enabled := cooldownAuditEnabled
	path := cooldownAuditPath
	cooldownAuditMu.Unlock()
	if !enabled || path == "" {
		return
	}
	line := time.Now().Format(time.RFC3339Nano) + " " + fmt.Sprintf(format, args...) + "\n"
	cooldownAuditMu.Lock()
	defer cooldownAuditMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
}
