package forwarder

import (
	"container/heap"
	"context"
	"fmt"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type _UDPMetrics struct {
	PacketsReceived   atomic.Int64
	PacketsSent       atomic.Int64
	PacketsDropped    atomic.Int64
	ActiveSessions    atomic.Int64
	ProcessingLatency atomic.Int64
}

// UDPProxy UDP代理实现
type UDPProxy struct {
	BaseProxy
	listenConn                                    net.PacketConn
	listenConnMutex                               sync.Mutex
	relayChs                                      []chan *udpPackage
	replyCh                                       chan *udpPackage
	udpPackageSize                                int
	targetConnectSessions                         sync.Map
	Upm                                           bool
	ShortMode                                     bool
	ctx                                           context.Context
	cancel                                        context.CancelFunc
	SingleProxyMaxUDPReadTargetDatagoroutineCount int64
	sessions                                      struct {
		store    sync.Map
		timeouts *timeoutQueue
	}
	metrics *_UDPMetrics
}

// udpPackage UDP数据包结构
type udpPackage struct {
	dataSize   int
	data       *[]byte
	remoteAddr net.Addr
}

// udpTargetConnSession UDP目标连接会话
type udpTargetConnSession struct {
	targetConn     *net.UDPConn
	lastTime       time.Time
	handlerStarted int32
}

// timeoutQueue 管理UDP会话超时
type timeoutQueue struct {
	items []*timeoutItem
	mu    sync.Mutex
}

type timeoutItem struct {
	key      string
	lastTime time.Time
	index    int
}

func (q *timeoutQueue) Len() int           { return len(q.items) }
func (q *timeoutQueue) Less(i, j int) bool { return q.items[i].lastTime.Before(q.items[j].lastTime) }
func (q *timeoutQueue) Swap(i, j int) {
	q.items[i], q.items[j] = q.items[j], q.items[i]
	q.items[i].index = i
	q.items[j].index = j
}

func (q *timeoutQueue) Push(x interface{}) {
	item := x.(*timeoutItem)
	item.index = len(q.items)
	q.items = append(q.items, item)
}

func (q *timeoutQueue) Pop() interface{} {
	old := q.items
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.index = -1
	q.items = old[0 : n-1]
	return item
}

// CreateUDPProxy 创建新的UDP代理实例
func CreateUDPProxy(proxyType, listenIP string, listenPort int, targetAddressList []string, options RelayRuleOptions) (*UDPProxy, error) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &UDPProxy{
		BaseProxy: BaseProxy{
			TCPUDPProxyCommonConf: TCPUDPProxyCommonConf{
				BaseProxyConf: BaseProxyConf{
					ProxyType: proxyType,
				},
				listenIP:          listenIP,
				listenPort:        listenPort,
				targetAddressList: targetAddressList,
				targetPort:        0,
				safeMode:          options.SafeMode,
			},
			RelayRuleOptions: options,
		},
		Upm:       options.UDPProxyPerformanceMode,
		ShortMode: options.UDPShortMode,
		ctx:       ctx,
		cancel:    cancel,
		metrics:   &_UDPMetrics{},
	}

	// 初始化配置
	p.SetUDPPacketSize(options.UDPPackageSize)
	p.SetMaxConnections(options.SingleProxyMaxUDPReadTargetDataRoutineCount)
	p.SetRateLimit(options.InRateLimit, options.OutRateLimit)

	// 初始化会话管理
	p.sessions.timeouts = &timeoutQueue{
		items: make([]*timeoutItem, 0),
	}
	p.sessions.store = sync.Map{}

	return p, nil
}

// getHandleRoutineNum 获取处理例程的数量
func (p *UDPProxy) getHandleRoutineNum() int {
	if p.Upm {
		return runtime.NumCPU()
	}
	return 1
}

// SetUDPPacketSize 设置UDP包大小
func (p *UDPProxy) SetUDPPacketSize(size int) {
	if size <= 0 || size > 65507 {
		p.udpPackageSize = _DefaultUDPPackageSize
		return
	}
	p.udpPackageSize = size
}

// GetUDPPacketSize 获取UDP包大小
func (p *UDPProxy) GetUDPPacketSize() int {
	return p.udpPackageSize
}

