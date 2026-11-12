package main

// Minimal Windows binding to steam_api64.dll's flat C API (no cgo).

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

const (
	appID = 480 // Valve's public test app (Spacewar); every Steam user can init it

	stateConnecting   = 1
	stateFindingRoute = 2
	stateConnected    = 3
	stateClosedByPeer = 4
	stateProblemLocal = 5

	eResultOK            = 1
	eResultLimitExceeded = 25

	sendReliableNoNagle   = 8 | 1
	sendUnreliableNoNagle = 1

	cfgTimeoutInitial  = 24
	cfgSendBufferSize  = 9
	cfgSendRateMin     = 10
	cfgSendRateMax     = 11
	cfgP2PTransportICE = 104
	iceEnableAll       = 0x7fffffff

	friendFlagImmediate = 4

	// SteamNetConnectionInfo_t offsets (identity is 136 bytes, pack 1; outer pack 8)
	infoSize        = 1024
	offInfoSteamID  = 8
	offInfoListen   = 144
	offInfoPOPRelay = 172
	offInfoState    = 176
	offInfoEndDebug = 184

	// SteamNetConnectionRealTimeStatus_t
	offRTPing           = 4
	offRTQualityLocal   = 8
	offRTPendingRelia   = 40
	offRTSentUnackRelia = 44

	// SteamNetworkingMessage_t
	offMsgData = 0
	offMsgSize = 8
)

var (
	dll   *syscall.LazyDLL
	procs = map[string]*syscall.LazyProc{}

	ifSockets, ifUtils, ifUser, ifFriends uintptr
)

func p(name string) *syscall.LazyProc {
	if pr, ok := procs[name]; ok {
		return pr
	}
	pr := dll.NewProc(name)
	procs[name] = pr
	return pr
}

func call(name string, args ...uintptr) uintptr {
	r, _, _ := p(name).Call(args...)
	return r
}

func steamInit() error {
	exe, _ := os.Executable()
	dir := filepath.Dir(exe)
	dllPath := filepath.Join(dir, "steam_api64.dll")
	if _, err := os.Stat(dllPath); err != nil {
		return fmt.Errorf("steam_api64.dll bulunamadi (%s). Programla ayni klasorde olmali", dllPath)
	}
	// Steam reads the app id from steam_appid.txt in the working directory.
	_ = os.Chdir(dir)
	_ = os.WriteFile(filepath.Join(dir, "steam_appid.txt"), []byte(fmt.Sprint(appID)), 0644)
	os.Setenv("SteamAppId", fmt.Sprint(appID))
	os.Setenv("SteamGameId", fmt.Sprint(appID))

	dll = syscall.NewLazyDLL(dllPath)
	if err := dll.Load(); err != nil {
		return fmt.Errorf("steam_api64.dll yuklenemedi: %v", err)
	}
	var errMsg [1024]byte
	if r := call("SteamAPI_InitFlat", uintptr(unsafe.Pointer(&errMsg[0]))); r&0xFFFFFFFF != 0 {
		return fmt.Errorf("Steam baslatilamadi (kod %d): %s\nSteam acik ve giris yapilmis olmali", r&0xFFFFFFFF, cstr(uintptr(unsafe.Pointer(&errMsg[0]))))
	}
	ifSockets = call("SteamAPI_SteamNetworkingSockets_SteamAPI_v012")
	ifUtils = call("SteamAPI_SteamNetworkingUtils_SteamAPI_v004")
	ifUser = call("SteamAPI_SteamUser_v023")
	ifFriends = call("SteamAPI_SteamFriends_v018")
	if ifSockets == 0 || ifUtils == 0 || ifUser == 0 || ifFriends == 0 {
		return errors.New("Steam arayuzleri alinamadi (DLL surumu uyusmuyor olabilir)")
	}
	return nil
}

func steamShutdown() { call("SteamAPI_Shutdown") }

func runCallbacks() {
	call("SteamAPI_RunCallbacks")
	call("SteamAPI_ISteamNetworkingSockets_RunCallbacks", ifSockets)
}

func cstr(ptr uintptr) string {
	if ptr == 0 {
		return ""
	}
	var b []byte
	for i := uintptr(0); i < 4096; i++ {
		c := *(*byte)(unsafe.Pointer(ptr + i))
		if c == 0 {
			break
		}
		b = append(b, c)
	}
	return string(b)
}

