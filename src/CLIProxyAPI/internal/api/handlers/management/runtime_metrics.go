package management

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store/authindex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

var runtimeMetricsStartedAt = time.Now()

type procStatSample struct {
	cpuSeconds float64
	rssBytes   uint64
	vmsBytes   uint64
}

type procStatusSample struct {
	threads                     uint64
	voluntaryContextSwitches    uint64
	nonvoluntaryContextSwitches uint64
}

type procIOSample struct {
	readBytes  uint64
	writeBytes uint64
}

func (h *Handler) GetRuntimeMetrics(c *gin.Context) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	proc := readProcStatSample()
	procStatus := readProcStatusSample()
	procIO := readProcIOSample()
	systemMem := readSystemMemory()
	load := readLoadAverage()
	cpuPercent := h.runtimeCPUPercent(proc.cpuSeconds)
	authStats := coreauth.RuntimeStats{}
	if h != nil && h.authManager != nil {
		authStats = h.authManager.RuntimeStats()
	}
	indexStats := h.runtimeAuthIndexStats(c.Request.Context())
	authIndexConfig := h.runtimeAuthIndexConfig()
	authIndexReady := authIndexConfig.Enabled && indexStats.Available && indexStats.Rows > 0 && indexStats.LastFullScanUnix > 0
	authIndexReadyReason := "ready"
	if !authIndexReady {
		authIndexReadyReason = authIndexNotReadyReason(authIndexConfig, indexStats)
	}

	c.JSON(200, gin.H{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"process": gin.H{
			"pid":                           os.Getpid(),
			"cpu_percent":                   cpuPercent,
			"cpu_seconds":                   proc.cpuSeconds,
			"rss_bytes":                     proc.rssBytes,
			"vms_bytes":                     proc.vmsBytes,
			"goroutines":                    runtime.NumGoroutine(),
			"gomaxprocs":                    runtime.GOMAXPROCS(0),
			"go_version":                    runtime.Version(),
			"num_cpu":                       runtime.NumCPU(),
			"memory_percent":                percent(proc.rssBytes, systemMem.totalBytes),
			"heap_alloc":                    mem.HeapAlloc,
			"heap_sys":                      mem.HeapSys,
			"heap_inuse":                    mem.HeapInuse,
			"heap_idle":                     mem.HeapIdle,
			"heap_released":                 mem.HeapReleased,
			"sys_bytes":                     mem.Sys,
			"mallocs":                       mem.Mallocs,
			"frees":                         mem.Frees,
			"heap_objects":                  mem.HeapObjects,
			"stack_inuse":                   mem.StackInuse,
			"gc_next":                       mem.NextGC,
			"gc_count":                      mem.NumGC,
			"gc_cpu_fraction":               mem.GCCPUFraction,
			"gc_pause_total_ns":             mem.PauseTotalNs,
			"last_gc_unix_ms":               mem.LastGC / uint64(time.Millisecond),
			"cgo_calls":                     runtime.NumCgoCall(),
			"uptime_seconds":                uint64(time.Since(runtimeMetricsStartedAt).Seconds()),
			"threads":                       procStatus.threads,
			"fd_count":                      readProcFDCount(),
			"io_read_bytes":                 procIO.readBytes,
			"io_write_bytes":                procIO.writeBytes,
			"voluntary_context_switches":    procStatus.voluntaryContextSwitches,
			"nonvoluntary_context_switches": procStatus.nonvoluntaryContextSwitches,
		},
		"system": gin.H{
			"load1":                  load[0],
			"load5":                  load[1],
			"load15":                 load[2],
			"load1_per_cpu":          load[0] / float64(maxInt(runtime.NumCPU(), 1)),
			"memory_total_bytes":     systemMem.totalBytes,
			"memory_available_bytes": systemMem.availableBytes,
			"memory_used_bytes":      systemMem.usedBytes(),
			"memory_used_percent":    percent(systemMem.usedBytes(), systemMem.totalBytes),
		},
		"auth": gin.H{
			"auth_count":           authStats.AuthCount,
			"sqlite_stub_count":    authStats.SQLiteStubCount,
			"full_auth_count":      authStats.AuthCount - authStats.SQLiteStubCount,
			"hydrated_cache_count": authStats.HydratedCacheCount,
			"hydrated_cache_limit": authStats.HydratedCacheLimit,
			"runtime_auth_count":   authStats.RuntimeAuthCount,
		},
		"auth_index": gin.H{
			"enabled":             authIndexConfig.Enabled,
			"ready":               authIndexReady,
			"ready_reason":        authIndexReadyReason,
			"store_type":          h.runtimeAuthStoreType(),
			"db_path":             indexStats.DBPath,
			"auth_dir":            indexStats.AuthDir,
			"available":           indexStats.Available,
			"journal_mode":        indexStats.JournalMode,
			"page_cache_kb":       authIndexConfig.PageCacheKB,
			"effective_cache_kb":  indexStats.CacheSizeKB,
			"page_size_bytes":     indexStats.PageSizeBytes,
			"lru_size":            authIndexConfig.LRUSize,
			"sync_mode":           authIndexConfig.SyncMode,
			"rebuild_on_start":    authIndexConfig.RebuildOnStart,
			"list_max_default":    authIndexConfig.ListMaxDefault,
			"list_max_hard":       authIndexConfig.ListMaxHard,
			"db_bytes":            indexStats.MainDBBytes,
			"wal_bytes":           indexStats.WALBytes,
			"shm_bytes":           indexStats.SHMBytes,
			"rows":                indexStats.Rows,
			"payload_rows":        indexStats.PayloadRows,
			"last_full_scan_unix": indexStats.LastFullScanUnix,
			"open_connections":    indexStats.OpenConnections,
			"in_use_connections":  indexStats.InUseConnections,
			"idle_connections":    indexStats.IdleConnections,
			"wait_count":          indexStats.WaitCount,
			"wait_duration_ms":    indexStats.WaitDurationMillis,
		},
	})
}

