package forwarder

import (
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// TCP相关常量
	_TCPDefaultStreamBufferSize             = 128 * 1024 // TCP默认流缓冲区大小（KB）
	_TCPMaxStreamBufferSize                 = 256 * 1024 // 256KB
	_DefaultGlobalMaxConnections            = 1024       // 默认全局最大连接数
	_TCPUDPDefaultSingleProxyMaxConnections = 256        // TCP/UDP默认单个代理的最大连接数

	// UDP相关常量
	_DefaultUDPBufferSize                          = 4 * 1024 * 1024 // UDP缓冲区大小（4MB）
	_DefaultUDPPackageSize                         = 65507           // UDP最大包大小（字节）
	_DefaultUDPMaxRoutineCount                     = 128             // UDP默认最大协程数
	_DefaultUDPQueueSize                           = 10000           // UDP包队列大小
	_DefaultGlobalUDPReadTargetDataMaxRoutineCount = 1024            // 默认全局UDP读取目标数据的最大协程数

	// 全局限制
	_DefaultMaxPortForwardsLimit = 128 // 默认最大端口转发限制
)

// SafeCheckMode 安全检查模式
const (
	SafeModeNone      = ""      // 不启用安全检查
	SafeModeWhitelist = "white" // 白名单模式
	SafeModeBlacklist = "black" // 黑名单模式
)

// SafeCheck 安全检查函数类型
type SafeCheck func(mode, ip string) (allowed bool)

// GlobalManager 全局管理器
type GlobalManager struct {
	securityManager *_SecurityManager // 安全管理器
	counters        *_GlobalCounters  // 全局计数器
	bufferPool      *_BufferPool      // 缓冲池管理器
	logger          Logger
	manager         *proxyManager
}

// _SecurityManager 安全管理器
type _SecurityManager struct {
	mode      string                // 安全模式
	whitelist map[string]struct{}   // IP白名单
	blacklist map[string]struct{}   // IP黑名单
	rateLimit map[string]*rateLimit // IP限速配置
	checkFunc SafeCheck             // 安全检查函数
	mutex     sync.RWMutex          // 读写锁
}

// rateLimit 速率限制
type rateLimit struct {
	count    atomic.Int64 // 计数器
	lastTime time.Time    // 最后更新时间
}

// _GlobalCounters 全局计数器
type _GlobalCounters struct {
	udpReadTargetDataMaxRoutineCount atomic.Int64 // UDP读取目标数据的最大协程数
	maxPortForwards                  atomic.Int64 // 最大端口转发数
	tcpMaxConnections                atomic.Int64 // TCP最大连接数
	tcpCurrentConnections            atomic.Int64 // TCP当前连接数
	udpMaxRoutines                   atomic.Int64 // UDP最大协程数
	udpCurrentRoutines               atomic.Int64 // UDP当前协程数
}

// _BufferPool 缓冲池管理器
type _BufferPool struct {
	tcpPool *sync.Pool // TCP缓冲池
	udpPool *sync.Pool // UDP缓冲池
	maxSize int64      // 最大缓冲区大小
}

// RelayRuleOptions 定义了转发规则的配置选项
type RelayRuleOptions struct {
	// UDPPackageSize UDP包大小，用于设置缓冲区大小
	UDPPackageSize int `json:"UDPPackageSize,omitempty"`

	// SingleProxyMaxTCPConnections 单个TCP代理允许的最大连接数
	SingleProxyMaxTCPConnections int64 `json:"SingleProxyMaxTCPConnections,omitempty"`

	// SingleProxyMaxUDPReadTargetDataRoutineCount 单个UDP代理允许的最大目标数据读取协程数
	SingleProxyMaxUDPReadTargetDataRoutineCount int64 `json:"SingleProxyMaxUDPReadTargetDataRoutineCount"`

	// UDPProxyPerformanceMode UDP代理性能模式开关，启用时会优化性能
	UDPProxyPerformanceMode bool `json:"UDPProxyPerformanceMode,omitempty"`

	// UDPShortMode UDP短连接模式开关，启用时会优化短连接场景
	UDPShortMode bool `json:"UDPShortMode,omitempty"`

	// SafeMode 安全模式设置，用于控制访问限制
	SafeMode string `json:"SafeMode,omitempty"`

	// InRateLimit 入站流量限制（单位：KB/s）
	InRateLimit float64 `json:"InRateLimit,omitempty"`

	// OutRateLimit 出站流量限制（单位：KB/s）
	OutRateLimit float64 `json:"OutRateLimit,omitempty"`

	// UDPBufferSize UDP缓冲区大小
	UDPBufferSize int `json:"UDPBufferSize,omitempty"`

	// UDPQueueSize UDP队列大小
	UDPQueueSize int `json:"UDPQueueSize,omitempty"`

	Description string `json:"Description,omitempty"`
}