func setGlobalInt(cfg, val int32) bool {
	return call("SteamAPI_ISteamNetworkingUtils_SetGlobalConfigValueInt32", ifUtils, uintptr(cfg), uintptr(val))&0xFF != 0
}

func initRelay() { call("SteamAPI_ISteamNetworkingUtils_InitRelayNetworkAccess", ifUtils) }

// relayStatus returns ESteamNetworkingAvailability (100 = current/ready).
func relayStatus() int32 {
	var buf [512]byte
	return int32(call("SteamAPI_ISteamNetworkingUtils_GetRelayNetworkStatus", ifUtils, uintptr(unsafe.Pointer(&buf[0]))))
}

func setConnStatusCallback(fn uintptr) bool {
	return call("SteamAPI_ISteamNetworkingUtils_SetGlobalCallback_SteamNetConnectionStatusChanged", ifUtils, fn)&0xFF != 0
}

func mySteamID() uint64 { return uint64(call("SteamAPI_ISteamUser_GetSteamID", ifUser)) }
func myName() string    { return cstr(call("SteamAPI_ISteamFriends_GetPersonaName", ifFriends)) }

func isFriend(id uint64) bool {
	return call("SteamAPI_ISteamFriends_HasFriend", ifFriends, uintptr(id), friendFlagImmediate)&0xFF != 0
}

// requestUserInfo asks Steam to fetch a non-friend's name (arrives asynchronously).
func requestUserInfo(id uint64) {
	call("SteamAPI_ISteamFriends_RequestUserInformation", ifFriends, uintptr(id), 1)
}

// displayName returns the persona name, or the SteamID if Steam doesn't know it yet.
func displayName(id uint64) string {
	if n := friendName(id); n != "" && n != "[unknown]" {
		return n
	}
	return fmt.Sprintf("SteamID %d", id)
}

func friendName(id uint64) string {
	return cstr(call("SteamAPI_ISteamFriends_GetFriendPersonaName", ifFriends, uintptr(id)))
}

type friend struct {
	id    uint64
	name  string
	state int32 // EPersonaState: 0 offline, 1 online, 2 busy, 3 away, 4 snooze ...
	inApp bool  // currently running SteamKopru (app 480)
}

// allFriends lists every Steam friend; those running SteamKopru first, then
// online ones, then offline (invisible friends also appear offline).
func allFriends() []friend {
	var out []friend
	n := int(int32(call("SteamAPI_ISteamFriends_GetFriendCount", ifFriends, friendFlagImmediate)))
	for i := 0; i < n; i++ {
		id := uint64(call("SteamAPI_ISteamFriends_GetFriendByIndex", ifFriends, uintptr(i), friendFlagImmediate))
		f := friend{
			id:    id,
			name:  friendName(id),
			state: int32(call("SteamAPI_ISteamFriends_GetFriendPersonaState", ifFriends, uintptr(id))),
		}
		var gi [64]byte
		if call("SteamAPI_ISteamFriends_GetFriendGamePlayed", ifFriends, uintptr(id), uintptr(unsafe.Pointer(&gi[0])))&0xFF != 0 {
			gameID := *(*uint64)(unsafe.Pointer(&gi[0]))
			f.inApp = gameID&0xFFFFFF == appID
		}
		out = append(out, f)
	}
	rank := func(f friend) int {
		switch {
		case f.inApp:
			return 0
		case f.state != 0:
			return 1
		}
		return 2
	}
	sort.SliceStable(out, func(a, b int) bool {
		ra, rb := rank(out[a]), rank(out[b])
		if ra != rb {
			return ra < rb
		}
		return strings.ToLower(out[a].name) < strings.ToLower(out[b].name)
	})
	return out
}

func createListenP2P(vport int) uint32 {
	return uint32(call("SteamAPI_ISteamNetworkingSockets_CreateListenSocketP2P", ifSockets, uintptr(vport), 0, 0))
}

func closeListen(h uint32) {
	call("SteamAPI_ISteamNetworkingSockets_CloseListenSocket", ifSockets, uintptr(h))
}

func connectP2P(steamID uint64, vport int) uint32 {
	var ident [136]byte
	call("SteamAPI_SteamNetworkingIdentity_SetSteamID64", uintptr(unsafe.Pointer(&ident[0])), uintptr(steamID))
	return uint32(call("SteamAPI_ISteamNetworkingSockets_ConnectP2P", ifSockets, uintptr(unsafe.Pointer(&ident[0])), uintptr(vport), 0, 0))
}

