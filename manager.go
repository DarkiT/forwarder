package forwarder

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Proxy 接口定义了代理的基本操作
type Proxy interface {
	// 基本控制方法
	Start() error    // 启动代理
	Stop() error     // 停止代理
	Restart() error  // 重启代理
	IsRunning() bool // 检查代理是否正在运行

	// 流量统计相关方法
	ReceiveDataCallback(int64)  // 接收数据时的回调函数
	SendDataCallback(int64)     // 发送数据时的回调函数
	GetTrafficIn() int64        // 获取入站总流量
	GetTrafficOut() int64       // 获取出站总流量
	GetTrafficInRate() float64  // 获取入站流量速率
	GetTrafficOutRate() float64 // 获取出站流量速率

	// 状态查询方法
	GetTargetAddress() string     // 获取目标地址
	GetType() string              // 获取代理类型
	GetStatus() (ok bool)         // 获取代理状态
	GetCurrentConnections() int64 // 获取当前连接数

	// 配置相关方法
	GetListenIP() string          // 获取监听IP
	GetListenPort() int           // 获取监听端口
	GetKey() string               // 获取代理唯一标识
	GetID() int                   // 获取代理ID
	GetDescription() string       // 获取代理描述
	GetLastUpdateTime() time.Time // 获取代理最后更新时间

	// 安全相关方法
	SafeCheck(ip string) bool               // IP安全检查
	SetRateLimit(inLimit, outLimit float64) // 设置流量限制
	GetRateLimit() (float64, float64)       // 获取流量限制

	String() string // 获取代理的字符串表示
}

// Manager 代理管理器接口
type Manager interface {
	// 代理管理方法
	AddProxy(proxy Proxy)
	RemoveProxy(id int, proxyType string)
	GetProxy(id int, proxyType string) (Proxy, bool)
	GetAllProxiesList() map[int]ProxyInfo

	// TCP代理相关方法
	GetTCPProxy(id int) (*TCPProxy, bool)
	GetAllTCPProxies() map[int]*TCPProxy
	RemoveTCPProxy(id int)

	// UDP代理相关方法
	GetUDPProxy(id int) (*UDPProxy, bool)
	GetAllUDPProxies() map[int]*UDPProxy
	RemoveUDPProxy(id int)

	// 网络连接方法
	GetTCPListener(addr string) (net.Listener, error)
	GetUDPConn(addr string) (net.PacketConn, error)

	// CreateProxy 创建新的代理实例
	CreateProxy(opts CreateProxyOptions) (Proxy, error)
}

// ProxyInfo 代理信息结构体
type ProxyInfo struct {
	ID          int        `json:"proxyId" toml:"proxyId"`
	Type        string     `json:"proxyType" toml:"proxyType"`
	ListenAddr  string     `json:"listenAddr" toml:"listenAddr"`
	RemoteAddr  string     `json:"remoteAddr" toml:"remoteAddr"`
	Status      bool       `json:"proxyStatus" toml:"proxyStatus"`
	RateIn      float64    `json:"rateIn" toml:"rateIn"`
	RateOut     float64    `json:"rateOut" toml:"rateOut"`
	TrafficIn   int64      `json:"trafficIn" toml:"trafficIn"`
	TrafficOut  int64      `json:"trafficOut" toml:"trafficOut"`
	RateLimit   _RateLimit `json:"rateLimit" toml:"rateLimit"`
	UpdateTime  time.Time  `json:"updateTime" toml:"updateTime"`
	Description string     `json:"description" toml:"description"`
	Connections int64      `json:"connections" toml:"connections"`
}

// _RateLimit 流量限制结构体
type _RateLimit struct {
	In  float64 `json:"limitIn" toml:"limitIn"`
	Out float64 `json:"limitOut" toml:"limitOut"`
}

// proxyManager 代理管理器实现
type proxyManager struct {
	forwarders map[string]map[int]Proxy
	mutex      sync.RWMutex
}

// _MrgConfig 代理管理器配置
type _MrgConfig struct {
	Protocols []string // 支持的协议类型
}

// ManagerOptions 定义管理器配置选项
type ManagerOptions func(*_MrgConfig)

// CreateProxyOptions 创建代理的选项
type CreateProxyOptions struct {
	ProxyType         string           // 代理类型
	ListenIP          string           // 监听IP
	ListenPort        int              // 监听端口
	TargetAddressList []string         // 目标地址列表
	RelayOptions      RelayRuleOptions // 转发规则选项
}

// WithProtocols 设置支持的协议
func WithProtocols(protocols []string) ManagerOptions {
	return func(c *_MrgConfig) {
		c.Protocols = protocols
	}
}