// DefaultRelayRuleOptions 默认的转发规则选项
var DefaultRelayRuleOptions = RelayRuleOptions{
	// UDP相关配置优化
	UDPPackageSize: _DefaultUDPPackageSize, // 使用最大包大小
	SingleProxyMaxUDPReadTargetDataRoutineCount: _DefaultUDPMaxRoutineCount,
	UDPProxyPerformanceMode:                     true,  // 启用性能模式
	UDPShortMode:                                false, // 长连接模式更适合持续通信

	// 缓冲区配置
	UDPBufferSize: _DefaultUDPBufferSize, // UDP缓冲区大小配置
	UDPQueueSize:  _DefaultUDPQueueSize,  // 添加队列大小配置

	// TCP相关配置
	SingleProxyMaxTCPConnections: _TCPUDPDefaultSingleProxyMaxConnections, // 256个连接

	// 安全配置
	SafeMode: SafeModeNone, // 默认为空，表示不启用安全检查

	// 流量限制（0表示不限制）
	InRateLimit:  0, // 入站流量不限制
	OutRateLimit: 0, // 出站流量不限制
}

// 全局单例实例
var globalManager = NewGlobalManager()

// GetGlobalManager 获取全局管理器实例
func GetGlobalManager() *GlobalManager {
	return globalManager
}

// ApplyDefaultOptions 应用默认选项
func ApplyDefaultOptions(opts *RelayRuleOptions) {
	// UDP 相关配置
	if opts.UDPPackageSize == 0 {
		opts.UDPPackageSize = DefaultRelayRuleOptions.UDPPackageSize
	}
	if opts.SingleProxyMaxUDPReadTargetDataRoutineCount == 0 {
		opts.SingleProxyMaxUDPReadTargetDataRoutineCount = DefaultRelayRuleOptions.SingleProxyMaxUDPReadTargetDataRoutineCount
	}
	if opts.UDPBufferSize == 0 {
		opts.UDPBufferSize = DefaultRelayRuleOptions.UDPBufferSize
	}
	if opts.UDPQueueSize == 0 {
		opts.UDPQueueSize = DefaultRelayRuleOptions.UDPQueueSize
	}

	// TCP 相关配置
	if opts.SingleProxyMaxTCPConnections == 0 {
		opts.SingleProxyMaxTCPConnections = DefaultRelayRuleOptions.SingleProxyMaxTCPConnections
	}

	// 性能模式配置
	// 注意：这里不使用 == false 判断，因为我们希望明确设置为 false 的配置保持不变
	if !opts.UDPProxyPerformanceMode && !opts.UDPShortMode {
		opts.UDPProxyPerformanceMode = DefaultRelayRuleOptions.UDPProxyPerformanceMode
		opts.UDPShortMode = DefaultRelayRuleOptions.UDPShortMode
	}

	// 安全配置
	if opts.SafeMode == "" {
		opts.SafeMode = DefaultRelayRuleOptions.SafeMode
	}

	// 流量限制配置
	if opts.InRateLimit == 0 {
		opts.InRateLimit = DefaultRelayRuleOptions.InRateLimit
	}
	if opts.OutRateLimit == 0 {
		opts.OutRateLimit = DefaultRelayRuleOptions.OutRateLimit
	}
}