// Start 启动UDP代理
func (p *UDPProxy) Start() error {
	p.listenConnMutex.Lock()
	defer p.listenConnMutex.Unlock()

	if p.IsRunning() {
		return fmt.Errorf("proxy %s is already running", p.String())
	}

	// 创建监听连接 - 使用 GlobalManager 获取 UDP 连接
	ln, err := globalManager.GetUDPConn(p.GetListenAddress())
	if err != nil {
		return fmt.Errorf("cannot start proxy %s: %v", p.String(), err)
	}

	p.listenConn = ln
	p.setRunning(true)
	globalManager.logger.Infof("[Port forwarding][Started] %s", p.String())

	// 初始化工作协程
	workerCount := p.getHandleRoutineNum()
	p.relayChs = make([]chan *udpPackage, workerCount)
	for i := 0; i < workerCount; i++ {
		p.relayChs[i] = make(chan *udpPackage, 1000)
		go p.handleIncoming(p.relayChs[i])
	}

	p.replyCh = make(chan *udpPackage, 1024)

	// 启动处理协程
	go p.ListenHandler(p.listenConn) // 处理入站数据
	go p.cleanupSessions()           // 清理过期会话

	globalManager.logger.Infof("[%s] UDP proxy started with %d workers", p.GetKey(), workerCount)

	return nil
}

// Stop 停止UDP代理
func (p *UDPProxy) Stop() error {
	p.listenConnMutex.Lock()
	defer p.listenConnMutex.Unlock()

	if !p.IsRunning() {
		return fmt.Errorf("proxy %s is not running", p.String())
	}

	// 关闭监听连接
	if p.listenConn != nil {
		p.listenConn.Close()
		p.listenConn = nil
	}

	// 取消上下文
	if p.cancel != nil {
		p.cancel()
	}

	// 清理会话
	p.sessions.store.Range(func(key, value interface{}) bool {
		if session, ok := value.(*udpTargetConnSession); ok {
			if session.targetConn != nil {
				session.targetConn.Close()
			}
		}
		p.sessions.store.Delete(key)
		// 增加全局连接数
		p.AddCurrentConnections(-1)
		return true
	})

	// 关闭通道
	if p.replyCh != nil {
		close(p.replyCh)
		p.replyCh = nil
	}

	for i := range p.relayChs {
		if p.relayChs[i] != nil {
			close(p.relayChs[i])
			p.relayChs[i] = nil
		}
	}

	p.setRunning(false)
	globalManager.logger.Infof("[Port forwarding][Stopped][%s]", p.String())

	return nil
}

// Restart 重启UDP代理
func (p *UDPProxy) Restart() error {
	globalManager.logger.Infof("[%s] Restarting UDP proxy...", p.GetKey())

	err := p.Stop()
	if err != nil && !strings.Contains(err.Error(), "not running") {
		globalManager.logger.Errorf("[%s] Failed to stop UDP proxy: %v", p.GetKey(), err)
		return err
	}

	globalManager.logger.Infof("[%s] UDP proxy stopped, reinitializing...", p.GetKey())

	// 等待资源完全释放
	time.Sleep(100 * time.Millisecond)

	// 重新初始化上下文
	var ctx context.Context
	ctx, p.cancel = context.WithCancel(context.Background())
	p.ctx = ctx

	// 重新初始化会话管理
	p.sessions.timeouts = &timeoutQueue{
		items: make([]*timeoutItem, 0),
	}
	p.sessions.store = sync.Map{}

	// 重新初始化通道
	workerCount := p.getHandleRoutineNum()
	p.relayChs = make([]chan *udpPackage, workerCount)
	for i := 0; i < workerCount; i++ {
		p.relayChs[i] = make(chan *udpPackage, 1000)
	}
	p.replyCh = make(chan *udpPackage, 1024)

	// 启动代理
	err = p.Start()
	if err != nil {
		globalManager.logger.Errorf("[%s] Failed to restart UDP proxy: %v", p.GetKey(), err)
		return err
	}

	globalManager.logger.Infof("[%s] UDP proxy successfully restarted", p.GetKey())
	return nil
}

// ReadFromTargetOnce 判断是否只从目标读取一次
func (p *UDPProxy) ReadFromTargetOnce() bool {
	return p.targetPort == 53 || p.ShortMode
}

// GetStatus 获取UDP代理的当前状态
func (p *UDPProxy) GetStatus() (ok bool) {
	return p.IsRunning()
}