// NewManager 创建新的代理管理器实例
func NewManager(opts ...ManagerOptions) Manager {
	config := &_MrgConfig{
		Protocols: []string{"tcp", "udp"}, // 默认支持 tcp 和 udp
	}

	// 应用自定义选项
	for _, opt := range opts {
		opt(config)
	}

	// 创建管理器实例
	pm := &proxyManager{
		forwarders: make(map[string]map[int]Proxy, len(config.Protocols)),
		mutex:      sync.RWMutex{},
	}

	// 初始化协议映射
	for _, proto := range config.Protocols {
		pm.forwarders[proto] = make(map[int]Proxy)
	}

	return pm
}

// AddProxy 添加代理
func (m *proxyManager) AddProxy(proxy Proxy) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	id := proxy.GetID()
	proxyType := "tcp"
	if strings.HasPrefix(proxy.GetType(), "udp") {
		proxyType = "udp"
	}
	m.forwarders[proxyType][id] = proxy
}

// RemoveProxy 移除代理
func (m *proxyManager) RemoveProxy(id int, proxyType string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	delete(m.forwarders[proxyType], id)
}

// GetProxy 获取指定代理
func (m *proxyManager) GetProxy(id int, proxyType string) (Proxy, bool) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	proxy, exists := m.forwarders[proxyType][id]
	return proxy, exists
}

