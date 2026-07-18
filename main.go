package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ======================== 配置 ========================

type Config struct {
	TargetURL  string `json:"target_url"`
	ListenAddr string `json:"listen_addr"`
	CertFile   string `json:"cert_file"`
	KeyFile    string `json:"key_file"`
	SkipTLSVerify bool `json:"skip_tls_verify"`

	LogFile    string `json:"log_file"`
	RoutesFile string `json:"routes_file"`

	ReloadIntervalSec           int `json:"reload_interval_sec"`
	BufferSizeMB                int `json:"buffer_size_mb"`
	BufferIdleTimeoutSec        int `json:"buffer_idle_timeout_sec"`
	MaxIdleConns                int `json:"max_idle_conns"`
	MaxIdleConnsPerHost         int `json:"max_idle_conns_per_host"`
	MaxConnsPerHost             int `json:"max_conns_per_host"`
	ReadTimeoutSec              int `json:"read_timeout_sec"`
	WriteTimeoutSec             int `json:"write_timeout_sec"`
	IdleTimeoutSec              int `json:"idle_timeout_sec"`
	DialTimeoutSec              int `json:"dial_timeout_sec"`
	DialKeepAliveSec            int `json:"dial_keep_alive_sec"`
	IdleConnTimeoutSec          int `json:"idle_conn_timeout_sec"`
	TLSHandshakeTimeoutSec      int `json:"tls_handshake_timeout_sec"`
	ExpectContinueTimeoutSec    int `json:"expect_continue_timeout_sec"`
	MemoryMonitoringIntervalSec int `json:"memory_monitoring_interval_sec"`
	ShutdownTimeoutSec          int `json:"shutdown_timeout_sec"`
	WsHandshakeTimeoutSec       int `json:"ws_handshake_timeout_sec"`

	FixedHeaders map[string]string `json:"fixed_headers"`
	LogMaxSizeMB  int `json:"log_max_size_mb"`
	LogMaxBackups int `json:"log_max_backups"`
	LogMaxAgeDays int `json:"log_max_age_days"`

	// 安全
	AuthEnabled        bool     `json:"auth_enabled"`
	APIKeys            []string `json:"api_keys"`
	IPWhitelist        []string `json:"ip_whitelist"`
	IPBlacklist        []string `json:"ip_blacklist"`
	RateLimitEnabled   bool     `json:"rate_limit_enabled"`
	RateLimitRPS       float64  `json:"rate_limit_rps"`
	RateLimitBurst     int      `json:"rate_limit_burst"`
	RateLimitCleanupSec int    `json:"rate_limit_cleanup_sec"`
}

func defaultConfig() Config {
	return Config{
		TargetURL:                   "https://www.xxx.com",
		ListenAddr:                  ":8443",
		CertFile:                    "/etc/ca/tls.crt",
		KeyFile:                     "/etc/ca/tls.key",
		SkipTLSVerify:               true,
		LogFile:                     "proxy.log",
		RoutesFile:                  "routes.json",
		ReloadIntervalSec:           10,
		BufferSizeMB:                16,
		BufferIdleTimeoutSec:        60,
		MaxIdleConns:                16,
		MaxIdleConnsPerHost:         16,
		MaxConnsPerHost:             16,
		ReadTimeoutSec:              30,
		WriteTimeoutSec:             600,
		IdleTimeoutSec:              120,
		DialTimeoutSec:              30,
		DialKeepAliveSec:            60,
		IdleConnTimeoutSec:          90,
		TLSHandshakeTimeoutSec:      10,
		ExpectContinueTimeoutSec:    1,
		MemoryMonitoringIntervalSec: 300,
		ShutdownTimeoutSec:          10,
		WsHandshakeTimeoutSec:       10,
		FixedHeaders: map[string]string{
			"Host":       "www.xxx.com",
			"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36",
			"Referer":    "https://www.xxx.com",
		},
		LogMaxSizeMB:  100,
		LogMaxBackups: 3,
		LogMaxAgeDays: 7,
		AuthEnabled:        false,
		IPWhitelist:        nil,
		IPBlacklist:        nil,
		RateLimitEnabled:   false,
		RateLimitRPS:       100,
		RateLimitBurst:     200,
		RateLimitCleanupSec: 60,
	}
}

