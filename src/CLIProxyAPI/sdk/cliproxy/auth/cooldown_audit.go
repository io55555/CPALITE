package auth

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

// SetCooldownAuditLog configures the cooldown audit log file path and enable flag.
// 默认开启；path 为空时不写文件。
func SetCooldownAuditLog(path string, enabled bool) {
	cooldownAuditMu.Lock()
	defer cooldownAuditMu.Unlock()
	cooldownAuditEnabled = enabled
	cooldownAuditPath = strings.TrimSpace(path)
}

func appendCooldownAudit(format string, args ...any) {
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
