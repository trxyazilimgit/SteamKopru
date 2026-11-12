package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// Protocol (Steam P2P virtual ports, which Steam limits to 0..65535):
//   vport N > 0 -> raw TCP tunnel to the host's 127.0.0.1:N (reliable stream).
//   vport 0     -> control + UDP. Every message starts with a tag byte:
//                  tagCtl + JSON, or tagData + one UDP datagram.
//                  The client's first message is {"op":"list"} (host replies with
//                  the share list) or {"op":"udp","port":N} (host replies
//                  {"ok":true} or {"err":...}; then datagrams flow both ways,
//                  sent unreliable so a lost packet never stalls the game).

const (
	tagCtl  = 0
	tagData = 1

	udpIdle  = 3 * time.Minute // UDP has no "close": drop sessions after this much silence
	ctrlIdle = 2 * time.Minute // control connection that never asked for anything

	protoTCP  = "tcp"
	protoUDP  = "udp"
	protoBoth = "both"
)

// share = one local port the host exposes.
type share struct {
	Port  int    `json:"port"`
	Name  string `json:"name"`
	Proto string `json:"proto,omitempty"` // "tcp" (default), "udp", "both"
}

func (s share) hasTCP() bool { return s.Proto != protoUDP }
func (s share) hasUDP() bool { return s.Proto == protoUDP || s.Proto == protoBoth }

func (s share) protoLabel() string {
	switch s.Proto {
	case protoUDP:
		return "UDP"
	case protoBoth:
		return "TCP+UDP"
	}
	return "TCP"
}

func (s share) label() string {
	p := fmt.Sprintf("%d/%s", s.Port, s.protoLabel())
	if s.Name != "" {
		return s.Name + " (" + p + ")"
	}
	return p
}

// mapping = a shared port and the local port(s) the client opened for it.
type mapping struct {
	share
	LocalTCP, LocalUDP int
}

type ctlMsg struct {
	Op     string  `json:"op,omitempty"`
	Port   int     `json:"port,omitempty"`
	OK     bool    `json:"ok,omitempty"`
	Err    string  `json:"err,omitempty"`
	V      int     `json:"v,omitempty"`
	Shares []share `json:"shares,omitempty"`
}

func ctlBytes(m ctlMsg) []byte {
	b, _ := json.Marshal(m)
	return append([]byte{tagCtl}, b...)
}

// tunnel = one Steam P2P connection <-> one TCP connection.
type tunnel struct {
	h          uint32
	listen     uint32 // host side: listen socket it came from
	remote     uint64
	port       int
	outgoing   bool
	since      time.Time
	tcp        net.Conn
	out        chan []byte // steam -> tcp
	connected  chan struct{}
	isUp       bool
	counted    bool
	sent, recv atomic.Int64
}

// query = client-side control connection waiting for the host's share list.
type query struct {
	ch     chan []byte
	up     chan struct{} // closed once the Steam connection is established
	isUp   bool
	got    bool
	reason string
}

// ctrlConn = host-side vport 0 connection (a list query or a UDP session).
type ctrlConn struct {
	h          uint32
	remote     uint64
	udp        *net.UDPConn // set once it became a UDP session
	port       int
	since      time.Time
	pending    []ctlMsg // requests waiting for the user's approval
	counted    bool
	last       atomic.Int64
	sent, recv atomic.Int64
}

// udpLn = client-side local UDP socket for one shared port.
type udpLn struct {
	ln        *net.UDPConn
	target    uint64
	port      int // host's port
	sess      map[string]uint32
	failUntil map[string]time.Time
}

// udpSess = client-side UDP session (one per local game socket).
type udpSess struct {
	h          uint32
	ul         *udpLn
	addr       *net.UDPAddr
	key        string
	up         bool // Steam connection established
	ready      bool // host acknowledged, datagrams may flow
	counted    bool
	since      time.Time
	pending    [][]byte
	last       atomic.Int64
	sent, recv atomic.Int64
}