// ValidateOptions 验证配置选项的合理性
func ValidateOptions(opts *RelayRuleOptions) error {
	// 验证 UDP 包大小
	if opts.UDPPackageSize > _DefaultUDPPackageSize {
		return fmt.Errorf("UDP package size too large: %d > %d", opts.UDPPackageSize, _DefaultUDPPackageSize)
	}

	// 验证缓冲区大小
	if opts.UDPBufferSize > _DefaultUDPBufferSize*2 {
		return fmt.Errorf("UDP buffer size too large: %d > %d", opts.UDPBufferSize, _DefaultUDPBufferSize*2)
	}

	// 验证队列大小
	if opts.UDPQueueSize > _DefaultUDPQueueSize*2 {
		return fmt.Errorf("UDP queue size too large: %d > %d", opts.UDPQueueSize, _DefaultUDPQueueSize*2)
	}

	// 验证协程数限制
	if opts.SingleProxyMaxUDPReadTargetDataRoutineCount > _DefaultUDPMaxRoutineCount*2 {
		return fmt.Errorf("UDP routine count too large: %d > %d", opts.SingleProxyMaxUDPReadTargetDataRoutineCount, _DefaultUDPMaxRoutineCount*2)
	}

	// 验证连接数限制
	if opts.SingleProxyMaxTCPConnections > _TCPUDPDefaultSingleProxyMaxConnections*2 {
		return fmt.Errorf("TCP connection limit too large: %d > %d", opts.SingleProxyMaxTCPConnections, _TCPUDPDefaultSingleProxyMaxConnections*2)
	}

	// 验证安全模式
	switch opts.SafeMode {
	case SafeModeNone, SafeModeWhitelist, SafeModeBlacklist:
		// 有效的安全模式
	default:
		return fmt.Errorf("invalid safe mode: %s", opts.SafeMode)
	}

	// 验证流量限制
	if opts.InRateLimit < 0 || opts.OutRateLimit < 0 {
		return fmt.Errorf("rate limit cannot be negative")
	}

	return nil
}

// NewGlobalManager 创建全局管理器实例
func NewGlobalManager() *GlobalManager {
	return &GlobalManager{
		securityManager: newSecurityManager(),
		counters:        newGlobalCounters(),
		bufferPool:      newBufferPool(),
		logger: &logger{
			logger: slog.Default(),
		},
		manager: &proxyManager{
			forwarders: make(map[string]map[int]Proxy),
			mutex:      sync.RWMutex{},
		},
	}
}

// newSecurityManager 创建安全管理器
func newSecurityManager() *_SecurityManager {
	sm := &_SecurityManager{
		mode:      SafeModeNone,
		whitelist: make(map[string]struct{}),
		blacklist: make(map[string]struct{}),
		rateLimit: make(map[string]*rateLimit),
	}
	sm.checkFunc = sm.defaultSafeCheck
	return sm
}

// newGlobalCounters 创建全局计数器
func newGlobalCounters() *_GlobalCounters {
	gc := &_GlobalCounters{}
	gc.maxPortForwards.Store(_DefaultMaxPortForwardsLimit)
	gc.tcpMaxConnections.Store(_DefaultGlobalMaxConnections)
	gc.udpMaxRoutines.Store(_DefaultUDPMaxRoutineCount)
	gc.udpReadTargetDataMaxRoutineCount.Store(_DefaultGlobalUDPReadTargetDataMaxRoutineCount)
	return gc
}

// newBufferPool 创建缓冲池
func newBufferPool() *_BufferPool {
	return &_BufferPool{
		tcpPool: &sync.Pool{
			New: func() interface{} {
				return make([]byte, _TCPDefaultStreamBufferSize)
			},
		},
		udpPool: &sync.Pool{
			New: func() interface{} {
				return make([]byte, _DefaultUDPPackageSize)
			},
		},
	}
}

// AddToWhitelist 添加IP到白名单
func (sm *_SecurityManager) AddToWhitelist(ip string) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	sm.whitelist[ip] = struct{}{}
}