// ListenHandler 监听处理器
func (p *UDPProxy) ListenHandler(ln net.PacketConn) {
	inDataBuf := globalManager.bufferPool.GetUDPBuffer(p.GetUDPPacketSize())
	defer globalManager.bufferPool.PutUDPBuffer(inDataBuf)
	i := uint64(0)
	connMap := make(map[string]struct{})

	for {
		select {
		case <-p.ctx.Done():
			return
		default:
			inDataBufSize, remoteAddr, err := ln.ReadFrom(inDataBuf)
			if err != nil {
				if strings.Contains(err.Error(), `smaller than the datagram`) {
					globalManager.logger.Errorf("[%s] UDP package max length setting is too small, please reset", p.GetKey())
				} else if !strings.Contains(err.Error(), "use of closed network connection") {
					globalManager.logger.Errorf("%s ReadFromUDP error: %s", p.String(), err.Error())
				}
				continue
			}

			remoteAddrStr := remoteAddr.String()
			if !p.SafeCheck(remoteAddrStr) {
				if _, logged := connMap[remoteAddrStr]; !logged {
					globalManager.logger.Warnf("[%s] New connection [%s] failed safety check", p.GetKey(), remoteAddrStr)
					connMap[remoteAddrStr] = struct{}{}
				}
				continue
			}

			if _, ok := p.targetConnectSessions.Load(remoteAddrStr); !ok {
				if _, logged := connMap[remoteAddrStr]; !logged {
					globalManager.logger.Infof("[%s] New connection [%s] passed safety check", p.GetKey(), remoteAddrStr)
					connMap[remoteAddrStr] = struct{}{}
				}
			}

			if err := p.checkRateLimit(true, inDataBufSize); err != nil {
				globalManager.logger.Warnf("[%s] %v, dropping package", p.GetKey(), err)
				continue
			}

			data := globalManager.bufferPool.GetUDPBuffer(inDataBufSize)
			copy(data, inDataBuf[:inDataBufSize])

			inUDPPack := udpPackage{dataSize: inDataBufSize, data: &data, remoteAddr: remoteAddr.(*net.UDPAddr)}

			p.relayChs[i%uint64(p.getHandleRoutineNum())] <- &inUDPPack
			i++
		}
	}
}

// Forwarder 转发器
func (p *UDPProxy) Forwarder(replyCh chan *udpPackage) {
	for udpMsg := range replyCh {
		se, ok := p.targetConnectSessions.Load(udpMsg.remoteAddr.String())

		if !ok {
			err := p.CheckReadTargetDataRoutineLimit()
			if err != nil {
				globalManager.logger.Warnf("[%s] Forwarding stopped: %s", p.GetKey(), err.Error())
				globalManager.bufferPool.PutUDPBuffer(*udpMsg.data)
				continue
			}
		}

		var session *udpTargetConnSession
		if ok {
			session = se.(*udpTargetConnSession)
		} else {
			session = &udpTargetConnSession{}
		}

		if !ok {
			addr := p.GetTargetAddress()
			tgAddr, err := net.ResolveUDPAddr("udp", addr)
			if err != nil {
				globalManager.logger.Errorf("[%s] UDP forwarding target address [%s] resolution error: %s", p.GetKey(), addr, err.Error())
				globalManager.bufferPool.PutUDPBuffer(*udpMsg.data)
				continue
			}
			targetConn, err := net.DialUDP("udp", nil, tgAddr)
			if err != nil {
				globalManager.logger.Errorf("[%s] UDP forwarding target address [%s] connection error: %s", p.GetKey(), addr, err.Error())
				globalManager.bufferPool.PutUDPBuffer(*udpMsg.data)
				continue
			}

			targetConn.SetWriteBuffer(p.GetUDPPacketSize())
			targetConn.SetReadBuffer(p.GetUDPPacketSize())

			session.targetConn = targetConn
		}
		session.lastTime = time.Now()

		if !p.ReadFromTargetOnce() {
			p.targetConnectSessions.Store(udpMsg.remoteAddr.String(), session)
		}

		p.ReceiveDataCallback(int64(udpMsg.dataSize))

		_, err := session.targetConn.Write(*udpMsg.data)
		if err != nil {
			globalManager.logger.Errorf("[%s] Error forwarding data to target port: %s", p.GetKey(), err.Error())
			session.targetConn.Close()
			globalManager.bufferPool.PutUDPBuffer(*udpMsg.data)
			continue
		}
		globalManager.bufferPool.PutUDPBuffer(*udpMsg.data)

		if !ok {
			go p.handlerDataFromTargetAddress(udpMsg.remoteAddr, session.targetConn)
		}
	}
}