var (
	mu          sync.Mutex
	tunnels     = map[uint32]*tunnel{}
	queries     = map[uint32]*query{}
	ctrlConns   = map[uint32]*ctrlConn{}
	udpSessions = map[uint32]*udpSess{}
	events      []uint32 // handles whose status changed (filled by Steam callback)
	closeReqs   []uint32 // connections someone wants closed (closed on the poll thread)

	hostShares  []share
	hostListens = map[uint32]int{} // TCP listen socket -> local TCP port
	ctrlListen  uint32
	clientLns   []net.Listener
	clientUDP   []*udpLn
	lastStat    time.Time

	stopPoll = make(chan struct{})
	pollDone = make(chan struct{})
	logSink  = func(string) {}
	statusFn = func(ping int32, route string) {}
)

func logf(f string, a ...any) {
	logSink("[" + time.Now().Format("15:04:05") + "] " + fmt.Sprintf(f, a...))
}

func nowNano() int64 { return time.Now().UnixNano() }

func sinceNano(t int64) time.Duration { return time.Duration(nowNano() - t) }

// startCore initialises Steam and the networking poll thread.
func startCore() error {
	if err := steamInit(); err != nil {
		return err
	}
	selfID = mySteamID()
	timeBeginPeriod1()
	setGlobalInt(cfgSendBufferSize, 16<<20)
	setGlobalInt(cfgSendRateMin, 512*1024)
	setGlobalInt(cfgSendRateMax, 32<<20)
	setGlobalInt(cfgTimeoutInitial, 20000)
	setGlobalInt(cfgP2PTransportICE, iceEnableAll)

	cb := syscall.NewCallback(func(ptr uintptr) uintptr {
		h := *(*uint32)(unsafe.Pointer(ptr)) // m_hConn is the first field
		events = append(events, h)           // runs on the poll thread, inside runCallbacks
		return 0
	})
	if !setConnStatusCallback(cb) {
		return fmt.Errorf("Steam baglanti geri cagrisi ayarlanamadi")
	}
	initRelay()
	ready := make(chan struct{})
	go pollLoop(ready)
	<-ready
	go waitRelay()
	return nil
}