// AddToBlacklist 添加IP到黑名单
func (sm *_SecurityManager) AddToBlacklist(ip string) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	sm.blacklist[ip] = struct{}{}
}

// RemoveFromWhitelist 从白名单移除IP
func (sm *_SecurityManager) RemoveFromWhitelist(ip string) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	delete(sm.whitelist, ip)
}

// RemoveFromBlacklist 从黑名单移除IP
func (sm *_SecurityManager) RemoveFromBlacklist(ip string) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	delete(sm.blacklist, ip)
}

// SetMode 设置安全模式
func (sm *_SecurityManager) SetMode(mode string) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	sm.mode = mode
}

// GetMode 获取当前安全模式
func (sm *_SecurityManager) GetMode() string {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()
	return sm.mode
}

// CheckSecurity 检查IP是否允许访问
func (sm *_SecurityManager) CheckSecurity(ip string) bool {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()
	return sm.checkFunc(sm.mode, ip)
}

// SetSafeCheck 设置安全检查函数
func (sm *_SecurityManager) SetSafeCheck(f SafeCheck) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	sm.checkFunc = f
}

// defaultSafeCheck 默认的安全检查实现
func (sm *_SecurityManager) defaultSafeCheck(mode, ip string) bool {
	// 不启用安全检查时直接返回 true
	if mode == SafeModeNone {
		return true
	}

	// 本地地址始终允许
	if ip == "127.0.0.1" || ip == "::1" || ip == "localhost" {
		return true
	}

	// 根据模式判断
	switch mode {
	case SafeModeWhitelist:
		_, ok := sm.whitelist[ip]
		return ok
	case SafeModeBlacklist:
		_, ok := sm.blacklist[ip]
		return !ok
	default:
		return false
	}
}

// CheckRateLimit 检查IP是否超过速率限制
func (sm *_SecurityManager) CheckRateLimit(ip string) bool {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()

	limit, exists := sm.rateLimit[ip]
	if !exists {
		return true
	}

	now := time.Now()
	if now.Sub(limit.lastTime) > time.Second {
		limit.count.Store(0)
		limit.lastTime = now
	}

	return limit.count.Load() < 1000 // 每秒1000次限制
}

// AddRateLimit 增加IP的访问计数
func (sm *_SecurityManager) AddRateLimit(ip string) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	if limit, exists := sm.rateLimit[ip]; exists {
		limit.count.Add(1)
	} else {
		sm.rateLimit[ip] = &rateLimit{
			lastTime: time.Now(),
		}
		sm.rateLimit[ip].count.Store(1)
	}
}

// _GlobalCounters 方法
// SetUDPReadTargetDataMaxRoutineCount 设置UDP读取目标数据的最大协程数
func (gc *_GlobalCounters) SetUDPReadTargetDataMaxRoutineCount(max int64) {
	gc.udpReadTargetDataMaxRoutineCount.Store(max)
}

// GetUDPReadTargetDataMaxRoutineCount 获取UDP读取目标数据的最大协程数
func (gc *_GlobalCounters) GetUDPReadTargetDataMaxRoutineCount() int64 {
	return gc.udpReadTargetDataMaxRoutineCount.Load()
}

// SetMaxPortForwardsCount 设置最大端口转发数
func (gc *_GlobalCounters) SetMaxPortForwardsCount(max int64) {
	gc.maxPortForwards.Store(max)
}

// GetMaxPortForwardsCount 获取最大端口转发数
func (gc *_GlobalCounters) GetMaxPortForwardsCount() int64 {
	return gc.maxPortForwards.Load()
}

// SetTCPMaxConnections 设置TCP最大连接数
func (gc *_GlobalCounters) SetTCPMaxConnections(max int64) {
	gc.tcpMaxConnections.Store(max)
}

// GetTCPMaxConnections 获取TCP最大连接数
func (gc *_GlobalCounters) GetTCPMaxConnections() int64 {
	return gc.tcpMaxConnections.Load()
}

// GetTCPCurrentConnections 获取TCP当前连接数
func (gc *_GlobalCounters) GetTCPCurrentConnections() int64 {
	return gc.tcpCurrentConnections.Load()
}