// CheckReadTargetDataRoutineLimit 检查读取目标数据的例程限制
func (p *UDPProxy) CheckReadTargetDataRoutineLimit() error {
	counters := globalManager.GetCounters()

	// 检查全局UDP协程数限制
	if counters.GetUDPCurrentRoutines() >= counters.GetUDPMaxRoutines() {
		return fmt.Errorf("exceeded global UDP read target data routine count limit [%d]",
			counters.GetUDPMaxRoutines())
	}

	// 检查单个代理的连接数限制
	if p.GetCurrentConnections() >= p.SingleProxyMaxConnections {
		return fmt.Errorf("exceeded single port UDP read target data routine count limit [%d]",
			p.SingleProxyMaxConnections)
	}
	return nil
}

// CheckTargetUDPConnectSessions 检查目标UDP连接会话
func (p *UDPProxy) CheckTargetUDPConnectSessions() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			if p.GetCurrentConnections() <= 0 {
				continue
			}

			p.targetConnectSessions.Range(func(key, value interface{}) bool {
				session := value.(*udpTargetConnSession)
				if time.Since(session.lastTime) >= 30*time.Second {
					session.targetConn.Close()
					p.targetConnectSessions.Delete(key)
				}
				return true
			})
		}
	}
}

// GetTargetUDPAddr 获取目标UDP地址
func (p *UDPProxy) GetTargetUDPAddr() *net.UDPAddr {
	addr := p.GetTargetAddress()
	globalManager.logger.Debugf("[%s] Resolving target address: %s", p.GetKey(), addr)

	targetAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		globalManager.logger.Errorf("[%s] Failed to resolve target address %s: %v", p.GetKey(), addr, err)
		return nil
	}
	return targetAddr
}

func (p *UDPProxy) handlerDataFromTargetAddress(raddr net.Addr, tgConn *net.UDPConn) {
	readBuffer := globalManager.bufferPool.GetUDPBuffer(p.GetUDPPacketSize())
	var session *udpTargetConnSession
	sessionKey := raddr.String()

	defer func() {
		globalManager.bufferPool.PutUDPBuffer(readBuffer)
		if p.ReadFromTargetOnce() {
			tgConn.Close()
		} else {
			p.targetConnectSessions.Delete(sessionKey)
		}
		globalManager.logger.Infof("[%s] Target address [%s] closed connection [%s]", p.GetKey(), tgConn.RemoteAddr().String(), tgConn.LocalAddr().String())
	}()

	var targetConn *net.UDPConn

	for {
		targetConn = nil
		session = nil

		timeout := 1200 * time.Millisecond
		if p.ReadFromTargetOnce() {
			timeout = 300 * time.Millisecond
		}

		if p.ReadFromTargetOnce() {
			targetConn = tgConn
		} else {
			se, ok := p.targetConnectSessions.Load(sessionKey)
			if !ok {
				return
			}
			session = se.(*udpTargetConnSession)
			targetConn = session.targetConn
		}

		targetConn.SetReadDeadline(time.Now().Add(timeout))
		n, _, err := targetConn.ReadFromUDP(readBuffer)
		if err != nil {
			errStr := err.Error()
			if strings.Contains(errStr, `i/o timeout`) && !p.ReadFromTargetOnce() {
				continue
			}
			if !strings.Contains(errStr, `use of closed network connection`) {
				globalManager.logger.Errorf("[%s] targetConn ReadFromUDP error: %s", p.GetKey(), err.Error())
			}
			return
		}

		data := globalManager.bufferPool.GetUDPBuffer(n)
		copy(data, readBuffer[:n])
		udpMsg := udpPackage{dataSize: n, data: &data, remoteAddr: raddr}

		select {
		case p.replyCh <- &udpMsg:
		default:
			globalManager.logger.Warnf("[%s] Reply channel is full, discarding message", p.GetKey())
			globalManager.bufferPool.PutUDPBuffer(data)
		}

		if p.ReadFromTargetOnce() {
			return
		}

		if _, ok := p.targetConnectSessions.Load(sessionKey); !ok {
			return
		}
	}
}