func waitRelay() {
	for i := 0; i < 150; i++ {
		if relayStatus() == 100 {
			logf("Steam ag rolesi hazir")
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	logf("Uyari: Steam rolesi henuz hazir degil, yine de denenecek")
}

// readErrFatal: on Windows a UDP socket reports ICMP "port unreachable" as a
// read error; only a closed socket should stop the reader.
func readErrFatal(err error) bool {
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	time.Sleep(5 * time.Millisecond)
	return false
}

// ---------- host ----------

// startHost exposes (or updates) the given local ports to Steam friends.
func startHost(shares []share) error {
	if len(shares) == 0 {
		return fmt.Errorf("en az bir port girmelisin")
	}
	for _, s := range shares {
		if !s.hasTCP() {
			continue
		}
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", s.Port), time.Second); err != nil {
			logf("Uyari: TCP 127.0.0.1:%d su an acik degil (%s). Oyun/sunucu calisiyor mu?", s.Port, s.label())
		} else {
			c.Close()
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if ctrlListen == 0 {
		ctrlListen = createListenP2P(0)
		if ctrlListen == 0 {
			return fmt.Errorf("Steam P2P dinleme baslatilamadi")
		}
	}
	wantTCP := map[int]bool{}
	wantUDP := map[int]bool{}
	for _, s := range shares {
		wantTCP[s.Port] = s.hasTCP()
		wantUDP[s.Port] = s.hasUDP()
	}
	have := map[int]bool{}
	for lh, port := range hostListens {
		if wantTCP[port] {
			have[port] = true
			continue
		}
		closeHostListen(lh)
		logf("TCP paylasimi kapatildi: port %d", port)
	}
	for _, s := range shares {
		if !s.hasTCP() || have[s.Port] {
			continue
		}
		lh := createListenP2P(s.Port)
		if lh == 0 {
			return fmt.Errorf("port %d icin Steam dinleme baslatilamadi", s.Port)
		}
		hostListens[lh] = s.Port
		have[s.Port] = true
	}
	for h, c := range ctrlConns {
		if c.udp != nil && !wantUDP[c.port] {
			closeReqs = append(closeReqs, h)
		}
	}
	hostShares = append([]share(nil), shares...)
	for _, s := range shares {
		logf("Paylasiliyor: %s", s.label())
	}
	logf("HAZIR. Arkadasin SteamKopru'da \"Arkadasa baglan\" sekmesinde seni (%s) secsin.", myName())
	logf("Listede cikmazsa su SteamID'yi girsin: %d", mySteamID())
	return nil
}

// stopHost closes every shared port and the control socket.
func stopHost() {
	mu.Lock()
	defer mu.Unlock()
	for lh := range hostListens {
		closeHostListen(lh)
	}
	if ctrlListen != 0 {
		closeListen(ctrlListen)
		ctrlListen = 0
	}
	for h := range ctrlConns {
		closeReqs = append(closeReqs, h)
	}
	hostShares = nil
	logf("Paylasim durduruldu")
}

// must hold mu. Tunnels are only queued for closing: t.out must be closed by
// the poll thread alone.
func closeHostListen(lh uint32) {
	for h, t := range tunnels {
		if t.listen == lh {
			closeReqs = append(closeReqs, h)
		}
	}
	closeListen(lh)
	delete(hostListens, lh)
}

func udpShared(port int) bool {
	for _, s := range hostShares {
		if s.Port == port && s.hasUDP() {
			return true
		}
	}
	return false
}

// must hold mu; runs on poll thread
func hostAccept(h uint32, ci connInfo, port int) {
	if r := acceptConn(h); r != eResultOK {
		logf("Gelen baglanti kabul edilemedi (kod %d)", r)
		closeConn(h, false)
		return
	}
	t := newTunnel(h, nil)
	t.listen, t.remote, t.port = ci.listen, ci.remote, port
	logf("%s baglaniyor -> TCP %d (#%d)", friendName(ci.remote), port, h)
	go func() {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
		if err != nil {
			logf("TCP 127.0.0.1:%d portuna baglanilamadi: oyun/sunucu acik mi? %v", port, err)
			requestClose(h)
			return
		}
		setNoDelay(c)
		mu.Lock()
		if tunnels[h] != t {
			mu.Unlock()
			c.Close()
			return
		}
		t.tcp = c
		mu.Unlock()
		go tcpToSteam(t)
		steamToTCP(t)
	}()
}

// must hold mu; runs on poll thread. Handles one message on a host control connection.
func hostCtlMsg(c *ctrlConn, b []byte) {
	if len(b) == 0 {
		return
	}
	if b[0] == tagData {
		if c.udp != nil {
			c.udp.Write(b[1:])
			c.recv.Add(int64(len(b) - 1))
			c.last.Store(nowNano())
		}
		return
	}
	var m ctlMsg
	if b[0] != tagCtl || json.Unmarshal(b[1:], &m) != nil {
		return
	}
	c.last.Store(nowNano())
	hostCtlOp(c, m)
}

// must hold mu. Runs a control request once the remote user is allowed.
func hostCtlOp(c *ctrlConn, m ctlMsg) {
	switch accessFor(c.remote) {
	case accDeny:
		sendMsg(c.h, ctlBytes(ctlMsg{Err: "bağlantı reddedildi"}))
		closeReqs = append(closeReqs, c.h)
		return
	case accAsk:
		c.pending = append(c.pending, m)
		askAccess(c.remote)
		return
	}
	switch m.Op {
	case "list":
		sendMsg(c.h, ctlBytes(ctlMsg{V: 2, Shares: hostShares}))
		logf("%s paylasilan portlari aldi", friendName(c.remote))
	case "udp":
		if c.udp != nil {
			return
		}
		if !udpShared(m.Port) {
			sendMsg(c.h, ctlBytes(ctlMsg{Err: fmt.Sprintf("UDP %d paylasilmiyor", m.Port)}))
			return
		}
		uc, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: m.Port})
		if err != nil {
			sendMsg(c.h, ctlBytes(ctlMsg{Err: err.Error()}))
			return
		}
		c.udp, c.port, c.since = uc, m.Port, time.Now()
		c.last.Store(nowNano())
		c.counted = true
		peerUp(peerKey{c.remote, false})
		sendMsg(c.h, ctlBytes(ctlMsg{OK: true}))
		ci, _ := getInfo(c.h)
		rt, _ := getRT(c.h)
		logf("%s baglandi -> UDP %d (#%d) - %s, ping %d ms", friendName(c.remote), m.Port, c.h, routeName(ci), rt.ping)
		statusFn(rt.ping, routeName(ci))
		go hostUDPReader(c, uc)
	}
}

// host: game -> Steam
func hostUDPReader(c *ctrlConn, uc *net.UDPConn) {
	buf := make([]byte, 1+65535)
	buf[0] = tagData
	for {
		n, err := uc.Read(buf[1:])
		if err != nil {
			if readErrFatal(err) {
				return
			}
			continue
		}
		c.last.Store(nowNano())
		c.sent.Add(int64(n))
		sendMsgFlags(c.h, buf[:n+1], sendUnreliableNoNagle)
	}
}

// ---------- client ----------

// queryHost asks the friend which ports they share.
func queryHost(target uint64) ([]share, error) {
	mu.Lock()
	h := connectP2P(target, 0)
	if h == 0 {
		mu.Unlock()
		return nil, fmt.Errorf("Steam baglantisi baslatilamadi")
	}
	q := &query{ch: make(chan []byte, 1), up: make(chan struct{})}
	queries[h] = q
	mu.Unlock()
	logf("Arkadasin paylastigi portlar soruluyor...")

	var b []byte
	var ok bool
	up := q.up
	deadline := time.After(25 * time.Second)
wait:
	for {
		select {
		case b, ok = <-q.ch:
			break wait
		case <-up:
			up = nil
			deadline = time.After(90 * time.Second) // connected: the host may be asking its user
		case <-deadline:
			requestClose(h)
			if up == nil {
				return nil, fmt.Errorf("arkadaşın bağlantı isteğine cevap vermedi")
			}
			return nil, fmt.Errorf("arkadaşına ulaşılamadı (zaman aşımı).\nArkadaşının SteamKöprü'yü güncel sürümle açıp \"Paylaşımı başlat\"a bastığından emin ol")
		}
	}
	if !ok {
		r := q.reason
		if r == "" {
			r = "bağlantı kurulamadı"
		}
		return nil, fmt.Errorf("arkadaşına bağlanılamadı: %s\nArkadaşının SteamKöprü'yü güncel sürümle açıp \"Paylaşımı başlat\"a bastığından emin ol", r)
	}
	var m ctlMsg
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("arkadaşından geçersiz yanıt geldi (farklı sürüm olabilir)")
	}
	if m.Err != "" {
		return nil, fmt.Errorf("arkadaşın bağlantıyı kabul etmedi: %s", m.Err)
	}
	var out []share
	for _, s := range m.Shares {
		if s.Port > 0 && s.Port <= 65535 {
			if s.Proto != protoUDP && s.Proto != protoBoth {
				s.Proto = protoTCP
			}
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("arkadaşın şu an hiçbir port paylaşmıyor")
	}
	return out, nil
}

// listenLocal opens 127.0.0.1 on the same port if possible (TCP and/or UDP on
// one number), otherwise a nearby or random free one.
func listenLocal(port int, wantTCP, wantUDP bool) (net.Listener, *net.UDPConn, int, error) {
	try := func(p int) (net.Listener, *net.UDPConn, int, error) {
		var tl net.Listener
		var ul *net.UDPConn
		var err error
		if wantTCP {
			if tl, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)); err != nil {
				return nil, nil, 0, err
			}
			p = tl.Addr().(*net.TCPAddr).Port
		}
		if wantUDP {
			if ul, err = net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: p}); err != nil {
				if tl != nil {
					tl.Close()
				}
				return nil, nil, 0, err
			}
			p = ul.LocalAddr().(*net.UDPAddr).Port
		}
		return tl, ul, p, nil
	}
	var lastErr error
	for i := 0; i <= 10 && port+i <= 65535; i++ {
		tl, ul, p, err := try(port + i)
		if err == nil {
			return tl, ul, p, nil
		}
		lastErr = err
	}
	for i := 0; i < 20; i++ { // random free port
		tl, ul, p, err := try(0)
		if err == nil {
			return tl, ul, p, nil
		}
		lastErr = err
	}
	return nil, nil, 0, lastErr
}

