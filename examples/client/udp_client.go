package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

type UDPStressTest struct {
	sourceAddr   string
	targetAddr   string
	packetSize   int
	duration     time.Duration
	concurrency  int
	totalPackets uint64
	totalBytes   uint64
	errorCount   uint64
}

func NewUDPStressTest() *UDPStressTest {
	return &UDPStressTest{
		packetSize:  1024,
		duration:    5 * time.Second,
		concurrency: runtime.NumCPU(),
	}
}

func (t *UDPStressTest) parseFlags() {
	flag.StringVar(&t.sourceAddr, "source", ":0", "源地址")
	flag.StringVar(&t.targetAddr, "target", "127.0.0.1:4554", "目标地址")
	flag.IntVar(&t.packetSize, "size", 1024, "数据包大小(bytes)")
	flag.DurationVar(&t.duration, "duration", 1*time.Minute, "测试持续时间")
	flag.IntVar(&t.concurrency, "concurrency", runtime.NumCPU(), "并发数")
	flag.Parse()
}

func (t *UDPStressTest) runSender(wg *sync.WaitGroup) {
	defer wg.Done()

	conn, err := net.Dial("udp", t.targetAddr)
	if err != nil {
		log.Printf("连接错误: %v", err)
		return
	}
	defer conn.Close()

	payload := make([]byte, t.packetSize)
	startTime := time.Now()

	for time.Since(startTime) < t.duration {
		_, err := conn.Write(payload)
		if err != nil {
			atomic.AddUint64(&t.errorCount, 1)
			continue
		}
		atomic.AddUint64(&t.totalPackets, 1)
		atomic.AddUint64(&t.totalBytes, uint64(t.packetSize))
	}
}

func (t *UDPStressTest) runReceiver(wg *sync.WaitGroup) {
	defer wg.Done()

	addr, err := net.ResolveUDPAddr("udp", t.sourceAddr)
	if err != nil {
		log.Printf("地址解析错误: %v", err)
		return
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Printf("监听错误: %v", err)
		return
	}
	defer conn.Close()

	buffer := make([]byte, t.packetSize*2)
	startTime := time.Now()

	for time.Since(startTime) < t.duration {
		conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, _, err := conn.ReadFromUDP(buffer)
		if err != nil {
			continue
		}
		atomic.AddUint64(&t.totalPackets, 1)
		atomic.AddUint64(&t.totalBytes, uint64(n))
	}
}

func (t *UDPStressTest) Run() {
	t.parseFlags()

	var wg sync.WaitGroup

	// 发送协程
	for i := 0; i < t.concurrency; i++ {
		wg.Add(1)
		go t.runSender(&wg)
	}

	// 接收协程
	for i := 0; i < t.concurrency; i++ {
		wg.Add(1)
		go t.runReceiver(&wg)
	}

	wg.Wait()

	// 计算结果
	totalTime := float64(t.duration.Seconds())
	throughput := float64(t.totalBytes) / totalTime / (1024 * 1024)
	pps := float64(t.totalPackets) / totalTime

	fmt.Printf("测试结果:\n")
	fmt.Printf("总包数: %d\n", t.totalPackets)
	fmt.Printf("总字节数: %d\n", t.totalBytes)
	fmt.Printf("吞吐量: %.2f MB/s\n", throughput)
	fmt.Printf("包率: %.2f pps\n", pps)
	fmt.Printf("错误数: %d\n", t.errorCount)
}

func main() {
	test := NewUDPStressTest()
	test.Run()
}