// getOrCreateSession 获取或创建会话
func (p *UDPProxy) getOrCreateSession(key string) (*udpTargetConnSession, error) {
	if session, ok := p.sessions.store.Load(key); ok {
		return session.(*udpTargetConnSession), nil
	}

	// 创建新会话
	targetConn, err := net.DialUDP("udp", nil, p.GetTargetUDPAddr())
	if err != nil {
		return nil, err
	}

	session := &udpTargetConnSession{
		targetConn: targetConn,
		lastTime:   time.Now(),
	}

	p.sessions.store.Store(key, session)
	p.metrics.ActiveSessions.Add(1)
	// 增加全局连接数
	p.AddCurrentConnections(1)

	globalManager.logger.Infof("[%s] New UDP session created for %s", p.GetKey(), key)
	return session, nil
}

// cleanupSessions 清理过期的UDP会话
func (p *UDPProxy) cleanupSessions() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.sessions.timeouts.mu.Lock()
			now := time.Now()
			for p.sessions.timeouts.Len() > 0 {
				if now.Sub(p.sessions.timeouts.items[0].lastTime) < 30*time.Second {
					break
				}
				item := heap.Pop(p.sessions.timeouts).(*timeoutItem)
				if session, ok := p.sessions.store.LoadAndDelete(item.key); ok {
					session.(*udpTargetConnSession).targetConn.Close()
					p.metrics.ActiveSessions.Add(-1)
				}
			}
			p.sessions.timeouts.mu.Unlock()
		}
	}
}

// handleIncoming 处理入站数据
func (p *UDPProxy) handleIncoming(ch chan *udpPackage) {
	for pkg := range ch {
		globalManager.logger.Debugf("[%s] Received data from client: %s", p.GetKey(), pkg.remoteAddr.String())

		sessionKey := pkg.remoteAddr.String()
		session, err := p.getOrCreateSession(sessionKey)
		if err != nil {
			globalManager.logger.Errorf("[%s] Failed to create session for %s: %v", p.GetKey(), sessionKey, err)
			globalManager.bufferPool.PutUDPBuffer(*pkg.data)
			continue
		}

		// 发送数据到目标
		n, err := session.targetConn.Write(*pkg.data)
		if err != nil {
			globalManager.logger.Errorf("[%s] Failed to write to target: %v", p.GetKey(), err)
			globalManager.bufferPool.PutUDPBuffer(*pkg.data)
			continue
		}

		// 启动响应处理协程
		if atomic.CompareAndSwapInt32(&session.handlerStarted, 0, 1) {
			go p.handleDataFromTargetAddress(session.targetConn, pkg.remoteAddr)
		}

		p.ReceiveDataCallback(int64(n))
		globalManager.bufferPool.PutUDPBuffer(*pkg.data)
	}
}

// handleDataFromTargetAddress 处理出站数据
func (p *UDPProxy) handleDataFromTargetAddress(targetConn *net.UDPConn, clientAddr net.Addr) {
	if targetConn == nil || clientAddr == nil {
		globalManager.logger.Errorf("[%s] Invalid connection or address", p.GetKey())
		return
	}

	buf := make([]byte, p.GetUDPPacketSize())
	sessionKey := clientAddr.String()

	defer func() {
		if r := recover(); r != nil {
			globalManager.logger.Errorf("[%s] Panic recovered: %v", p.GetKey(), r)
		}
		if targetConn != nil {
			targetConn.Close()
		}
		p.sessions.store.Delete(sessionKey)
		p.metrics.ActiveSessions.Add(-1)
		// 增加全局连接数
		p.AddCurrentConnections(-1)
	}()

	for {
		targetConn.SetReadDeadline(time.Now().Add(30 * time.Second))

		n, _, err := targetConn.ReadFromUDP(buf)
		if err != nil {
			if !strings.Contains(err.Error(), "use of closed network connection") {
				globalManager.logger.Errorf("[%s] Read error: %v", p.GetKey(), err)
			}
			return
		}

		// 检查出站流量限制
		if err := p.checkRateLimit(false, n); err != nil {
			globalManager.logger.Warnf("[%s] Rate limit exceeded: %v", p.GetKey(), err)
			time.Sleep(time.Millisecond * 100)
			continue
		}

		// 写回客户端
		if p.listenConn != nil {
			_, err = p.listenConn.WriteTo(buf[:n], clientAddr)
			if err != nil {
				globalManager.logger.Errorf("[%s] Write error: %v", p.GetKey(), err)
				continue
			}
			p.SendDataCallback(int64(n))
		}
	}
}