// startClient opens local port(s) per shared port, each tunnelling to the friend.
func startClient(target uint64, shares []share) ([]mapping, error) {
	mu.Lock()
	for _, ln := range clientLns {
		ln.Close()
	}
	for _, ul := range clientUDP {
		ul.ln.Close()
	}
	for h := range udpSessions {
		closeReqs = append(closeReqs, h)
	}
	clientLns, clientUDP = nil, nil
	mu.Unlock()

	var maps []mapping
	var tls []net.Listener
	var uls []*udpLn
	fail := func(err error) ([]mapping, error) {
		for _, l := range tls {
			l.Close()
		}
		for _, u := range uls {
			u.ln.Close()
		}
		return nil, err
	}
	type pend struct {
		tl   net.Listener
		ul   *udpLn
		port int
	}
	var starts []pend
	for _, s := range shares {
		tl, uc, p, err := listenLocal(s.Port, s.hasTCP(), s.hasUDP())
		if err != nil {
			return fail(fmt.Errorf("yerel port açılamadı (%s): %v", s.label(), err))
		}
		m := mapping{share: s}
		var ul *udpLn
		if tl != nil {
			tls = append(tls, tl)
			m.LocalTCP = p
		}
		if uc != nil {
			ul = &udpLn{ln: uc, target: target, port: s.Port, sess: map[string]uint32{}, failUntil: map[string]time.Time{}}
			uls = append(uls, ul)
			m.LocalUDP = p
		}
		maps = append(maps, m)
		starts = append(starts, pend{tl, ul, s.Port})
	}
	mu.Lock()
	clientLns, clientUDP = tls, uls
	mu.Unlock()

	for i, m := range maps {
		if m.LocalTCP != 0 {
			logf("HAZIR: %s -> TCP localhost:%d", m.share.label(), m.LocalTCP)
			go acceptLoop(starts[i].tl, target, starts[i].port)
		}
		if m.LocalUDP != 0 {
			logf("HAZIR: %s -> UDP localhost:%d", m.share.label(), m.LocalUDP)
			go udpListenLoop(starts[i].ul)
		}
	}
	return maps, nil
}