// GetAllProxiesList 获取所有代理列表
func (m *proxyManager) GetAllProxiesList() map[int]ProxyInfo {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	// 首先收集所有代理信息
	proxyList := make(map[int]ProxyInfo)
	for _, proxies := range m.forwarders {
		for _, proxy := range proxies {
			in, out := proxy.GetRateLimit()
			proxyList[proxy.GetID()] = ProxyInfo{
				ID:         proxy.GetID(),
				Type:       proxy.GetType(),
				ListenAddr: fmt.Sprintf("%s:%d", proxy.GetListenIP(), proxy.GetListenPort()),
				RemoteAddr: proxy.GetTargetAddress(),
				Status:     proxy.GetStatus(),
				RateIn:     proxy.GetTrafficInRate(),
				RateOut:    proxy.GetTrafficOutRate(),
				TrafficIn:  proxy.GetTrafficIn(),
				TrafficOut: proxy.GetTrafficOut(),
				RateLimit: _RateLimit{
					In:  in,
					Out: out,
				},
				UpdateTime:  proxy.GetLastUpdateTime(),
				Description: proxy.GetDescription(),
				Connections: proxy.GetCurrentConnections(),
			}
		}
	}

	// 获取所有ID并排序
	ids := make([]int, 0, len(proxyList))
	for id := range proxyList {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	// 创建新的有序map
	sortedList := make(map[int]ProxyInfo)
	for _, id := range ids {
		sortedList[id] = proxyList[id]
	}

	return sortedList
}

// GetTCPProxy 获取指定ID TCP代理列表
func (m *proxyManager) GetTCPProxy(id int) (*TCPProxy, bool) {
	if proxy, exists := m.GetProxy(id, "tcp"); exists {
		if tcpProxy, ok := proxy.(*TCPProxy); ok {
			return tcpProxy, true
		}
	}
	return nil, false
}

// GetAllTCPProxies 获取所有TCP代理列表
func (m *proxyManager) GetAllTCPProxies() map[int]*TCPProxy {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	tcpProxies := make(map[int]*TCPProxy)
	for id, proxy := range m.forwarders["tcp"] {
		if tcpProxy, ok := proxy.(*TCPProxy); ok {
			tcpProxies[id] = tcpProxy
		}
	}
	return tcpProxies
}

// RemoveTCPProxy 移除指定TCP代理
func (m *proxyManager) RemoveTCPProxy(id int) {
	m.RemoveProxy(id, "tcp")
}

// GetUDPProxy 获取指定ID UDP代理列表
func (m *proxyManager) GetUDPProxy(id int) (*UDPProxy, bool) {
	if proxy, exists := m.GetProxy(id, "udp"); exists {
		if udpProxy, ok := proxy.(*UDPProxy); ok {
			return udpProxy, true
		}
	}
	return nil, false
}

// GetAllUDPProxies 获取所有UDP代理列表
func (m *proxyManager) GetAllUDPProxies() map[int]*UDPProxy {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	udpProxies := make(map[int]*UDPProxy)
	for id, proxy := range m.forwarders["udp"] {
		if udpProxy, ok := proxy.(*UDPProxy); ok {
			udpProxies[id] = udpProxy
		}
	}
	return udpProxies
}

// RemoveUDPProxy 移除指定UDP代理
func (m *proxyManager) RemoveUDPProxy(id int) {
	m.RemoveProxy(id, "udp")
}

// CreateProxy 创建新的代理实例
func (m *proxyManager) CreateProxy(opts CreateProxyOptions) (proxy Proxy, err error) {
	if _, _, err = m.isPortInUse(opts.ProxyType, opts.ListenIP, strconv.Itoa(opts.ListenPort)); err != nil {
		return nil, err
	}
	// 应用默认选项
	ApplyDefaultOptions(&opts.RelayOptions)
	// 验证选项
	if err := ValidateOptions(&opts.RelayOptions); err != nil {
		return nil, fmt.Errorf("invalid options: %w", err)
	}
	switch {
	case strings.HasPrefix(opts.ProxyType, "tcp"):
		proxy, err = m.createTCPProxy(opts)
	case strings.HasPrefix(opts.ProxyType, "udp"):
		proxy, err = m.createUDPProxy(opts)
	default:
		return nil, fmt.Errorf("unsupported proxy type: %s", opts.ProxyType)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create %s proxy: %w", opts.ProxyType, err)
	}

	m.mutex.Lock()
	defer m.mutex.Unlock()

	id := proxy.GetID()
	proxyType := "tcp"
	if strings.HasPrefix(proxy.GetType(), "udp") {
		proxyType = "udp"
	}

	if m.forwarders[proxyType] == nil {
		m.forwarders[proxyType] = make(map[int]Proxy)
	}

	m.forwarders[proxyType][id] = proxy

	return proxy, nil
}

// createTCPProxy 创建TCP代理
func (m *proxyManager) createTCPProxy(opts CreateProxyOptions) (*TCPProxy, error) {
	proxy, err := CreateTCPProxy(
		opts.ProxyType,
		opts.ListenIP,
		opts.ListenPort,
		opts.TargetAddressList,
		opts.RelayOptions,
	)
	if err != nil {
		return nil, err
	}

	return proxy, nil
}

// createUDPProxy 创建UDP代理
func (m *proxyManager) createUDPProxy(opts CreateProxyOptions) (*UDPProxy, error) { // 验证选项
	proxy, err := CreateUDPProxy(
		opts.ProxyType,
		opts.ListenIP,
		opts.ListenPort,
		opts.TargetAddressList,
		opts.RelayOptions,
	)
	if err != nil {
		return nil, err
	}

	return proxy, nil
}

// 网络连接相关方法
func (m *proxyManager) getNetworkConn(network, listenAddress string) (interface{}, error) {
	host, portStr, err := net.SplitHostPort(listenAddress)
	if err != nil {
		return nil, fmt.Errorf("invalid address format: %w", err)
	}

	port, err := net.LookupPort(network, portStr)
	if err != nil {
		return nil, fmt.Errorf("invalid port: %w", err)
	}

	switch network {
	case "tcp", "tcp4", "tcp6":
		return net.Listen(network, fmt.Sprintf("%s:%d", host, port))
	case "udp", "udp4", "udp6":
		return net.ListenUDP(network, &net.UDPAddr{
			IP:   net.ParseIP(host),
			Port: port,
		})
	default:
		return nil, fmt.Errorf("unsupported network type: %s", network)
	}
}

func (m *proxyManager) GetTCPListener(addr string) (net.Listener, error) {
	conn, err := m.getNetworkConn("tcp", addr)
	if err != nil {
		return nil, err
	}
	return conn.(net.Listener), nil
}

func (m *proxyManager) GetUDPConn(addr string) (net.PacketConn, error) {
	conn, err := m.getNetworkConn("udp", addr)
	if err != nil {
		return nil, err
	}
	return conn.(net.PacketConn), nil
}

// isPortInUse 检查指定的端口是否已被使用
func (m *proxyManager) isPortInUse(proxyType, listenIP, listenPort string) (host string, port int, err error) {
	// 验证端口
	port, err = net.LookupPort(proxyType, listenPort)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port: %w", err)
	}

	// 构建地址
	addr := &net.TCPAddr{
		IP:   net.ParseIP(listenIP),
		Port: port,
	}

	// 检查端口可用性
	network := strings.ToLower(proxyType)
	if !strings.HasPrefix(network, "tcp") && !strings.HasPrefix(network, "udp") {
		return "", 0, fmt.Errorf("unsupported protocol: %s", proxyType)
	}

	var l interface{ Close() error }
	if strings.HasPrefix(network, "tcp") {
		l, err = net.ListenTCP(network, addr)
	} else {
		l, err = net.ListenUDP(network, (*net.UDPAddr)(addr))
	}

	if err != nil {
		return "", 0, fmt.Errorf("port %d is in use", port)
	}
	defer l.Close()

	return host, port, nil
}
