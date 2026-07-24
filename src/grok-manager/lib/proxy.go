package grokmanager

import (
	"bufio"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 代理优先级：认证文件 proxy_url > CPA 配置 proxy-url > 直连
// （不走仅依赖环境变量的隐式代理，避免“看起来像直连/乱连”）

var cpaProxyCache struct {
	mu  sync.Mutex
	url string
	at  time.Time
}

func (c authTokenConfig) effectiveProxyURL() string {
	if p := normalizeProxySetting(c.ProxyURL); p != "" {
		return p
	}
	if c.Metadata != nil {
		if v, ok := c.Metadata["proxy_url"].(string); ok {
			if p := normalizeProxySetting(v); p != "" {
				return p
			}
		}
		if v, ok := c.Metadata["proxy-url"].(string); ok {
			if p := normalizeProxySetting(v); p != "" {
				return p
			}
		}
	}
	return ""
}

func normalizeProxySetting(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	low := strings.ToLower(raw)
	switch low {
	case "direct", "none", "off", "false", "0", "null", "nil", "-":
		return "direct"
	}
	return raw
}

func isDirectProxySetting(raw string) bool {
	return strings.EqualFold(strings.TrimSpace(raw), "direct")
}

func parseProxyURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || isDirectProxySetting(raw) {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, nil
	}
	return u, nil
}

// resolveProxyFunc builds an http.Transport.Proxy function with the required priority.
func resolveProxyFunc(authProxy string) func(*http.Request) (*url.URL, error) {
	authProxy = normalizeProxySetting(authProxy)
	return func(req *http.Request) (*url.URL, error) {
		// 1) 认证文件
		if isDirectProxySetting(authProxy) {
			return nil, nil
		}
		if authProxy != "" {
			if u, err := parseProxyURL(authProxy); err == nil && u != nil {
				return u, nil
			}
		}
		// 2) CPA 全局 proxy-url
		cpa := loadCPAProxyURL()
		if isDirectProxySetting(cpa) {
			return nil, nil
		}
		if cpa != "" {
			if u, err := parseProxyURL(cpa); err == nil && u != nil {
				return u, nil
			}
		}
		// 3) 直连
		return nil, nil
	}
}

func newHTTPClientWithProxy(authProxy string, timeout time.Duration, maxIdle int) *http.Client {
	if maxIdle < 2 {
		maxIdle = 2
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			MaxIdleConns:        maxIdle,
			MaxIdleConnsPerHost: maxIdle,
			Proxy:               resolveProxyFunc(authProxy),
		},
	}
}

func loadCPAProxyURL() string {
	cpaProxyCache.mu.Lock()
	defer cpaProxyCache.mu.Unlock()
	if time.Since(cpaProxyCache.at) < 30*time.Second && cpaProxyCache.at != (time.Time{}) {
		return cpaProxyCache.url
	}
	urlVal := discoverCPAProxyURL()
	cpaProxyCache.url = urlVal
	cpaProxyCache.at = time.Now()
	return urlVal
}

func discoverCPAProxyURL() string {
	for _, path := range cpaConfigCandidates() {
		if v := readProxyURLFromConfigFile(path); v != "" {
			return v
		}
	}
	return ""
}

func cpaConfigCandidates() []string {
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		out = append(out, p)
	}
	if wd, err := os.Getwd(); err == nil {
		add(filepath.Join(wd, "config.yaml"))
		add(filepath.Join(wd, "config.yml"))
		add(filepath.Join(wd, "..", "config.yaml"))
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		add(filepath.Join(dir, "config.yaml"))
		add(filepath.Join(dir, "config.yml"))
		// plugins/windows/amd64/xxx.dll -> 上溯到 CPA 根目录
		add(filepath.Join(dir, "..", "..", "..", "config.yaml"))
		add(filepath.Join(dir, "..", "..", "config.yaml"))
		add(filepath.Join(dir, "..", "config.yaml"))
	}
	// 常见部署/开发路径
	add(`C:\Users\Administrator\Desktop\CPA\20260509\config.yaml`)
	add(`/root/.cli-proxy-api/config.yaml`)
	add(`C:\CLIProxyAPI-local\config.yaml`)
	return out
}

func readProxyURLFromConfigFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// 仅匹配顶层 proxy-url / proxy_url
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		low := strings.ToLower(line)
		var rest string
		switch {
		case strings.HasPrefix(low, "proxy-url:"):
			rest = strings.TrimSpace(line[len("proxy-url:"):])
		case strings.HasPrefix(low, "proxy_url:"):
			rest = strings.TrimSpace(line[len("proxy_url:"):])
		default:
			continue
		}
		rest = strings.TrimSpace(strings.Split(rest, " #")[0])
		rest = strings.Trim(rest, `"'`)
		return normalizeProxySetting(rest)
	}
	return ""
}