func acceptLoop(ln net.Listener, target uint64, remotePort int) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		setNoDelay(c)
		mu.Lock()
		h := connectP2P(target, remotePort)
		if h == 0 {
			mu.Unlock()
			logf("Steam baglantisi baslatilamadi")
			c.Close()
			continue
		}
		t := newTunnel(h, c)
		t.remote, t.port, t.outgoing = target, remotePort, true
		mu.Unlock()
		logf("Baglaniliyor -> TCP %d (#%d)", remotePort, h)
		go tcpToSteam(t)
		go steamToTCP(t)
	}
}

// client: game -> Steam. Each local source address gets its own Steam session.
func udpListenLoop(ul *udpLn) {
	buf := make([]byte, 1+65535)
	buf[0] = tagData
	for {
		n, addr, err := ul.ln.ReadFromUDP(buf[1:])
		if err != nil {
			if readErrFatal(err) {
				return
			}
			continue
		}
		key := addr.String()
		mu.Lock()
		s := udpSessions[ul.sess[key]]
		if s == nil {
			if time.Now().Before(ul.failUntil[key]) {
				mu.Unlock()
				continue
			}
			h := connectP2P(ul.target, 0)
			if h == 0 {
				mu.Unlock()
				continue
			}
			s = &udpSess{h: h, ul: ul, addr: addr, key: key}
			udpSessions[h] = s
			ul.sess[key] = h
			logf("Baglaniliyor -> UDP %d (#%d)", ul.port, h)
		}
		s.last.Store(nowNano())
		if !s.ready {
			if len(s.pending) < 256 {
				s.pending = append(s.pending, append([]byte(nil), buf[:n+1]...))
			}
			mu.Unlock()
			continue
		}
		h := s.h
		mu.Unlock()
		s.sent.Add(int64(n))
		sendMsgFlags(h, buf[:n+1], sendUnreliableNoNagle)
	}
}