// AddTCPConnections 增加TCP连接数
func (gc *_GlobalCounters) AddTCPConnections(delta int64) int64 {
	return gc.tcpCurrentConnections.Add(delta)
}

// SetUDPMaxRoutines 设置UDP最大协程数
func (gc *_GlobalCounters) SetUDPMaxRoutines(max int64) {
	gc.udpMaxRoutines.Store(max)
}

// GetUDPMaxRoutines 获取UDP最大协程数
func (gc *_GlobalCounters) GetUDPMaxRoutines() int64 {
	return gc.udpMaxRoutines.Load()
}

// GetUDPCurrentRoutines 获取UDP当前协程数
func (gc *_GlobalCounters) GetUDPCurrentRoutines() int64 {
	return gc.udpCurrentRoutines.Load()
}

// AddUDPRoutines 增加UDP协程数
func (gc *_GlobalCounters) AddUDPRoutines(delta int64) int64 {
	return gc.udpCurrentRoutines.Add(delta)
}

// SetMaxSize 设置最大缓冲区大小
func (bp *_BufferPool) SetMaxSize(size int64) {
	bp.maxSize = size
}

// GetMaxSize 获取最大缓冲区大小
func (bp *_BufferPool) GetMaxSize() int64 {
	return bp.maxSize
}

// GetTCPBuffer 获取TCP缓冲区
func (bp *_BufferPool) GetTCPBuffer(trafficRate float64) []byte {
	size := _TCPDefaultStreamBufferSize
	if trafficRate > 10*1024*1024 {
		size = _TCPMaxStreamBufferSize
	}

	buf := bp.tcpPool.Get().([]byte)
	if cap(buf) < size {
		return make([]byte, size)
	}
	return buf[:size]
}

// PutTCPBuffer 归还TCP缓冲区
func (bp *_BufferPool) PutTCPBuffer(buf []byte) {
	size := cap(buf)
	if size == _TCPDefaultStreamBufferSize || size == _TCPMaxStreamBufferSize {
		bp.tcpPool.Put(buf)
	}
}

// GetUDPBuffer 获取UDP缓冲区
func (bp *_BufferPool) GetUDPBuffer(size int) []byte {
	buf := bp.udpPool.Get().([]byte)
	if cap(buf) < size {
		bp.udpPool.Put(buf)
		return make([]byte, size)
	}
	return buf[:size]
}

// PutUDPBuffer 归还UDP缓冲区
func (bp *_BufferPool) PutUDPBuffer(buf []byte) {
	if cap(buf) == _DefaultUDPPackageSize {
		bp.udpPool.Put(buf)
	}
}

// GetSecurityManager 获取安全管理器
func (gm *GlobalManager) GetSecurityManager() *_SecurityManager {
	return gm.securityManager
}

// GetCounters 获取全局计数器
func (gm *GlobalManager) GetCounters() *_GlobalCounters {
	return gm.counters
}

// GetBufferPool 获取缓冲池管理器
func (gm *GlobalManager) GetBufferPool() *_BufferPool {
	return gm.bufferPool
}

// GetTCPListener 获取TCP监听器
func (gm *GlobalManager) GetTCPListener(addr string) (net.Listener, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid address format: %w", err)
	}

	port, err := net.LookupPort("tcp", portStr)
	if err != nil {
		return nil, fmt.Errorf("invalid port: %w", err)
	}

	return net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
}

// GetUDPConn 获取UDP连接
func (gm *GlobalManager) GetUDPConn(addr string) (net.PacketConn, error) {
	return gm.manager.GetUDPConn(addr)
}

// GetManager 获取指定代理
func (gm *GlobalManager) GetManager() *proxyManager {
	return gm.manager
}

// GetProxyKey 生成代理的唯一标识键
func (gm *GlobalManager) GetProxyKey(proxyType, listenIP string, listenPort int) string {
	return fmt.Sprintf("%s@%s:%d", proxyType, listenIP, listenPort)
}
