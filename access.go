package main

import (
	"fmt"
	"sort"
	"time"
)

// ---------- access control ----------

const (
	accessFriends    = "friends"     // only Steam friends, others rejected
	accessFriendsAsk = "friends_ask" // friends automatically, ask for others
	accessAsk        = "ask"         // ask for everyone
	accessAll        = "all"         // anyone who knows the SteamID

	accDeny  = -1
	accAsk   = 0
	accAllow = 1
)

var (
	accessMode   = accessFriendsAsk
	selfID       uint64
	allowAlways  = map[uint64]bool{} // persisted by the UI
	sessionAllow = map[uint64]bool{}
	sessionDeny  = map[uint64]bool{}
	asking       = map[uint64]bool{}

	// askFn is called (with mu held; must not block) when someone needs approval.
	askFn = func(remote uint64, name string, friend bool) {}
)

// must hold mu
func accessFor(remote uint64) int {
	if remote == selfID {
		return accAllow
	}
	if sessionDeny[remote] {
		return accDeny
	}
	if allowAlways[remote] || sessionAllow[remote] {
		return accAllow
	}
	friend := isFriend(remote)
	switch accessMode {
	case accessAll:
		return accAllow
	case accessFriends:
		if friend {
			return accAllow
		}
		return accDeny
	case accessFriendsAsk:
		if friend {
			return accAllow
		}
	}
	return accAsk
}

// must hold mu
func askAccess(remote uint64) {
	if asking[remote] {
		return
	}
	asking[remote] = true
	requestUserInfo(remote)
	name := displayName(remote)
	logf("%s baglanmak istiyor, onayin bekleniyor", name)
	notifyFn("Bağlantı isteği", name+" paylaşımına bağlanmak istiyor")
	askFn(remote, name, isFriend(remote))
}

// decideAccess applies the user's answer and resumes the waiting requests.
func decideAccess(remote uint64, allow, always bool) {
	mu.Lock()
	defer mu.Unlock()
	delete(asking, remote)
	if allow {
		sessionAllow[remote] = true
		delete(sessionDeny, remote)
		if always {
			allowAlways[remote] = true
		}
		logf("%s icin izin verildi", displayName(remote))
	} else {
		sessionDeny[remote] = true
		delete(sessionAllow, remote)
		logf("%s reddedildi", displayName(remote))
	}
	for _, c := range ctrlConns {
		if c.remote == remote && len(c.pending) > 0 {
			p := c.pending
			c.pending = nil
			for _, m := range p {
				hostCtlOp(c, m)
			}
		}
	}
}

func setAccessMode(m string) {
	mu.Lock()
	accessMode = m
	mu.Unlock()
}

func setAllowAlways(ids []uint64) {
	mu.Lock()
	allowAlways = map[uint64]bool{}
	for _, id := range ids {
		allowAlways[id] = true
	}
	mu.Unlock()
}

func allowAlwaysList() []uint64 {
	mu.Lock()
	defer mu.Unlock()
	var out []uint64
	for id := range allowAlways {
		out = append(out, id)
	}
	sort.Slice(out, func(a, b int) bool { return out[a] < out[b] })
	return out
}

// blockPeer denies a user for this session, forgets any permanent permission
// and drops all of their connections.
func blockPeer(remote uint64) {
	mu.Lock()
	defer mu.Unlock()
	sessionDeny[remote] = true
	delete(sessionAllow, remote)
	delete(allowAlways, remote)
	for h, t := range tunnels {
		if t.remote == remote && !t.outgoing {
			closeReqs = append(closeReqs, h)
		}
	}
	for h, c := range ctrlConns {
		if c.remote == remote {
			closeReqs = append(closeReqs, h)
		}
	}
	logf("%s engellendi", displayName(remote))
}

// ---------- peers (for notifications) ----------

type peerKey struct {
	id  uint64
	out bool // we connected to them (client side)
}

var (
	peerCount = map[peerKey]int{}
	peerShown = map[peerKey]bool{}
	// notifyFn shows a desktop notification (called with mu held; must not block).
	notifyFn = func(title, body string) {}
)

// must hold mu
func peerUp(k peerKey) {
	peerCount[k]++
	if peerCount[k] != 1 || peerShown[k] {
		return
	}
	peerShown[k] = true
	if k.out {
		notifyFn("Bağlantı kuruldu", displayName(k.id)+" ile bağlantı kuruldu")
	} else {
		notifyFn("Arkadaşın bağlandı", displayName(k.id)+" paylaşımına bağlandı")
	}
}

// must hold mu. "Left" is only announced after 5 s without any connection, so
// games that reconnect quickly don't spam notifications.
func peerDown(k peerKey) {
	peerCount[k]--
	if peerCount[k] > 0 {
		return
	}
	delete(peerCount, k)
	time.AfterFunc(5*time.Second, func() {
		mu.Lock()
		defer mu.Unlock()
		if peerCount[k] > 0 || !peerShown[k] {
			return
		}
		delete(peerShown, k)
		if k.out {
			notifyFn("Bağlantı koptu", displayName(k.id)+" ile bağlantı kapandı")
		} else {
			notifyFn("Arkadaşın ayrıldı", displayName(k.id)+" bağlantıyı kapattı")
		}
	})
}

// ---------- connection panel ----------

type connRow struct {
	H          uint32
	Remote     uint64
	Name       string
	Proto      string
	Port       int
	Outgoing   bool
	Since      time.Time
	Sent, Recv int64
	Ping       int32
	Route      string
}

func snapshotConns() []connRow {
	mu.Lock()
	defer mu.Unlock()
	var rows []connRow
	add := func(h uint32, remote uint64, proto string, port int, out bool, since time.Time, sent, recv int64) {
		r := connRow{H: h, Remote: remote, Name: displayName(remote), Proto: proto, Port: port,
			Outgoing: out, Since: since, Sent: sent, Recv: recv, Ping: -1}
		if rt, ok := getRT(h); ok {
			r.Ping = rt.ping
		}
		if ci, ok := getInfo(h); ok {
			r.Route = routeName(ci)
		}
		rows = append(rows, r)
	}
	for _, t := range tunnels {
		if t.isUp {
			add(t.h, t.remote, "TCP", t.port, t.outgoing, t.since, t.sent.Load(), t.recv.Load())
		}
	}
	for _, c := range ctrlConns {
		if c.udp != nil {
			add(c.h, c.remote, "UDP", c.port, false, c.since, c.sent.Load(), c.recv.Load())
		}
	}
	for _, s := range udpSessions {
		if s.ready {
			add(s.h, s.ul.target, "UDP", s.ul.port, true, s.since, s.sent.Load(), s.recv.Load())
		}
	}
	sort.Slice(rows, func(a, b int) bool { return rows[a].Since.Before(rows[b].Since) })
	return rows
}

func kickConn(h uint32) { requestClose(h) }

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
