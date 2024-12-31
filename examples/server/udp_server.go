package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type UDPServer struct {
	listenAddr  string
	packetCount uint64
	byteCount   uint64
	concurrency int
}

func NewUDPServer() *UDPServer {
	return &UDPServer{
		listenAddr:  ":5445",
		concurrency: runtime.NumCPU(),
	}
}

func (s *UDPServer) parseFlags() {
	flag.StringVar(&s.listenAddr, "listen", ":5445", "监听地址")
	flag.IntVar(&s.concurrency, "concurrency", runtime.NumCPU(), "并发处理协程数")
	flag.Parse()
}

func (s *UDPServer) handleConnection(conn *net.UDPConn, done chan struct{}) {
	defer func() {
		done <- struct{}{}
	}()

	// 添加统计变量
	var lastPackets, lastBytes uint64
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	buffer := make([]byte, 65535)

	for {
		select {
		case <-ticker.C:
			// 获取当前统计
			currentPackets := atomic.LoadUint64(&s.packetCount)
			currentBytes := atomic.LoadUint64(&s.byteCount)

			// 计算速率
			packetRate := currentPackets - lastPackets
			byteRate := currentBytes - lastBytes

			fmt.Printf("速率统计 - 流量: %.2f MB/s, 包数: %d/s\n", float64(byteRate)/(1024*1024), packetRate)

			// 更新上次统计
			lastPackets = currentPackets
			lastBytes = currentBytes

		default:
			// 设置读取超时，避免阻塞
			conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))

			n, remoteAddr, err := conn.ReadFromUDP(buffer)
			if err != nil {
				if !strings.Contains(err.Error(), "i/o timeout") {
					log.Printf("读取错误: %v", err)
				}
				continue
			}

			// 更新统计
			atomic.AddUint64(&s.packetCount, 1)
			atomic.AddUint64(&s.byteCount, uint64(n))

			// 回显数据
			_, err = conn.WriteToUDP(buffer[:n], remoteAddr)
			if err != nil {
				log.Printf("写入错误: %v", err)
			}
		}
	}
}

func (s *UDPServer) Run() {
	s.parseFlags()

	addr, err := net.ResolveUDPAddr("udp", s.listenAddr)
	if err != nil {
		log.Fatalf("地址解析错误: %v", err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Fatalf("监听错误: %v", err)
	}
	defer conn.Close()

	// 设置更大的接收缓冲区
	err = conn.SetReadBuffer(4 * 1024 * 1024)
	if err != nil {
		log.Printf("设置缓冲区错误: %v", err)
	}

	log.Printf("UDP服务器启动，监听地址: %s", s.listenAddr)

	var wg sync.WaitGroup
	done := make(chan struct{}, s.concurrency)

	// 启动多个处理协程
	for i := 0; i < s.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handleConnection(conn, done)
		}()
	}

	tk := time.NewTicker(1 * time.Second)
	defer tk.Stop()

	// 定期打印统计信息
	go func() {
		var lastPackets, lastBytes uint64
		for {
			select {
			case <-done:
				return
			case <-tk.C:
				// 获取当前统计
				currentPackets := atomic.LoadUint64(&s.packetCount)
				currentBytes := atomic.LoadUint64(&s.byteCount)

				// 计算速率
				packetRate := currentPackets - lastPackets
				byteRate := currentBytes - lastBytes

				fmt.Printf("统计信息 - 总计：包数=%d 字节数=%d\n速率：%.2f MB/s %d 包/秒\n",
					currentPackets,
					currentBytes,
					float64(byteRate)/(1024*1024),
					packetRate)

				// 更新上次统计
				lastPackets = currentPackets
				lastBytes = currentBytes

			default:
				runtime.Gosched()
			}
		}
	}()

	wg.Wait()
}

func main() {
	server := NewUDPServer()
	server.Run()
}