func readProcStatusSample() procStatusSample {
	file, err := os.Open("/proc/self/status")
	if err != nil {
		return procStatusSample{}
	}
	defer file.Close()
	sample := procStatusSample{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, _ := strconv.ParseUint(fields[1], 10, 64)
		switch strings.TrimSuffix(fields[0], ":") {
		case "Threads":
			sample.threads = value
		case "voluntary_ctxt_switches":
			sample.voluntaryContextSwitches = value
		case "nonvoluntary_ctxt_switches":
			sample.nonvoluntaryContextSwitches = value
		}
	}
	return sample
}

func readProcIOSample() procIOSample {
	file, err := os.Open("/proc/self/io")
	if err != nil {
		return procIOSample{}
	}
	defer file.Close()
	sample := procIOSample{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, _ := strconv.ParseUint(fields[1], 10, 64)
		switch strings.TrimSuffix(fields[0], ":") {
		case "read_bytes":
			sample.readBytes = value
		case "write_bytes":
			sample.writeBytes = value
		}
	}
	return sample
}

func readProcFDCount() uint64 {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0
	}
	return uint64(len(entries))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (h *Handler) runtimeAuthIndexConfig() config.AuthIndexCacheConfig {
	cfg := config.DefaultAuthIndexCacheConfig()
	if h != nil && h.cfg != nil {
		cfg = h.cfg.AuthIndexCache
	}
	normalizer := &config.Config{AuthIndexCache: cfg}
	normalizer.NormalizeAuthIndexCacheConfig()
	return normalizer.AuthIndexCache
}

func authIndexNotReadyReason(cfg config.AuthIndexCacheConfig, stats authindex.RuntimeStats) string {
	switch {
	case !cfg.Enabled:
		return "disabled"
	case !stats.Available:
		return "unavailable"
	case stats.Rows <= 0:
		return "empty_index"
	case stats.LastFullScanUnix <= 0:
		return "not_scanned"
	default:
		return "unknown"
	}
}

func (h *Handler) runtimeAuthStoreType() string {
	if h == nil || h.authManager == nil {
		return ""
	}
	store := h.authManager.Store()
	if store == nil {
		return ""
	}
	return fmt.Sprintf("%T", store)
}