func loadConfig(path string) (Config, error) {
	cfg := defaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	return cfg, json.Unmarshal(data, &cfg)
}

// ======================== 日志轮转 ========================

type rotatingWriter struct {
	mu      sync.Mutex
	path    string
	file    *os.File
	size    int64
	maxSize int64
	backups int
	maxAge  time.Duration
}

func newRotatingWriter(p string, maxSizeMB, backups, maxAgeDays int) (*rotatingWriter, error) {
	w := &rotatingWriter{
		path:    p,
		maxSize: int64(maxSizeMB) * 1024 * 1024,
		backups: backups,
		maxAge:  time.Duration(maxAgeDays) * 24 * time.Hour,
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	w.file = f
	if info, err := f.Stat(); err == nil {
		w.size = info.Size()
	}
	return w, nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size+int64(len(p)) > w.maxSize {
		w.rotate()
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingWriter) rotate() {
	oldFile := w.file
	timestamp := time.Now().Format("20060102-150405")
	os.Rename(w.path, w.path+"."+timestamp)

	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return // 保留旧引用，防 nil panic
	}
	w.file = f
	w.size = 0
	oldFile.Close()
	w.cleanup()
}

func (w *rotatingWriter) cleanup() {
	dir := filepath.Dir(w.path)
	base := filepath.Base(w.path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type eInfo struct {
		name    string
		modTime time.Time
	}
	var backups []eInfo
	cutoff := time.Now().Add(-w.maxAge)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), base+".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(dir, e.Name()))
			continue
		}
		backups = append(backups, eInfo{name: e.Name(), modTime: info.ModTime()})
	}
	if len(backups) > w.backups {
		for i := 0; i < len(backups)-w.backups; i++ {
			os.Remove(filepath.Join(dir, backups[i].name))
		}
	}
}

func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}

// ======================== 日志记录器 ========================

type proxyLogger struct {
	fileLogger *log.Logger
	stdLogger  *log.Logger
	writer     *rotatingWriter
}

func newProxyLogger(w *rotatingWriter) *proxyLogger {
	return &proxyLogger{
		fileLogger: log.New(w, "", log.LstdFlags),
		stdLogger:  log.New(os.Stdout, "", log.LstdFlags),
		writer:     w,
	}
}

func (l *proxyLogger) Printf(format string, v ...interface{}) {
	l.stdLogger.Printf(format, v...)
	l.fileLogger.Printf(format, v...)
}

func (l *proxyLogger) Close() error {
	return l.writer.Close()
}

// ======================== 缓冲池 ========================

type bufferPool struct {
	pool        sync.Pool
	size        int
	idleTimer   *time.Timer
	idleTimeout time.Duration
	mu          sync.Mutex
	active      int
}

func newBufferPool(size int, idleTimeout time.Duration) *bufferPool {
	bp := &bufferPool{
		size:        size,
		idleTimeout: idleTimeout,
		pool: sync.Pool{
			New: func() interface{} {
				return make([]byte, size)
			},
		},
	}
	bp.idleTimer = time.AfterFunc(idleTimeout, bp.cleanup)
	bp.idleTimer.Stop()
	return bp
}

func (b *bufferPool) Get() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.idleTimer != nil {
		b.idleTimer.Stop()
	}
	b.active++
	return b.pool.Get().([]byte)
}

func (b *bufferPool) Put(buf []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cap(buf) == b.size {
		buf = buf[:b.size]
		b.pool.Put(buf)
	}
	if b.active > 0 {
		b.active--
	}
	if b.active <= 0 && b.idleTimer != nil {
		b.idleTimer.Reset(b.idleTimeout)
	}
}

func (b *bufferPool) cleanup() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active <= 0 {
		for i := 0; i < 10; i++ {
			if b.pool.Get() == nil {
				break
			}
		}
		if logger != nil {
			logger.Printf("缓冲池已清理(空闲超时)")
		}
	}
}

// ======================== 路由 & 负载均衡 ========================

type contextKey string

const proxyCtxKey contextKey = "proxyContext"