// must hold mu; runs on poll thread. Handles one message on a client UDP session.
func clientUDPMsg(s *udpSess, b []byte) {
	if len(b) == 0 {
		return
	}
	if b[0] == tagData {
		if s.ready {
			s.ul.ln.WriteToUDP(b[1:], s.addr)
			s.recv.Add(int64(len(b) - 1))
			s.last.Store(nowNano())
		}
		return
	}
	var m ctlMsg
	if s.ready || b[0] != tagCtl || json.Unmarshal(b[1:], &m) != nil {
		return
	}
	if !m.OK {
		logf("UDP %d acilamadi: %s", s.ul.port, m.Err)
		s.ul.failUntil[s.key] = time.Now().Add(10 * time.Second)
		closeReqs = append(closeReqs, s.h)
		return
	}
	s.ready = true
	s.since = time.Now()
	s.counted = true
	peerUp(peerKey{s.ul.target, true})
	for _, p := range s.pending {
		s.sent.Add(int64(len(p) - 1))
		sendMsgFlags(s.h, p, sendUnreliableNoNagle)
	}
	s.pending = nil
	ci, _ := getInfo(s.h)
	rt, _ := getRT(s.h)
	logf("BAGLANDI UDP %d (#%d) - %s, ping %d ms", s.ul.port, s.h, routeName(ci), rt.ping)
	statusFn(rt.ping, routeName(ci))
}

// ---------- tunnel plumbing ----------

func setNoDelay(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
}

// must hold mu
func newTunnel(h uint32, c net.Conn) *tunnel {
	t := &tunnel{h: h, tcp: c, out: make(chan []byte, 8192), connected: make(chan struct{})}
	tunnels[h] = t
	return t
}

func requestClose(h uint32) {
	mu.Lock()
	closeReqs = append(closeReqs, h)
	mu.Unlock()
}

// must hold mu
func anyActive() bool {
	if len(tunnels) > 0 || len(udpSessions) > 0 {
		return true
	}
	for _, c := range ctrlConns {
		if c.udp != nil {
			return true
		}
	}
	return false
}

// must hold mu; runs on poll thread. Closes a tunnel, query, control connection or UDP session.
func closeTunnel(h uint32, reason string) {
	defer func() {
		if !anyActive() {
			statusFn(-1, "")
		}
	}()
	if q := queries[h]; q != nil {
		delete(queries, h)
		closeConn(h, false)
		if !q.got {
			q.reason = reason
			close(q.ch)
		}
		return
	}
	if c := ctrlConns[h]; c != nil {
		delete(ctrlConns, h)
		closeConn(h, true) // linger: a final error reply should still arrive
		if c.counted {
			peerDown(peerKey{c.remote, false})
		}
		if c.udp != nil {
			c.udp.Close()
			logf("UDP oturumu kapandi (#%d) %s", h, reason)
		}
		return
	}
	if s := udpSessions[h]; s != nil {
		delete(udpSessions, h)
		if s.counted {
			peerDown(peerKey{s.ul.target, true})
		}
		if s.ul.sess[s.key] == h {
			delete(s.ul.sess, s.key)
		}
		closeConn(h, false)
		if s.ready {
			logf("UDP oturumu kapandi (#%d) %s", h, reason)
		} else {
			logf("UDP baglantisi kurulamadi (#%d) %s", h, reason)
			if s.ul.failUntil[s.key].Before(time.Now()) {
				s.ul.failUntil[s.key] = time.Now().Add(5 * time.Second)
			}
		}
		return
	}
	t := tunnels[h]
	if t == nil {
		closeConn(h, false)
		return
	}
	delete(tunnels, h)
	if t.counted {
		peerDown(peerKey{t.remote, t.outgoing})
	}
	closeConn(h, true) // linger: deliver what's already queued
	close(t.out)
	if t.tcp != nil {
		t.tcp.Close()
	}
	if t.isUp {
		logf("Baglanti kapandi (#%d) %s", h, reason)
	} else {
		logf("Baglanti kurulamadi (#%d) %s", h, reason)
	}
}

