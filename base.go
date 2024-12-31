package forwarder

import (
	"context"
	"fmt"
	"hash/crc32"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// BaseProxy TCP、UDP协议的基础代理结构体
type BaseProxy struct {
	// TCPUDPProxyCommonConf 包含TCP和UDP代理共用的配置
	TCPUDPProxyCommonConf

	// running 表示代理是否正在运行
	running bool

	// runningMutex 用于保护 running 状态的互斥锁
	runningMutex sync.Mutex

	// 流量统计相关字段
	trafficInLast  int64     // 上次统计的入站流量
	trafficOutLast int64     // 上次统计的出站流量
	lastInUpdate   time.Time // 入站更新时间
	lastOutUpdate  time.Time // 出站更新时间

	// 流量限制器
	inLimiter  *rateLimiter // 入站流量限制器
	outLimiter *rateLimiter // 出站流量限制器

	// RelayRuleOptions 字段
	RelayRuleOptions RelayRuleOptions
}

// ReceiveDataCallback 接收数据回调
//
// @param size int64 接收到的数据大小
func (p *BaseProxy) ReceiveDataCallback(size int64) {
	atomic.AddInt64(&p.TrafficIn, size)
}

// SendDataCallback 发送数据回调
//
// @param size int64 发送的数据大小
func (p *BaseProxy) SendDataCallback(size int64) {
	atomic.AddInt64(&p.TrafficOut, size)
}

// GetType 获取代理类型
func (p *BaseProxy) GetType() string {
	return p.ProxyType
}

// GetStatus 获取代理状态
func (p *BaseProxy) GetStatus() (bool, string) {
	if p.IsRunning() {
		return true, "运行中"
	}
	return false, "已停止"
}

// GetTrafficIn 获取入站流量
func (p *BaseProxy) GetTrafficIn() int64 {
	return atomic.LoadInt64(&p.TrafficIn)
}

// GetTrafficOut 获取出站流量
func (p *BaseProxy) GetTrafficOut() int64 {
	return atomic.LoadInt64(&p.TrafficOut)
}

// IsRunning 检查代理是否正在运行
func (p *BaseProxy) IsRunning() bool {
	p.runningMutex.Lock()
	defer p.runningMutex.Unlock()
	return p.running
}

func (p *BaseProxy) setRunning(running bool) {
	p.runningMutex.Lock()
	defer p.runningMutex.Unlock()
	p.running = running
}

// GetTrafficInRate 计算并返回入站流量速率
//
// @return float64 入站流量速率（字节/秒）
func (p *BaseProxy) GetTrafficInRate() float64 {
	now := time.Now()
	duration := now.Sub(p.lastInUpdate).Seconds()
	if duration == 0 {
		return 0
	}
	rate := float64(p.GetTrafficIn()-p.trafficInLast) / duration
	p.trafficInLast = p.GetTrafficIn()
	p.lastInUpdate = now
	return rate
}

// GetTrafficOutRate 计算并返回出站流量速率
//
// @return float64 出站流量速率（字节/秒）
func (p *BaseProxy) GetTrafficOutRate() float64 {
	now := time.Now()
	duration := now.Sub(p.lastOutUpdate).Seconds()
	if duration == 0 {
		return 0
	}
	rate := float64(p.GetTrafficOut()-p.trafficOutLast) / duration
	p.trafficOutLast = p.GetTrafficOut()
	p.lastOutUpdate = now
	return rate
}

// SetRateLimit 设置入站和出站的速率限制
//
// @param inLimit float64 入站限制（KB/s）
//
// @param outLimit float64 出站限制（KB/s）
func (p *BaseProxy) SetRateLimit(inLimit, outLimit float64) {
	// 设置入站限制
	if inLimit > 0 {
		p.inLimiter = newRateLimiter(inLimit)
	} else {
		p.inLimiter = nil
	}

	// 设置出站限制
	if outLimit > 0 {
		p.outLimiter = newRateLimiter(outLimit)
	} else {
		p.outLimiter = nil
	}
}

// GetRateLimit 获取当前的入站和出站速率限制
//
// @return float64, float64 入站限制和出站限制（KB/s）
func (p *BaseProxy) GetRateLimit() (inLimit, outLimit float64) {
	if p.inLimiter != nil {
		inLimit = p.inLimiter.Limit()
	}
	if p.outLimiter != nil {
		outLimit = p.outLimiter.Limit()
	}
	return inLimit, outLimit
}

// checkRateLimit 检查是否超过速率限制
// @param isIn bool 是否为入站流量
// @param size int 数据大小
// @return error 如果超过限制返回错误
func (p *BaseProxy) checkRateLimit(isIn bool, size int) error {
	if isIn && p.inLimiter != nil {
		if !p.inLimiter.AllowN(time.Now(), float64(size)) {
			GetGlobalManager().GetSecurityManager().AddRateLimit(p.GetListenIP())
			return fmt.Errorf("incoming traffic exceeded rate limit")
		}
	}
	if !isIn && p.outLimiter != nil {
		if !p.outLimiter.AllowN(time.Now(), float64(size)) {
			GetGlobalManager().GetSecurityManager().AddRateLimit(p.GetListenIP())
			return fmt.Errorf("outgoing traffic exceeded rate limit")
		}
	}
	return nil
}

// GetDescription 获取代理的描述信息
func (p *BaseProxy) GetDescription() string {
	return p.RelayRuleOptions.Description
}

// SetDescription 设置代理的描述信息
func (p *BaseProxy) SetDescription(desc string) {
	p.RelayRuleOptions.Description = desc
}

// GetLastUpdateTime 获取最后一次流量统计的更新时间
func (p *BaseProxy) GetLastUpdateTime() time.Time {
	return p.lastOutUpdate
}

// BaseProxyConf 基础代理配置结构体
type BaseProxyConf struct {
	// 流量统计
	TrafficIn  int64 // 入站总流量计数器
	TrafficOut int64 // 出站总流量计数器

	// 代理标识和类型
	key       string // 代理的唯一标识符
	ProxyType string // 代理类型 (tcp/tcp4/tcp6/udp/udp4/udp6)
}

// GetType 获取代理类型
func (p *BaseProxyConf) GetType() string {
	return p.ProxyType
}

// GetStatus 获取代理状态
func (p *BaseProxyConf) GetStatus() string {
	return p.ProxyType
}

// ReceiveDataCallback 处理接收数据的回调
//
// @param nw int64 接收到的数据大小
func (p *BaseProxyConf) ReceiveDataCallback(nw int64) {
	atomic.AddInt64(&p.TrafficIn, nw)
}

// SendDataCallback 处理发送数据的回调
//
// @param nw int64 发送的数据大小
func (p *BaseProxyConf) SendDataCallback(nw int64) {
	atomic.AddInt64(&p.TrafficOut, nw)
}

// GetTrafficIn 获取入站总流量
//
// @return int64 入站总流量大小
func (p *BaseProxyConf) GetTrafficIn() int64 {
	return atomic.LoadInt64(&p.TrafficIn)
}

// GetTrafficOut 获取出站总流量
//
// @return int64 出站总流量大小
func (p *BaseProxyConf) GetTrafficOut() int64 {
	return atomic.LoadInt64(&p.TrafficOut)
}

type TCPUDPProxyCommonConf struct {
	CurrentConnectionsCount   int64
	SingleProxyMaxConnections int64

	BaseProxyConf
	listenAddress      string
	listenIP           string
	listenPort         int
	targetAddressList  []string
	targetAddressCount int
	targetAddressIndex uint64
	targetAddressLock  sync.Mutex
	targetPort         int

	safeMode string
}

// SetMaxConnections 设置最大连接数
func (p *TCPUDPProxyCommonConf) SetMaxConnections(max int64) {
	if max <= 0 {
		p.SingleProxyMaxConnections = _TCPUDPDefaultSingleProxyMaxConnections
	} else {
		p.SingleProxyMaxConnections = max
	}
}

// AddCurrentConnections 增加当前全局连接数
func (p *TCPUDPProxyCommonConf) AddCurrentConnections(delta int64) {
	atomic.AddInt64(&p.CurrentConnectionsCount, delta)

	// 使用 GlobalManager 替代全局函数
	gm := GetGlobalManager()
	if strings.HasPrefix(p.ProxyType, "tcp") {
		gm.GetCounters().AddTCPConnections(delta)
		return
	}

	if strings.HasPrefix(p.ProxyType, "udp") {
		gm.GetCounters().AddUDPRoutines(delta)
		return
	}
}

// GetCurrentConnections 获取当前连接数
func (p *TCPUDPProxyCommonConf) GetCurrentConnections() int64 {
	return atomic.LoadInt64(&p.CurrentConnectionsCount)
}

// GetListenAddress 获取监听地址
func (p *TCPUDPProxyCommonConf) GetListenAddress() string {
	if p.listenAddress == "" {
		if strings.Contains(p.listenIP, ":") {
			p.listenAddress = fmt.Sprintf("[%s]:%d", p.listenIP, p.listenPort)
		} else {
			p.listenAddress = fmt.Sprintf("%s:%d", p.listenIP, p.listenPort)
		}
	}
	return p.listenAddress
}

// GetKey 获取代理键
func (p *TCPUDPProxyCommonConf) GetKey() string {
	if p.key == "" {
		p.key = GetGlobalManager().GetProxyKey(p.ProxyType, p.listenIP, p.listenPort)
	}
	return p.key
}

// GetID 获取代理ID
func (p *TCPUDPProxyCommonConf) GetID() int {
	if p.key == "" {
		p.key = p.GetKey()
	}

	return int(crc32.ChecksumIEEE([]byte(p.key)))
}

// GetListenIP 获取监听IP
func (p *TCPUDPProxyCommonConf) GetListenIP() string {
	return p.listenIP
}

// GetListenPort 获取监听端口
func (p *TCPUDPProxyCommonConf) GetListenPort() int {
	return p.listenPort
}

// GetTargetAddress 获取目标地址
func (p *TCPUDPProxyCommonConf) GetTargetAddress() string {
	p.targetAddressLock.Lock()
	defer p.targetAddressLock.Unlock()

	// 初始化检查
	if p.targetAddressCount <= 0 {
		p.targetAddressCount = len(p.targetAddressList)
		p.targetAddressIndex = 0
	}

	// 尝试最多 targetAddressCount 次
	for i := 0; i < p.targetAddressCount; i++ {
		index := p.targetAddressIndex % uint64(p.targetAddressCount)
		address := p.targetAddressList[index]

		// 格式化地址
		if !strings.Contains(address, ":") {
			address = fmt.Sprintf("%s:%d", address, p.targetPort)
		}

		// 简单的健康检查
		if isHealthy(address) {
			p.targetAddressIndex++
			return address
		}

		p.targetAddressIndex++
	}

	return "" // 或返回错误
}

// String 返回代理的字符串表示
func (p *TCPUDPProxyCommonConf) String() string {
	if p.targetPort == 0 {
		return fmt.Sprintf("%s@%v ===> %v", p.ProxyType, p.GetListenAddress(), p.targetAddressList)
	}
	return fmt.Sprintf("%s@%v ===> %v:%d", p.ProxyType, p.GetListenAddress(), p.targetAddressList, p.targetPort)
}

// SafeCheck 执行安全检查
func (p *TCPUDPProxyCommonConf) SafeCheck(remoteAddr string) bool {
	host, _, _ := net.SplitHostPort(remoteAddr)

	// 使用 GlobalManager 的安全检查
	gm := GetGlobalManager()
	return gm.GetSecurityManager().CheckSecurity(host)
}

// GetBuffer 获取缓冲区
func (p *BaseProxy) GetBuffer(isUDP bool, size int) []byte {
	gm := GetGlobalManager()
	if isUDP {
		return gm.GetBufferPool().GetUDPBuffer(size)
	}
	return gm.GetBufferPool().GetTCPBuffer(p.GetTrafficOutRate())
}

// PutBuffer 归还缓冲区
func (p *BaseProxy) PutBuffer(buf []byte, isUDP bool) {
	gm := GetGlobalManager()
	if isUDP {
		gm.GetBufferPool().PutUDPBuffer(buf)
	} else {
		gm.GetBufferPool().PutTCPBuffer(buf)
	}
}

// GetSafeMode 获取安全模式
func (p *TCPUDPProxyCommonConf) GetSafeMode() string {
	return p.safeMode
}

// healthCheck 健康检查结果缓存
type healthCheck struct {
	healthy   bool      // 健康状态
	lastCheck time.Time // 上次检查时间
}

// healthChecks 存储健康检查结果
var (
	healthChecks  sync.Map           // 地址 -> healthCheck 的映射
	checkTimeout  = time.Second * 3  // 连接超时时间
	checkInterval = time.Second * 30 // 检查间隔
)

// isHealthy 检查目标地址是否健康
func isHealthy(address string) bool {
	// 从缓存获取上次检查结果
	if check, ok := healthChecks.Load(address); ok {
		lastCheck := check.(healthCheck)
		// 如果距离上次检查时间不足检查间隔，直接返回缓存的结果
		if time.Since(lastCheck.lastCheck) < checkInterval {
			return lastCheck.healthy
		}
	}

	// 执行健康检查
	healthy := checkHealth(address)

	// 更新缓存
	healthChecks.Store(address, healthCheck{
		healthy:   healthy,
		lastCheck: time.Now(),
	})

	return healthy
}

// checkHealth 执行实际的健康检查
func checkHealth(address string) bool {
	// 先尝试 TCP 连接
	if checkTCP(address) {
		return true
	}
	// TCP 失败则尝试 UDP
	return checkUDP(address)
}

// checkTCP 检查 TCP 服务健康状态
func checkTCP(address string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return false
	}
	defer conn.Close()
	return true
}

// checkUDP 检查 UDP 服务健康状态
func checkUDP(address string) bool {
	conn, err := net.DialTimeout("udp", address, checkTimeout)
	if err != nil {
		return false
	}
	defer conn.Close()
	return true
}