// proxyContext 通过 context 在 handler/Director/WebSocket 之间传递请求信息
type proxyContext struct {
	newPath      string
	originalPath string
	backendURL   *url.URL
}

// backendTarget 单个后端
type backendTarget struct {
	URL    *url.URL
	Weight int
}

// routeEntry 路由条目：前缀 + 后端列表
type routeEntry struct {
	prefix   string
	backends []backendTarget
	balancer *loadBalancer
}

// loadBalancer 加权轮询负载均衡器
type loadBalancer struct {
	targets []backendTarget
	total   int
	counter uint64
}

func newLoadBalancer(targets []backendTarget) *loadBalancer {
	total := 0
	for _, t := range targets {
		total += t.Weight
	}
	return &loadBalancer{targets: targets, total: total}
}

// next 返回下一个后端（加权轮询）
func (lb *loadBalancer) next() *url.URL {
	if len(lb.targets) == 1 {
		return lb.targets[0].URL
	}
	idx := int(atomic.AddUint64(&lb.counter, 1)) % lb.total
	cumulative := 0
	for _, t := range lb.targets {
		cumulative += t.Weight
		if idx < cumulative {
			return t.URL
		}
	}
	return lb.targets[0].URL
}

var (
	routeMutex  sync.RWMutex
	routeTable  map[string]*routeEntry
	lastMod     time.Time
	networkDataBytes int64
)