func tcpToSteam(t *tunnel) {
	select {
	case <-t.connected:
	case <-time.After(30 * time.Second):
		requestClose(t.h)
		return
	}
	buf := make([]byte, 64*1024)
	for {
		n, err := t.tcp.Read(buf)
		if n > 0 {
			for {
				r := sendMsg(t.h, buf[:n])
				if r == eResultOK {
					t.sent.Add(int64(n))
					break
				}
				if r == eResultLimitExceeded {
					time.Sleep(2 * time.Millisecond)
					continue
				}
				requestClose(t.h)
				return
			}
		}
		if err != nil {
			requestClose(t.h)
			return
		}
	}
}

func steamToTCP(t *tunnel) {
	for b := range t.out {
		if _, err := t.tcp.Write(b); err != nil {
			go func() {
				for range t.out {
				}
			}()
			requestClose(t.h)
			return
		}
	}
}

// must hold mu; pulls the share list off a query connection if it has arrived.
func pollQuery(h uint32, q *query) {
	n := recvMsgs(h, 4, func(b []byte) {
		if q.got || len(b) == 0 {
			return
		}
		switch b[0] {
		case tagCtl:
			b = b[1:]
		case '{': // untagged reply from the previous SteamKopru build
		default:
			return
		}
		q.got = true
		q.ch <- b
	})
	if q.got {
		closeTunnel(h, "")
	} else if n < 0 {
		closeTunnel(h, "gecersiz baglanti")
	}
}

func routeName(ci connInfo) string {
	if pop := popName(ci.popRelay); pop != "" {
		return "Steam rolesi: " + pop
	}
	return "dogrudan P2P"
}

// shutdownCore stops the poll thread, closes all tunnels and shuts Steam down.
func shutdownCore() {
	select {
	case <-stopPoll:
		return
	default:
	}
	close(stopPoll)
	select {
	case <-pollDone:
	case <-time.After(2 * time.Second):
	}
	steamShutdown()
}

// must hold mu; one handle carrying live traffic, for the ping display.
func anyLiveHandle() uint32 {
	for h, t := range tunnels {
		if t.isUp {
			return h
		}
	}
	for h, s := range udpSessions {
		if s.ready {
			return h
		}
	}
	for h, c := range ctrlConns {
		if c.udp != nil {
			return h
		}
	}
	return 0
}

