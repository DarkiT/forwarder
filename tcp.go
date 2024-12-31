package forwarder

import (
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// TCPProxy TCP代理实现
type TCPProxy struct {
	BaseProxy
	listenConn      net.Listener // 监听连接
	listenConnMutex sync.Mutex   // 监听连接锁
	connMap         sync.Map     // 连接映射表
}

// CreateTCPProxy 创建新的TCP代理实例
func CreateTCPProxy(proxyType, listenIP string, listenPort int, targetAddressList []string, options RelayRuleOptions) (*TCPProxy, error) {
	p := &TCPProxy{
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
	}
	// 设置最大连接数和流量限制
	p.SetMaxConnections(options.SingleProxyMaxTCPConnections)
	p.SetRateLimit(options.InRateLimit, options.OutRateLimit)
	return p, nil
}

// GetStatus 获取TCP代理的当前状态
func (p *TCPProxy) GetStatus() (ok bool) {
	return p.IsRunning()
}

// Start  启动TCP代理
func (p *TCPProxy) Start() error {
	p.listenConnMutex.Lock()
	defer p.listenConnMutex.Unlock()

	if p.IsRunning() {
		return fmt.Errorf("proxy %s is already running", p.String())
	}

	// 获取TCP监听器
	listener, err := globalManager.GetTCPListener(p.GetListenAddress())
	if err != nil {
		globalManager.logger.Errorf("[%s] Failed to listen: %v", p.GetKey(), err)
		return err
	}

	p.listenConn = listener
	p.setRunning(true)
	globalManager.logger.Infof("[Port forwarding][Started] %s", p.String())

	// 启动连接接收协程
	go p.acceptConnections()

	return nil
}

// Stop  停止TCP代理
func (p *TCPProxy) Stop() error {
	p.listenConnMutex.Lock()
	defer p.listenConnMutex.Unlock()

	if !p.IsRunning() {
		return fmt.Errorf("proxy %s is not running", p.String())
	}

	if p.listenConn != nil {
		p.listenConn.Close()
	}

	p.connMap.Range(func(key, value interface{}) bool {
		conn := value.(net.Conn)
		conn.Close()
		return true
	})

	p.setRunning(false)
	globalManager.logger.Infof("[Port forwarding][Stopped] %s", p.String())

	return nil
}

// Restart 重启TCP代理
func (p *TCPProxy) Restart() error {
	globalManager.logger.Infof("[%s] Restarting TCP proxy...", p.GetKey())

	// 停止代理
	err := p.Stop()
	if err != nil && !strings.Contains(err.Error(), "not running") {
		globalManager.logger.Errorf("[%s] Failed to stop TCP proxy: %v", p.GetKey(), err)
		return err
	}

	globalManager.logger.Infof("[%s] TCP proxy stopped, reinitializing...", p.GetKey())

	// 等待资源完全释放
	time.Sleep(100 * time.Millisecond)

	// 重新初始化连接映射
	p.connMap = sync.Map{}

	// 启动代理
	err = p.Start()
	if err != nil {
		globalManager.logger.Errorf("[%s] Failed to restart TCP proxy: %v", p.GetKey(), err)
		return err
	}

	globalManager.logger.Infof("[%s] TCP proxy successfully restarted", p.GetKey())
	return nil
}

// CheckConnectionsLimit 检查是否超过连接数限制
func (p *TCPProxy) CheckConnectionsLimit() error {
	gm := globalManager
	counters := gm.GetCounters()

	// 检查全局TCP连接数限制
	if counters.GetTCPCurrentConnections() >= counters.GetTCPMaxConnections() {
		return fmt.Errorf("exceeded global TCP max connections limit [%d]",
			counters.GetTCPMaxConnections())
	}

	// 检查单个代理的连接数限制
	if p.GetCurrentConnections() >= p.SingleProxyMaxConnections {
		return fmt.Errorf("exceeded single port TCP max connections limit [%d]",
			p.SingleProxyMaxConnections)
	}

	return nil
}

// acceptConnections 接受新的TCP连接
func (p *TCPProxy) acceptConnections() {
	for {
		newConn, err := p.listenConn.Accept()
		if err != nil {
			if strings.Contains(err.Error(), "use of closed network connection") {
				break
			}
			globalManager.logger.Errorf("Cannot accept connection: %s", err.Error())
			continue
		}

		err = p.CheckConnectionsLimit()
		if err != nil {
			globalManager.logger.Warnf("[%s] Exceeded max connections limit, rejecting new connection: %s", p.GetKey(), err.Error())
			newConn.Close()
			continue
		}

		if !p.SafeCheck(newConn.RemoteAddr().String()) {
			globalManager.logger.Warnf("[%s] New connection [%s] failed safety check", p.GetKey(), newConn.RemoteAddr().String())
			newConn.Close()
			continue
		}

		globalManager.logger.Infof("[%s] New connection [%s] passed safety check", p.GetKey(), newConn.RemoteAddr().String())

		p.connMap.Store(newConn.RemoteAddr().String(), newConn)
		p.AddCurrentConnections(1)
		go p.handle(newConn)
	}
}

// optimizeTCPConn 优化TCP连接参数
func (p *TCPProxy) optimizeTCPConn(conn *net.TCPConn) {
	conn.SetNoDelay(true)
	conn.SetKeepAlive(true)
	conn.SetKeepAlivePeriod(time.Second * 30)

	// 使用全局定义的缓冲区大小
	rate := p.GetTrafficInRate()
	size := _TCPDefaultStreamBufferSize
	if rate > 10*1024*1024 {
		size = _TCPMaxStreamBufferSize
	}

	conn.SetReadBuffer(size)
	conn.SetWriteBuffer(size)
}

// handle 处理新连接
func (p *TCPProxy) handle(conn net.Conn) {
	targetConn, err := net.DialTimeout("tcp", p.GetTargetAddress(), 30*time.Second)
	if err != nil {
		globalManager.logger.Errorf("[%s] Failed to connect to target: %v", p.GetKey(), err)
		conn.Close()
		return
	}

	// 优化连接参数
	if tc, ok := conn.(*net.TCPConn); ok {
		p.optimizeTCPConn(tc)
	}
	if tc, ok := targetConn.(*net.TCPConn); ok {
		p.optimizeTCPConn(tc)
	}

	defer func() {
		targetConn.Close()
		conn.Close()
		p.AddCurrentConnections(-1)
		p.connMap.Delete(conn.RemoteAddr().String())
	}()

	p.relayData(targetConn, conn)
}

// relayData 在源和目标之间转发数据
func (p *TCPProxy) relayData(dst io.ReadWriter, src io.ReadWriter) {
	var wg sync.WaitGroup
	wg.Add(2)

	copy := func(writer io.Writer, reader io.Reader, callback func(int64), isIncoming bool) {
		defer wg.Done()

		// 使用全局缓冲池
		rate := p.GetTrafficInRate()
		buf := globalManager.bufferPool.GetTCPBuffer(rate)
		defer globalManager.bufferPool.PutTCPBuffer(buf)

		// 设置超时
		if tc, ok := reader.(*net.TCPConn); ok {
			tc.SetReadDeadline(time.Now().Add(time.Second * 30))
		}
		if tc, ok := writer.(*net.TCPConn); ok {
			tc.SetWriteDeadline(time.Now().Add(time.Second * 30))
		}

		for {
			nr, er := reader.Read(buf)
			if nr > 0 {
				// 检查流量限制
				if err := p.checkRateLimit(isIncoming, nr); err != nil {
					globalManager.logger.Warnf("[%s] Rate limit exceeded: %v", p.GetKey(), err)
					time.Sleep(time.Millisecond * 100)
					continue
				}

				// 写入数据
				nw, ew := writer.Write(buf[:nr])
				if nw > 0 {
					callback(int64(nw))
				}
				if ew != nil {
					break
				}
				if nr != nw {
					break
				}
			}
			if er != nil {
				if er != io.EOF {
					globalManager.logger.Errorf("[%s] Read error: %v", p.GetKey(), er)
				}
				break
			}
		}
	}

	// 启动双向数据转发
	go copy(dst, src, p.ReceiveDataCallback, true)
	go copy(src, dst, p.SendDataCallback, false)

	wg.Wait()
}