// parseRoutes 解析路由配置，支持两种格式:
//   - 字符串: "api": "backend/api"  (使用全局 target)
//   - 对象:   "stream": {"prefix": "live", "backends": [{"url": "...", "weight": 1}]}
func parseRoutes(data []byte) (map[string]*routeEntry, error) {
	var wrapper struct {
		Routes map[string]json.RawMessage `json:"routes"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, err
	}
	entries := make(map[string]*routeEntry)
	for key, rawVal := range wrapper.Routes {
		// 尝试字符串格式
		var prefix string
		if err := json.Unmarshal(rawVal, &prefix); err == nil {
			entries[key] = &routeEntry{prefix: prefix}
			continue
		}
		// 对象格式
		var obj struct {
			Prefix   string `json:"prefix"`
			Backends []struct {
				URL    string `json:"url"`
				Weight int    `json:"weight"`
			} `json:"backends"`
		}
		if err := json.Unmarshal(rawVal, &obj); err != nil {
			return nil, fmt.Errorf("路由 %q 格式错误: %v", key, err)
		}
		var targets []backendTarget
		for _, b := range obj.Backends {
			u, err := url.Parse(b.URL)
			if err != nil {
				return nil, fmt.Errorf("路由 %q 后端 URL 错误 %q: %v", key, b.URL, err)
			}
			w := b.Weight
			if w <= 0 {
				w = 1
			}
			targets = append(targets, backendTarget{URL: u, Weight: w})
		}
		entry := &routeEntry{prefix: obj.Prefix, backends: targets}
		if len(targets) > 0 {
			entry.balancer = newLoadBalancer(targets)
		}
		entries[key] = entry
	}
	return entries, nil
}

func loadRoutes(routesFile string, l *proxyLogger) error {
	fileInfo, err := os.Stat(routesFile)
	if err != nil {
		return err
	}
	if !fileInfo.ModTime().After(lastMod) {
		return nil
	}
	data, err := os.ReadFile(routesFile)
	if err != nil {
		return err
	}
	newRoutes, err := parseRoutes(data)
	if err != nil {
		return err
	}
	routeMutex.Lock()
	defer routeMutex.Unlock()
	routeTable = newRoutes
	lastMod = fileInfo.ModTime()
	l.Printf("路由配置已重新加载，共 %d 条路由", len(routeTable))
	return nil
}

func findRoute(reqPath string) (*routeEntry, string, bool) {
	parts := strings.SplitN(strings.TrimPrefix(reqPath, "/"), "/", 2)
	if len(parts) < 1 {
		return nil, "", false
	}
	routeMutex.RLock()
	defer routeMutex.RUnlock()
	entry, exists := routeTable[parts[0]]
	if !exists {
		return nil, "", false
	}
	remaining := ""
	if len(parts) > 1 {
		remaining = parts[1]
	}
	return entry, remaining, true
}

func startRouteReloader(ctx context.Context, routesFile string, interval time.Duration, l *proxyLogger) {
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := loadRoutes(routesFile, l); err != nil {
					l.Printf("路由配置重载失败: %v", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

// ======================== WebSocket 代理 ========================

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Connection"), "upgrade") &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func proxyWebSocket(w http.ResponseWriter, r *http.Request, target *url.URL, targetPath string) {
	timeout := time.Duration(cfg.WsHandshakeTimeoutSec) * time.Second

	// 连接后端
	d := &net.Dialer{Timeout: timeout}
	backendConn, err := d.DialContext(r.Context(), "tcp", target.Host)
	if err != nil {
		logger.Printf("[WS] 后端连接失败 %s: %v", target.Host, err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}

	// TLS 加密（wss/https）
	if target.Scheme == "wss" || target.Scheme == "https" {
		tlsConn := tls.Client(backendConn, &tls.Config{
			InsecureSkipVerify: cfg.SkipTLSVerify,
			ServerName:         target.Hostname(),
		})
		if err := tlsConn.HandshakeContext(r.Context()); err != nil {
			backendConn.Close()
			logger.Printf("[WS] TLS 握手失败: %v", err)
			http.Error(w, "TLS Handshake Failed", http.StatusBadGateway)
			return
		}
		backendConn = tlsConn
	}
	defer backendConn.Close()

	// 构建升级请求发往后端
	upgradeReq, _ := http.NewRequest("GET", target.ResolveReference(&url.URL{Path: targetPath}).String(), nil)
	upgradeReq.Header = r.Header.Clone()
	upgradeReq.Header.Set("Host", target.Host)

	if err := upgradeReq.Write(backendConn); err != nil {
		logger.Printf("[WS] 发送升级请求失败: %v", err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}

	// 读取后端响应
	br := bufio.NewReader(backendConn)
	resp, err := http.ReadResponse(br, upgradeReq)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		logger.Printf("[WS] 后端未升级: status=%v err=%v", resp.StatusCode, err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}

	// 劫持客户端连接
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}
	clientConn, clientBuf, err := hj.Hijack()
	if err != nil {
		logger.Printf("[WS] 劫持连接失败: %v", err)
		return
	}
	defer clientConn.Close()

	// 转发 101 响应给客户端
	var respBuf bytes.Buffer
	fmt.Fprintf(&respBuf, "HTTP/1.1 101 Switching Protocols\r\n")
	resp.Header.Write(&respBuf)
	respBuf.WriteString("\r\n")
	clientConn.Write(respBuf.Bytes())

	logger.Printf("[WS] 连接建立: %s => %s", r.URL.Path, target.ResolveReference(&url.URL{Path: targetPath}))

	// 双向数据转发（clientBuf 自动先消费缓冲区再读 raw conn）
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(backendConn, clientBuf)
		done <- struct{}{}
	}()
	go func() {
		if br.Buffered() > 0 {
			io.Copy(clientConn, br)
		}
		io.Copy(clientConn, backendConn)
		done <- struct{}{}
	}()
	<-done

	logger.Printf("[WS] 连接关闭: %s", r.URL.Path)
}

// ======================== 安全 ========================

// extractIP 从 RemoteAddr 提取纯 IP（去掉端口）
func extractIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// ipFilter CIDR 基础的 IP 黑白名单过滤器
type ipFilter struct {
	whitelist []*net.IPNet
	blacklist []*net.IPNet
}

func newIPFilter(whitelist, blacklist []string) (*ipFilter, error) {
	f := &ipFilter{}
	for _, cidr := range whitelist {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("白名单 CIDR %q 解析失败: %v", cidr, err)
		}
		f.whitelist = append(f.whitelist, ipNet)
	}
	for _, cidr := range blacklist {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("黑名单 CIDR %q 解析失败: %v", cidr, err)
		}
		f.blacklist = append(f.blacklist, ipNet)
	}
	return f, nil
}

// Allow 判断 IP 是否允许访问。黑名单优先；白名单为空则全部放行
func (f *ipFilter) Allow(ip string) bool {
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return false
	}
	for _, ipNet := range f.blacklist {
		if ipNet.Contains(parsedIP) {
			return false
		}
	}
	if len(f.whitelist) == 0 {
		return true
	}
	for _, ipNet := range f.whitelist {
		if ipNet.Contains(parsedIP) {
			return true
		}
	}
	return false
}

// tokenBucket 单个令牌桶
type tokenBucket struct {
	tokens     float64
	maxTokens  float64
	refillRate float64
	lastRefill time.Time
}

// Allow 尝试消费一个令牌
func (tb *tokenBucket) Allow() bool {
	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tb.tokens += elapsed * tb.refillRate
	if tb.tokens > tb.maxTokens {
		tb.tokens = tb.maxTokens
	}
	tb.lastRefill = now
	if tb.tokens >= 1 {
		tb.tokens--
		return true
	}
	return false
}

// rateLimiter 基于 IP 的令牌桶限流器
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rps     float64
	burst   int
}

func newRateLimiter(rps float64, burst, cleanupSec int) *rateLimiter {
	rl := &rateLimiter{
		buckets: make(map[string]*tokenBucket),
		rps:     rps,
		burst:   burst,
	}
	if cleanupSec > 0 {
		go rl.cleanupLoop(time.Duration(cleanupSec) * time.Second)
	}
	return rl
}

func (rl *rateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	bucket, exists := rl.buckets[key]
	if !exists {
		bucket = &tokenBucket{
			tokens:     float64(rl.burst),
			maxTokens:  float64(rl.burst),
			refillRate: rl.rps,
			lastRefill: time.Now(),
		}
		rl.buckets[key] = bucket
	}
	return bucket.Allow()
}

func (rl *rateLimiter) cleanupLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		cutoff := time.Now().Add(-5 * time.Minute)
		for key, bucket := range rl.buckets {
			if bucket.lastRefill.Before(cutoff) {
				delete(rl.buckets, key)
			}
		}
		rl.mu.Unlock()
	}
}

// validateAPIKey 检查请求中的 API Key
func validateAPIKey(r *http.Request, validKeys map[string]bool) bool {
	// X-API-Key 头
	if key := r.Header.Get("X-API-Key"); key != "" {
		return validKeys[key]
	}
	// Authorization: Bearer <key>
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return validKeys[strings.TrimPrefix(auth, "Bearer ")]
	}
	return false
}

// securityHeaders 注入安全响应头
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

// securityFilter 组合安全中间件：IP 过滤 → 限流 → 认证
func securityFilter(l *proxyLogger, ipF *ipFilter, rl *rateLimiter, apiKeys map[string]bool, authEnabled bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 健康检查跳过安全检查
		if r.URL.Path == "/_health" {
			next.ServeHTTP(w, r)
			return
		}

		ip := extractIP(r.RemoteAddr)

		// IP 过滤
		if ipF != nil {
			if !ipF.Allow(ip) {
				l.Printf("[SECURITY] IP拒绝: %s %s %s", r.Method, r.URL.Path, ip)
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
		}

		// 限流
		if rl != nil {
			if !rl.Allow(ip) {
				l.Printf("[SECURITY] 限流: %s %s %s", r.Method, r.URL.Path, ip)
				w.Header().Set("Retry-After", "1")
				http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
				return
			}
		}

		// API Key 认证
		if authEnabled && len(apiKeys) > 0 {
			if !validateAPIKey(r, apiKeys) {
				l.Printf("[SECURITY] 认证失败: %s %s %s", r.Method, r.URL.Path, r.RemoteAddr)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// ======================== 中间件 ========================

func panicRecovery(l *proxyLogger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				stack := make([]byte, 64*1024)
				n := runtime.Stack(stack, false)
				l.Printf("[PANIC] %s %s: %v\n%s", r.Method, r.URL.Path, err, stack[:n])
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ======================== main ========================

var logger *proxyLogger
var cfg Config

func main() {
	// 加载配置
	configPath := "config.json"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}
	var err error
	cfg, err = loadConfig(configPath)
	if err != nil {
		log.Printf("加载配置文件失败 (%s)，使用默认配置: %v", configPath, err)
		cfg = defaultConfig()
	}

	// 日志
	lw, err := newRotatingWriter(cfg.LogFile, cfg.LogMaxSizeMB, cfg.LogMaxBackups, cfg.LogMaxAgeDays)
	if err != nil {
		log.Fatalf("无法创建日志文件: %v", err)
	}
	logger = newProxyLogger(lw)
	defer logger.Close()
	logger.Printf("配置加载完成: %s", configPath)

	// 路由
	if err := loadRoutes(cfg.RoutesFile, logger); err != nil {
		logger.Printf("初始路由配置加载失败: %v", err)
		log.Fatalf("初始路由配置加载失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startRouteReloader(ctx, cfg.RoutesFile, time.Duration(cfg.ReloadIntervalSec)*time.Second, logger)

	// 全局目标（用于单后端路由）
	globalTarget, err := url.Parse(cfg.TargetURL)
	if err != nil {
		logger.Printf("URL解析失败: %v", err)
		log.Fatalf("URL解析失败: %v", err)
	}

	// 反向代理
	proxy := httputil.NewSingleHostReverseProxy(globalTarget)
	bufPool := newBufferPool(cfg.BufferSizeMB*1024*1024, time.Duration(cfg.BufferIdleTimeoutSec)*time.Second)

	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   time.Duration(cfg.DialTimeoutSec) * time.Second,
			KeepAlive: time.Duration(cfg.DialKeepAliveSec) * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          cfg.MaxIdleConns,
		MaxIdleConnsPerHost:   cfg.MaxIdleConnsPerHost,
		MaxConnsPerHost:       cfg.MaxConnsPerHost,
		IdleConnTimeout:       time.Duration(cfg.IdleConnTimeoutSec) * time.Second,
		TLSHandshakeTimeout:   time.Duration(cfg.TLSHandshakeTimeoutSec) * time.Second,
		ExpectContinueTimeout: time.Duration(cfg.ExpectContinueTimeoutSec) * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: cfg.SkipTLSVerify,
			MinVersion:         tls.VersionTLS12,
		},
		DisableCompression: true,
	}

	proxy.Director = func(req *http.Request) {
		pc, ok := req.Context().Value(proxyCtxKey).(proxyContext)
		if !ok {
			return
		}
		req.URL.Scheme = pc.backendURL.Scheme
		req.URL.Host = pc.backendURL.Host
		req.Host = pc.backendURL.Host
		req.URL.Path = pc.newPath
		for k, v := range cfg.FixedHeaders {
			req.Header.Set(k, v)
		}
		req.Header.Del("Accept-Encoding")
		req.Header.Del("If-Modified-Since")
	}
	proxy.Transport = transport
	proxy.BufferPool = bufPool

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logger.Printf("[502] %s %s -> %v", r.Method, r.URL.Path, err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		// CORS
		resp.Header.Set("Access-Control-Allow-Origin", "*")

		// Range 请求优化: 视频/音频内容添加缓存和 Range 支持头
		resp.Header.Set("Accept-Ranges", "bytes")
		ct := resp.Header.Get("Content-Type")
		if strings.HasPrefix(ct, "video/") || strings.HasPrefix(ct, "audio/") {
			if resp.StatusCode == http.StatusPartialContent {
				resp.Header.Set("Cache-Control", "public, max-age=3600")
			}
		}

		// 流量统计
		if resp.ContentLength > 0 {
			atomic.AddInt64(&networkDataBytes, resp.ContentLength)
		}
		return nil
	}

	// 请求处理
	proxyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// 健康检查
		if r.URL.Path == "/_health" {
			if r.Method != http.MethodGet {
				http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}

		// 路由匹配
		entry, remaining, exists := findRoute(r.URL.Path)
		if !exists {
			logger.Printf("%s %s [404] [%s] %v", r.Method, r.URL.Path, r.RemoteAddr, time.Since(start))
			http.NotFound(w, r)
			return
		}

		// 选择后端
		var backendURL *url.URL
		if entry.balancer != nil {
			backendURL = entry.balancer.next() // 加权轮询
		} else {
			backendURL = globalTarget // 单后端，使用全局目标
		}

		originalPath := r.URL.Path
		newPath := path.Join("/", entry.prefix, remaining)

		// WebSocket 代理
		if isWebSocketUpgrade(r) {
			proxyWebSocket(w, r, backendURL, newPath)
			return
		}

		// HTTP 代理
		pc := proxyContext{
			newPath:      newPath,
			originalPath: originalPath,
			backendURL:   backendURL,
		}
		ctx := context.WithValue(r.Context(), proxyCtxKey, pc)
		r = r.WithContext(ctx)
		proxy.ServeHTTP(w, r)

		logger.Printf("%s %s => %s [backend=%s] [%s] %v",
			r.Method, originalPath, newPath, backendURL.Host, r.RemoteAddr, time.Since(start))
	})

	var handler http.Handler = proxyHandler

	// 安全中间件初始化
	var ipF *ipFilter
	if len(cfg.IPWhitelist) > 0 || len(cfg.IPBlacklist) > 0 {
		var err error
		ipF, err = newIPFilter(cfg.IPWhitelist, cfg.IPBlacklist)
		if err != nil {
			logger.Printf("IP过滤器初始化失败: %v", err)
		} else {
			logger.Printf("IP过滤: 白名单=%d 黑名单=%d", len(ipF.whitelist), len(ipF.blacklist))
		}
	}

	var rl *rateLimiter
	if cfg.RateLimitEnabled {
		rl = newRateLimiter(cfg.RateLimitRPS, cfg.RateLimitBurst, cfg.RateLimitCleanupSec)
		logger.Printf("限流: %v RPS, 突发=%d", cfg.RateLimitRPS, cfg.RateLimitBurst)
	}

	apiKeysMap := make(map[string]bool)
	for _, k := range cfg.APIKeys {
		apiKeysMap[k] = true
	}
	if cfg.AuthEnabled {
		logger.Printf("认证: 已启用, %d 个 API Key", len(apiKeysMap))
	}

	handler = securityFilter(logger, ipF, rl, apiKeysMap, cfg.AuthEnabled, handler)
	handler = securityHeaders(handler)
	handler = panicRecovery(logger, handler)

	// 内存监控
	go func() {
		ticker := time.NewTicker(time.Duration(cfg.MemoryMonitoringIntervalSec) * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			logger.Printf("内存: Alloc=%.2fMB Sys=%.2fMB GC=%d Goroutines=%d 流量=%.2fMB",
				float64(m.Alloc)/1024/1024,
				float64(m.Sys)/1024/1024,
				m.NumGC,
				runtime.NumGoroutine(),
				float64(atomic.LoadInt64(&networkDataBytes))/1024/1024,
			)
		}
	}()

	// HTTP 服务器
	server := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: handler,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
		ReadTimeout:  time.Duration(cfg.ReadTimeoutSec) * time.Second,
		WriteTimeout: time.Duration(cfg.WriteTimeoutSec) * time.Second,
		IdleTimeout:  time.Duration(cfg.IdleTimeoutSec) * time.Second,
	}

	// 优雅关闭
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-quit
		logger.Printf("收到信号 %v，正在关闭...", sig)
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Duration(cfg.ShutdownTimeoutSec)*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Printf("关闭错误: %v", err)
		}
	}()

	logger.Printf("启动代理服务器")
	logger.Printf("监听: %s | 目标: %s | 缓冲: %dMB", cfg.ListenAddr, cfg.TargetURL, cfg.BufferSizeMB)
	logger.Printf("路由文件: %s | 重载间隔: %ds", cfg.RoutesFile, cfg.ReloadIntervalSec)
	logger.Printf("功能: WebSocket代理 | 加权负载均衡 | Range缓存优化 | 安全层")
	logger.Printf("健康检查: https://%s/_health", cfg.ListenAddr)

	if err := server.ListenAndServeTLS(cfg.CertFile, cfg.KeyFile); err != nil && err != http.ErrServerClosed {
		logger.Printf("服务器启动失败: %v", err)
		log.Fatalf("服务器启动失败: %v", err)
	}
	logger.Printf("服务器已停止")
}
