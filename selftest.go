package main

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

// SteamKopru.exe -selftest : Steam init, relay, and a loopback tunnel test
// (echo server <- host <- Steam P2P to self <- client <- test data).
func init() {
	if len(os.Args) < 2 || os.Args[1] != "-selftest" {
		return
	}
	logSink = func(s string) { fmt.Println(s) }
	if err := startCore(); err != nil {
		fmt.Println("INIT FAIL:", err)
		os.Exit(1)
	}
	defer os.Exit(0)
	defer shutdownCore()
	fmt.Printf("user=%s id=%d\n", myName(), mySteamID())
	waitRelay()

	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Println("echo fail", err)
		return
	}
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	// two shared ports: the echo server and a second one, to test multi-port
	echo2, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() {
		for {
			c, err := echo2.Accept()
			if err != nil {
				return
			}
			go func() { c.Write([]byte("two!")); c.Close() }()
		}
	}()
	// third: a UDP echo server
	uecho, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	go func() {
		b := make([]byte, 65536)
		for {
			n, a, err := uecho.ReadFromUDP(b)
			if err != nil {
				if readErrFatal(err) {
					return
				}
				continue
			}
			uecho.WriteToUDP(b[:n], a)
		}
	}()
	if err := startHost([]share{
		{Port: echo.Addr().(*net.TCPAddr).Port, Name: "echo", Proto: protoTCP},
		{Port: echo2.Addr().(*net.TCPAddr).Port, Name: "second", Proto: protoTCP},
		{Port: uecho.LocalAddr().(*net.UDPAddr).Port, Name: "udpecho", Proto: protoUDP},
	}); err != nil {
		fmt.Println("HOST FAIL:", err)
		return
	}
	shares, err := queryHost(mySteamID())
	if err != nil {
		fmt.Println("QUERY FAIL:", err)
		return
	}
	fmt.Println("RESULT: share list", shares)
	maps, err := startClient(mySteamID(), shares)
	if err != nil {
		fmt.Println("CLIENT FAIL:", err)
		return
	}
	fmt.Println("RESULT: mappings", maps)
	if uc, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", maps[2].LocalUDP)); err == nil {
		got, lat := 0, time.Duration(0)
		rb := make([]byte, 2048)
		for i := 0; i < 50; i++ {
			msg := []byte(fmt.Sprintf("udp-%03d", i))
			t0 := time.Now()
			uc.Write(msg)
			uc.SetReadDeadline(time.Now().Add(3 * time.Second))
			if n, err := uc.Read(rb); err == nil && string(rb[:n]) == string(msg) {
				got++
				lat += time.Since(t0)
			}
		}
		if got > 0 {
			lat /= time.Duration(got)
		}
		fmt.Println("RESULT: UDP echo", got, "/ 50 ok, avg rtt", lat)
		uc.Close()
	}
	if c2, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", maps[1].LocalTCP)); err == nil {
		c2.SetReadDeadline(time.Now().Add(30 * time.Second))
		b, _ := io.ReadAll(c2)
		fmt.Println("RESULT: second port got", string(b))
		c2.Close()
	}
	port := maps[0].LocalTCP
	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		fmt.Println("dial fail", err)
		return
	}
	payload := make([]byte, 4<<20)
	rand.Read(payload)
	start := time.Now()
	go c.Write(payload)
	got := make([]byte, len(payload))
	c.SetReadDeadline(time.Now().Add(60 * time.Second))
	if _, err := io.ReadFull(c, got); err != nil {
		fmt.Println("RESULT: FAIL read:", err)
		return
	}
	el := time.Since(start)
	fmt.Printf("RESULT: match=%v 4MB roundtrip in %v (%.1f MB/s)\n", bytes.Equal(got, payload), el, 4/el.Seconds())
	// small-message latency
	b := []byte("ping")
	var tot time.Duration
	for i := 0; i < 20; i++ {
		t0 := time.Now()
		c.Write(b)
		io.ReadFull(c, make([]byte, 4))
		tot += time.Since(t0)
	}
	fmt.Println("RESULT: avg small roundtrip", tot/20)
	c.Close()
	time.Sleep(500 * time.Millisecond)
}