func (h *Handler) runtimeAuthIndexStats(parent context.Context) authindex.RuntimeStats {
	if h == nil || h.cfg == nil || !h.cfg.AuthIndexCache.Enabled {
		return authindex.RuntimeStats{}
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	if h.authManager != nil {
		if store, ok := h.authManager.Store().(*authindex.Store); ok && store != nil {
			return store.RuntimeStats(ctx)
		}
	}
	authDir, err := util.ResolveAuthDir(h.cfg.AuthDir)
	if err != nil || strings.TrimSpace(authDir) == "" {
		return authindex.RuntimeStats{}
	}
	cfg := h.runtimeAuthIndexConfig()
	store, err := authindex.Open(ctx, authDir, cfg)
	if err != nil {
		return authindex.RuntimeStats{}
	}
	defer func() { _ = store.Close() }()
	return store.RuntimeStats(ctx)
}

func (h *Handler) runtimeCPUPercent(cpuSeconds float64) float64 {
	if h == nil || cpuSeconds <= 0 {
		return 0
	}
	now := time.Now()
	h.runtimeMetricsMu.Lock()
	defer h.runtimeMetricsMu.Unlock()
	if h.lastProcCPUWall.IsZero() || h.lastProcCPUSeconds <= 0 {
		h.lastProcCPUWall = now
		h.lastProcCPUSeconds = cpuSeconds
		return 0
	}
	elapsed := now.Sub(h.lastProcCPUWall).Seconds()
	deltaCPU := cpuSeconds - h.lastProcCPUSeconds
	h.lastProcCPUWall = now
	h.lastProcCPUSeconds = cpuSeconds
	if elapsed <= 0 || deltaCPU < 0 {
		return 0
	}
	return deltaCPU / elapsed / float64(runtime.NumCPU()) * 100
}

type systemMemorySample struct {
	totalBytes     uint64
	availableBytes uint64
}

func (m systemMemorySample) usedBytes() uint64 {
	if m.totalBytes == 0 || m.availableBytes > m.totalBytes {
		return 0
	}
	return m.totalBytes - m.availableBytes
}

func readLoadAverage() [3]float64 {
	var out [3]float64
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return out
	}
	fields := strings.Fields(string(data))
	for i := 0; i < len(out) && i < len(fields); i++ {
		out[i], _ = strconv.ParseFloat(fields[i], 64)
	}
	return out
}

func readSystemMemory() systemMemorySample {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return systemMemorySample{}
	}
	defer file.Close()
	sample := systemMemorySample{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		valueKB, _ := strconv.ParseUint(fields[1], 10, 64)
		switch strings.TrimSuffix(fields[0], ":") {
		case "MemTotal":
			sample.totalBytes = valueKB * 1024
		case "MemAvailable":
			sample.availableBytes = valueKB * 1024
		}
	}
	return sample
}

func readProcStatSample() procStatSample {
	statData, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return procStatSample{}
	}
	text := string(statData)
	endComm := strings.LastIndex(text, ")")
	if endComm < 0 || endComm+2 >= len(text) {
		return procStatSample{}
	}
	fields := strings.Fields(text[endComm+2:])
	if len(fields) < 22 {
		return procStatSample{}
	}
	utimeTicks, _ := strconv.ParseFloat(fields[11], 64)
	stimeTicks, _ := strconv.ParseFloat(fields[12], 64)
	vmsBytes, _ := strconv.ParseUint(fields[20], 10, 64)
	rssPages, _ := strconv.ParseUint(fields[21], 10, 64)
	return procStatSample{
		cpuSeconds: (utimeTicks + stimeTicks) / 100,
		rssBytes:   rssPages * uint64(os.Getpagesize()),
		vmsBytes:   vmsBytes,
	}
}

func percent(part, total uint64) float64 {
	if total == 0 {
		return 0
	}
	value, _ := strconv.ParseFloat(fmt.Sprintf("%.2f", float64(part)/float64(total)*100), 64)
	return value
}