func pollLoop(ready chan struct{}) {
	runtime.LockOSThread()
	defer close(pollDone)
	close(ready)
	for {
		select {
		case <-stopPoll:
			mu.Lock()
			for h := range tunnels {
				closeTunnel(h, "")
			}
			for h := range queries {
				closeTunnel(h, "")
			}
			for h := range ctrlConns {
				closeTunnel(h, "")
			}
			for h := range udpSessions {
				closeTunnel(h, "")
			}
			for lh := range hostListens {
				closeListen(lh)
			}
			if ctrlListen != 0 {
				closeListen(ctrlListen)
			}
			for _, ln := range clientLns {
				ln.Close()
			}
			for _, ul := range clientUDP {
				ul.ln.Close()
			}
			mu.Unlock()
			runCallbacks()
			time.Sleep(200 * time.Millisecond) // let lingering closes flush
			return
		default:
		}
		runCallbacks()

		mu.Lock()
		evs := events
		events = nil
		for _, h := range evs {
			ci, ok := getInfo(h)
			if !ok {
				closeTunnel(h, "")
				continue
			}
			t := tunnels[h]
			switch ci.state {
			case stateConnecting:
				if t != nil || ci.listen == 0 || queries[h] != nil || ctrlConns[h] != nil || udpSessions[h] != nil {
					break // outgoing connection, or already handled
				}
				acc := accessFor(ci.remote)
				if acc == accDeny {
					closeConn(h, false)
					logf("%s reddedildi (erisim ayarlari)", displayName(ci.remote))
				} else if ci.listen == ctrlListen {
					// accepted even when approval is pending: the request
					// is held until the user answers (see hostCtlOp)
					if acceptConn(h) == eResultOK {
						c := &ctrlConn{h: h, remote: ci.remote}
						c.last.Store(nowNano())
						ctrlConns[h] = c
					} else {
						closeConn(h, false)
					}
				} else if port, ok := hostListens[ci.listen]; ok {
					if acc == accAllow {
						hostAccept(h, ci, port)
					} else {
						closeConn(h, false)
						logf("%s once \"Baglan\" ile izin istemeli", displayName(ci.remote))
					}
				} else {
					closeConn(h, false)
				}
			case stateConnected:
				if q := queries[h]; q != nil {
					if !q.isUp {
						q.isUp = true
						close(q.up)
					}
					sendMsg(h, ctlBytes(ctlMsg{Op: "list"}))
				} else if s := udpSessions[h]; s != nil && !s.up {
					s.up = true
					sendMsg(h, ctlBytes(ctlMsg{Op: "udp", Port: s.ul.port}))
				} else if t != nil && !t.isUp {
					t.isUp = true
					t.since = time.Now()
					t.counted = true
					peerUp(peerKey{t.remote, t.outgoing})
					close(t.connected)
					rt, _ := getRT(h)
					logf("BAGLANDI TCP (#%d) - %s, ping %d ms", h, routeName(ci), rt.ping)
					statusFn(rt.ping, routeName(ci))
				}
			case stateClosedByPeer, stateProblemLocal:
				if q := queries[h]; q != nil {
					pollQuery(h, q) // the list may have arrived just before the close
				}
				closeTunnel(h, ci.endDebug)
			}
		}
		for _, h := range closeReqs {
			closeTunnel(h, "")
		}
		closeReqs = nil

		// control / UDP traffic (small and non-blocking, handled under mu)
		for h, q := range queries {
			pollQuery(h, q)
		}
		for h, c := range ctrlConns {
			if recvMsgs(h, 256, func(b []byte) { hostCtlMsg(c, b) }) < 0 {
				closeReqs = append(closeReqs, h)
				continue
			}
			idle := ctrlIdle
			if c.udp != nil {
				idle = udpIdle
			}
			if sinceNano(c.last.Load()) > idle {
				closeReqs = append(closeReqs, h)
			}
		}
		for h, s := range udpSessions {
			if s.up && recvMsgs(h, 256, func(b []byte) { clientUDPMsg(s, b) }) < 0 {
				closeReqs = append(closeReqs, h)
				continue
			}
			if s.ready && sinceNano(s.last.Load()) > udpIdle {
				closeReqs = append(closeReqs, h)
			}
		}

		var live []*tunnel
		for _, t := range tunnels {
			if t.isUp {
				live = append(live, t)
			}
		}
		mu.Unlock()

		// Pushing to t.out may block on a slow TCP writer, so do it without mu.
		// Only this goroutine closes t.out, so there is no send-on-closed race.
		var dead []uint32
		for _, t := range live {
			h := t.h
			if recvMsgs(h, 256, func(b []byte) { t.recv.Add(int64(len(b))); t.out <- b }) < 0 {
				dead = append(dead, h)
			}
		}

		mu.Lock()
		for _, h := range dead {
			closeTunnel(h, "")
		}
		if time.Since(lastStat) > 5*time.Second {
			lastStat = time.Now()
			if h := anyLiveHandle(); h != 0 {
				if rt, ok := getRT(h); ok {
					ci, _ := getInfo(h)
					statusFn(rt.ping, routeName(ci))
				}
			}
		}
		mu.Unlock()

		time.Sleep(time.Millisecond)
	}
}