func acceptConn(h uint32) int32 {
	return int32(call("SteamAPI_ISteamNetworkingSockets_AcceptConnection", ifSockets, uintptr(h)))
}

func closeConn(h uint32, linger bool) {
	l := uintptr(0)
	if linger {
		l = 1
	}
	call("SteamAPI_ISteamNetworkingSockets_CloseConnection", ifSockets, uintptr(h), 0, 0, l)
}

func sendMsg(h uint32, data []byte) int32 { return sendMsgFlags(h, data, sendReliableNoNagle) }

// sendMsgFlags copies data into Steam's queue (safe to reuse the buffer afterwards).
func sendMsgFlags(h uint32, data []byte, flags int) int32 {
	if len(data) == 0 {
		return eResultOK
	}
	return int32(call("SteamAPI_ISteamNetworkingSockets_SendMessageToConnection", ifSockets, uintptr(h),
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(flags), 0))
}

// recvMsgs appends received payloads; returns -1 if the handle is invalid.
func recvMsgs(h uint32, max int, fn func([]byte)) int {
	ptrs := make([]uintptr, max)
	n := int(int32(call("SteamAPI_ISteamNetworkingSockets_ReceiveMessagesOnConnection", ifSockets, uintptr(h),
		uintptr(unsafe.Pointer(&ptrs[0])), uintptr(max))))
	for i := 0; i < n; i++ {
		m := ptrs[i]
		data := *(*uintptr)(unsafe.Pointer(m + offMsgData))
		size := int(*(*int32)(unsafe.Pointer(m + offMsgSize)))
		if size > 0 {
			b := make([]byte, size)
			copy(b, unsafe.Slice((*byte)(unsafe.Pointer(data)), size))
			fn(b)
		}
		call("SteamAPI_SteamNetworkingMessage_t_Release", m)
	}
	return n
}

type connInfo struct {
	state    int32
	listen   uint32
	remote   uint64
	popRelay uint32
	endDebug string
}

func getInfo(h uint32) (connInfo, bool) {
	var b [infoSize]byte
	if call("SteamAPI_ISteamNetworkingSockets_GetConnectionInfo", ifSockets, uintptr(h), uintptr(unsafe.Pointer(&b[0])))&0xFF == 0 {
		return connInfo{}, false
	}
	ci := connInfo{
		state:    *(*int32)(unsafe.Pointer(&b[offInfoState])),
		listen:   *(*uint32)(unsafe.Pointer(&b[offInfoListen])),
		remote:   *(*uint64)(unsafe.Pointer(&b[offInfoSteamID])),
		popRelay: *(*uint32)(unsafe.Pointer(&b[offInfoPOPRelay])),
	}
	ci.endDebug = cstr(uintptr(unsafe.Pointer(&b[offInfoEndDebug])))
	return ci, true
}

type rtStatus struct {
	ping            int32
	quality         float32
	pendingReliable int32
	unacked         int32
}

func getRT(h uint32) (rtStatus, bool) {
	var b [512]byte
	r := int32(call("SteamAPI_ISteamNetworkingSockets_GetConnectionRealTimeStatus", ifSockets, uintptr(h), uintptr(unsafe.Pointer(&b[0])), 0, 0))
	if r != eResultOK {
		return rtStatus{}, false
	}
	return rtStatus{
		ping:            *(*int32)(unsafe.Pointer(&b[offRTPing])),
		quality:         *(*float32)(unsafe.Pointer(&b[offRTQualityLocal])),
		pendingReliable: *(*int32)(unsafe.Pointer(&b[offRTPendingRelia])),
		unacked:         *(*int32)(unsafe.Pointer(&b[offRTSentUnackRelia])),
	}, true
}

// popName decodes a SteamNetworkingPOPID ("fra", "ist", ...).
func popName(id uint32) string {
	if id == 0 {
		return ""
	}
	var s []byte
	for _, sh := range []uint{24, 16, 8, 0} {
		if c := byte(id >> sh); c != 0 {
			s = append(s, c)
		}
	}
	return string(s)
}

func timeBeginPeriod1() {
	syscall.NewLazyDLL("winmm.dll").NewProc("timeBeginPeriod").Call(1)
}
