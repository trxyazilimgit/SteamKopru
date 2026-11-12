package main

import (
	_ "embed"
	"fmt"
	"image/color"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

//go:embed assets/icon.png
var iconPNG []byte

var appIcon = fyne.NewStaticResource("icon.png", iconPNG)

const (
	developerName = "Yusuf Özbaş"
	developerURL  = "https://trxyazilim.com"
)

var protoOptions = []string{"TCP", "UDP", "TCP+UDP"}

func protoFromLabel(l string) string {
	switch l {
	case "UDP":
		return protoUDP
	case "TCP+UDP":
		return protoBoth
	}
	return protoTCP
}

func stateText(f friend) string {
	if f.inApp {
		return "SteamKöprü açık"
	}
	switch f.state {
	case 0:
		return "çevrimdışı"
	case 2:
		return "meşgul"
	case 3, 4:
		return "uzakta"
	}
	return "çevrimiçi"
}

func fmtDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d sn", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d dk", int(d.Minutes()))
	}
	return fmt.Sprintf("%d sa %d dk", int(d.Hours()), int(d.Minutes())%60)
}

var accessModes = []struct{ key, label string }{
	{accessFriendsAsk, "Arkadaşlar otomatik, diğerleri için sor (önerilen)"},
	{accessFriends, "Sadece Steam arkadaşlarım bağlanabilir"},
	{accessAsk, "Herkes için bana sor"},
	{accessAll, "Herkes bağlanabilir"},
}

func main() {
	a := app.NewWithID("com.steamkopru.app")
	prefs := a.Preferences()
	a.SetIcon(appIcon)
	w := a.NewWindow("SteamKöprü")
	w.SetIcon(appIcon)
	w.Resize(fyne.NewSize(700, 680))

	// --- log + status ---
	logLines := []string{}
	logBox := widget.NewMultiLineEntry()
	logBox.Wrapping = fyne.TextWrapWord
	logBox.Disable()
	logSink = func(s string) {
		fyne.Do(func() {
			logLines = append(logLines, s)
			if len(logLines) > 300 {
				logLines = logLines[len(logLines)-300:]
			}
			logBox.SetText(strings.Join(logLines, "\n"))
			logBox.CursorRow = len(logLines)
			logBox.Refresh()
		})
	}
	defaultIdle := "Durum: başlamadı — aşağıdan bir sekme seç"
	status := widget.NewLabel(defaultIdle)
	status.Truncation = fyne.TextTruncateEllipsis // long status must not widen the window
	idleText := defaultIdle
	setIdle := func(s string) {
		idleText = s
		status.SetText(s)
	}
	statusFn = func(ping int32, route string) {
		fyne.Do(func() {
			if ping < 0 {
				status.SetText(idleText)
				return
			}
			status.SetText(fmt.Sprintf("Bağlı  •  Ping: %d ms  •  %s", ping, route))
		})
	}
	who := widget.NewLabelWithStyle("Steam'e bağlanılıyor...", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	who.Truncation = fyne.TextTruncateEllipsis
	steamReady := false

	// --- notifications ---
	var notifyOn atomic.Bool
	notifyOn.Store(prefs.BoolWithFallback("notify", true))
	notifyFn = func(title, body string) {
		if notifyOn.Load() {
			showToast(title, body)
		}
	}

	// --- access settings (loaded before Steam starts) ---
	mode := prefs.StringWithFallback("access_mode", accessFriendsAsk)
	setAccessMode(mode)
	var saved []uint64
	for _, p := range strings.Split(prefs.String("allow_always"), ",") {
		if id, err := strconv.ParseUint(strings.TrimSpace(p), 10, 64); err == nil {
			saved = append(saved, id)
		}
	}
	setAllowAlways(saved)
	var refreshAllowed func()
	saveAllow := func() {
		ids := allowAlwaysList()
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = strconv.FormatUint(id, 10)
		}
		prefs.SetString("allow_always", strings.Join(parts, ","))
		if refreshAllowed != nil {
			refreshAllowed()
		}
	}

	// incoming connection approval
	askFn = func(remote uint64, name string, friend bool) {
		fyne.Do(func() {
			w.Show()
			w.RequestFocus()
			rel := "Steam arkadaşın değil"
			if friend {
				rel = "Steam arkadaşın"
			}
			msg := widget.NewLabel(fmt.Sprintf("%s paylaştığın portlara bağlanmak istiyor.\n%s • SteamID %d", name, rel, remote))
			msg.Wrapping = fyne.TextWrapWord
			var d dialog.Dialog
			answered := false
			answer := func(allow, always bool) {
				if answered {
					return
				}
				answered = true
				d.Hide()
				go func() {
					decideAccess(remote, allow, always)
					if always {
						fyne.Do(saveAllow)
					}
				}()
			}
			deny := widget.NewButtonWithIcon("Reddet", theme.CancelIcon(), func() { answer(false, false) })
			once := widget.NewButton("Bu sefer izin ver", func() { answer(true, false) })
			always := widget.NewButtonWithIcon("Her zaman izin ver", theme.ConfirmIcon(), func() { answer(true, true) })
			always.Importance = widget.HighImportance
			d = dialog.NewCustomWithoutButtons("Bağlantı isteği",
				container.NewVBox(msg, widget.NewLabel("Cevap vermek için yaklaşık 1,5 dakikan var."),
					container.NewGridWithColumns(3, deny, once, always)), w)
			d.Resize(fyne.NewSize(520, 0))
			d.Show()
		})
	}

	// ================= host tab =================
	hostInfo := widget.NewLabel("Paylaşmak istediğin portları ekle — hazır oyun listesinden seçebilir ya da elle girebilirsin (aralık da olur: 2456-2458). Sonra \"Paylaşımı başlat\"a bas.")
	hostInfo.Wrapping = fyne.TextWrapWord

	type shareRow struct {
		port, name *widget.Entry
		proto      *widget.Select
	}
	var rows []*shareRow
	rowsBox := container.NewVBox()
	var rebuildRows func()
	rebuildRows = func() {
		rowsBox.Objects = nil
		for _, r := range rows {
			r := r
			del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
				for i, x := range rows {
					if x == r {
						rows = append(rows[:i], rows[i+1:]...)
						break
					}
				}
				rebuildRows()
			})
			rowsBox.Add(container.NewBorder(nil, nil, nil, del, container.NewGridWithColumns(3, r.port, r.proto, r.name)))
		}
		rowsBox.Refresh()
	}
	addRow := func(ports, proto, name string) {
		pe := widget.NewEntry()
		pe.SetPlaceHolder("Port / aralık")
		pe.SetText(ports)
		ne := widget.NewEntry()
		ne.SetPlaceHolder("Ad (isteğe bağlı)")
		ne.SetText(name)
		ps := widget.NewSelect(protoOptions, nil)
		ps.SetSelected(share{Proto: proto}.protoLabel())
		rows = append(rows, &shareRow{pe, ne, ps})
		rebuildRows()
	}
	for _, r := range parseRowSpecs(prefs.StringWithFallback("host_shares", "25565/tcp=Minecraft")) {
		addRow(r.Ports, r.Proto, r.Name)
	}
	if len(rows) == 0 {
		addRow("25565", protoTCP, "Minecraft")
	}
	collectRows := func() []rowSpec {
		var out []rowSpec
		for _, r := range rows {
			if strings.TrimSpace(r.port.Text) == "" {
				continue
			}
			out = append(out, rowSpec{strings.TrimSpace(r.port.Text), protoFromLabel(r.proto.Selected), strings.TrimSpace(r.name.Text)})
		}
		return out
	}

	presetNames := make([]string, len(presets))
	for i, p := range presets {
		presetNames[i] = p.Name
	}
	var presetSel *widget.Select
	presetSel = widget.NewSelect(presetNames, func(sel string) {
		if sel == "" {
			return
		}
		for _, p := range presets {
			if p.Name != sel {
				continue
			}
			// replace a single empty row instead of appending next to it
			if len(rows) == 1 && strings.TrimSpace(rows[0].port.Text) == "" {
				rows = nil
			}
			for _, r := range p.Rows {
				addRow(r.Ports, r.Proto, p.Name)
			}
			if p.Note != "" {
				dialog.ShowInformation(p.Name, p.Note, w)
			}
		}
		presetSel.ClearSelected()
	})
	presetSel.PlaceHolder = "Hazır oyun ekle..."

	addBtn := widget.NewButtonWithIcon("Port ekle", theme.ContentAddIcon(), func() { addRow("", protoTCP, "") })
	var hostBtn, stopBtn *widget.Button
	hostBtn = widget.NewButtonWithIcon("Paylaşımı başlat", theme.MediaPlayIcon(), func() {
		specs := collectRows()
		shares, err := expandRows(specs)
		if err != nil {
			dialog.ShowError(err, w)
			return
		}
		if err := startHost(shares); err != nil {
			dialog.ShowError(err, w)
			return
		}
		prefs.SetString("host_shares", formatRowSpecs(specs))
		labels := make([]string, len(specs))
		for i, r := range specs {
			labels[i] = fmt.Sprintf("%s/%s", r.Ports, share{Proto: r.Proto}.protoLabel())
			if r.Name != "" {
				labels[i] = r.Name + " (" + labels[i] + ")"
			}
		}
		hostBtn.SetText("Paylaşımı güncelle")
		stopBtn.Enable()
		setIdle("Durum: paylaşım açık — arkadaşının bağlanması bekleniyor")
		hostInfo.SetText(fmt.Sprintf("Paylaşılıyor: %s\nArkadaşın \"Arkadaşa bağlan\" sekmesinde seni seçip Bağlan'a bassın. Listede göremezse şu SteamID'yi girsin: %d",
			strings.Join(labels, ", "), mySteamID()))
	})
	stopBtn = widget.NewButtonWithIcon("Durdur", theme.MediaStopIcon(), func() {
		stopHost()
		hostBtn.SetText("Paylaşımı başlat")
		stopBtn.Disable()
		setIdle(defaultIdle)
		hostInfo.SetText("Paylaşım durduruldu. Portları düzenleyip tekrar başlatabilirsin.")
	})
	stopBtn.Disable()
	copyID := widget.NewButtonWithIcon("SteamID'mi kopyala", theme.ContentCopyIcon(), func() {
		a.Clipboard().SetContent(fmt.Sprint(mySteamID()))
	})
	bold := func(s string) *widget.Label {
		return widget.NewLabelWithStyle(s, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	}
	// spacer exactly as wide as a row's delete button so the columns line up
	delGap := canvas.NewRectangle(color.Transparent)
	delGap.SetMinSize(fyne.NewSize(widget.NewButtonWithIcon("", theme.DeleteIcon(), nil).MinSize().Width, 1))
	header := container.NewBorder(nil, nil, nil, delGap,
		container.NewGridWithColumns(3, bold("Port / aralık"), bold("Protokol"), bold("Ad")))
	hostTab := container.NewBorder(
		container.NewVBox(hostInfo, header),
		container.NewVBox(container.NewGridWithColumns(2, presetSel, addBtn), container.NewGridWithColumns(2, hostBtn, stopBtn), copyID),
		nil, nil, container.NewVScroll(rowsBox),
	)

	// ================= client tab =================
	var allF, shown []friend
	var selectedID uint64
	selectedIdx := -1
	friendList := widget.NewList(
		func() int { return len(shown) },
		func() fyne.CanvasObject {
			l := widget.NewLabel("")
			l.Truncation = fyne.TextTruncateEllipsis
			return l
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			l := o.(*widget.Label)
			f := shown[i]
			l.SetText(fmt.Sprintf("%s   —   %s", f.name, stateText(f)))
			switch {
			case f.inApp:
				l.Importance = widget.SuccessImportance
			case f.state == 0:
				l.Importance = widget.LowImportance
			default:
				l.Importance = widget.MediumImportance
			}
			l.Refresh()
		},
	)
	friendList.OnSelected = func(i widget.ListItemID) {
		if i >= 0 && i < len(shown) {
			selectedID = shown[i].id
			selectedIdx = i
		}
	}
	search := widget.NewEntry()
	search.SetPlaceHolder("Arkadaş ara...")
	emptyHint := widget.NewLabel("")
	emptyHint.Wrapping = fyne.TextWrapWord
	applyFilter := func() {
		q := strings.ToLower(strings.TrimSpace(search.Text))
		shown = nil
		newIdx := -1
		for _, f := range allF {
			if q == "" || strings.Contains(strings.ToLower(f.name), q) {
				if f.id == selectedID {
					newIdx = len(shown)
				}
				shown = append(shown, f)
			}
		}
		friendList.Refresh()
		if newIdx != selectedIdx {
			if newIdx < 0 {
				friendList.UnselectAll()
				selectedIdx = -1
			} else {
				friendList.Select(newIdx)
			}
		}
	}
	search.OnChanged = func(string) { applyFilter() }
	refresh := func() {
		if !steamReady {
			return
		}
		allF = allFriends()
		applyFilter()
		inApp := 0
		for _, f := range allF {
			if f.inApp {
				inApp++
			}
		}
		switch {
		case len(allF) == 0:
			emptyHint.SetText("Steam arkadaş listen boş görünüyor. Arkadaşının SteamID'sini aşağıya girebilirsin.")
		case inApp == 0:
			emptyHint.SetText("SteamKöprü'yü açmış görünen arkadaş yok (görünmez moddaysa ya da oyun detayları gizliyse görünmez). Yine de listeden seçip Bağlan'a basabilirsin.")
		default:
			emptyHint.SetText("Yeşil olanlar SteamKöprü'yü açmış. Arkadaşını seç ve Bağlan'a bas.")
		}
	}
	idEntry := widget.NewEntry()
	idEntry.SetPlaceHolder("Ya da SteamID yapıştır (isteğe bağlı)")
	mapsBox := container.NewVBox()
	var connectBtn *widget.Button
	connectBtn = widget.NewButtonWithIcon("Bağlan", theme.LoginIcon(), func() {
		var target uint64
		if s := strings.TrimSpace(idEntry.Text); s != "" {
			n, err := strconv.ParseUint(s, 10, 64)
			if err != nil || n < 1<<52 {
				dialog.ShowError(fmt.Errorf("geçersiz SteamID"), w)
				return
			}
			target = n
		} else if selectedID != 0 {
			target = selectedID
		} else {
			dialog.ShowInformation("Seçim yok", "Listeden bir arkadaş seç ya da SteamID gir.", w)
			return
		}
		connectBtn.Disable()
		connectBtn.SetText("Bağlanılıyor...")
		setIdle("Durum: arkadaşına bağlanılıyor... (arkadaşının onayı gerekebilir)")
		go func() {
			shares, err := queryHost(target)
			var maps []mapping
			if err == nil {
				maps, err = startClient(target, shares)
			}
			fyne.Do(func() {
				connectBtn.Enable()
				connectBtn.SetText("Bağlan")
				if err != nil {
					setIdle(defaultIdle)
					dialog.ShowError(err, w)
					return
				}
				mapsBox.Objects = nil
				mapsBox.Add(bold("Oyunda bu adreslere bağlan:"))
				list := container.NewVBox()
				for _, m := range maps {
					local := m.LocalTCP
					if local == 0 {
						local = m.LocalUDP
					}
					addr := fmt.Sprintf("localhost:%d", local)
					txt := fmt.Sprintf("%s  →  %s", m.share.label(), addr)
					if m.LocalTCP != 0 && m.LocalUDP != 0 && m.LocalTCP != m.LocalUDP {
						txt = fmt.Sprintf("%s  →  TCP localhost:%d, UDP localhost:%d", m.share.label(), m.LocalTCP, m.LocalUDP)
					}
					if local != m.Port {
						txt += "  (port dolu olduğu için farklı)"
					}
					cp := widget.NewButtonWithIcon("", theme.ContentCopyIcon(), func() { a.Clipboard().SetContent(addr) })
					addrLabel := widget.NewLabelWithStyle(txt, fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
					addrLabel.Wrapping = fyne.TextWrapWord
					list.Add(container.NewBorder(nil, nil, nil, cp, addrLabel))
				}
				if len(maps) > 4 { // long ranges: keep the friend list visible
					sc := container.NewVScroll(list)
					sc.SetMinSize(fyne.NewSize(0, 160))
					mapsBox.Add(sc)
				} else {
					mapsBox.Add(list)
				}
				mapsBox.Refresh()
				setIdle("Durum: hazır — oyunda aşağıdaki adreslere bağlan")
			})
		}()
	})
	clientTab := container.NewBorder(
		container.NewVBox(emptyHint, container.NewBorder(nil, nil, nil,
			widget.NewButtonWithIcon("Yenile", theme.ViewRefreshIcon(), refresh), search)),
		container.NewVBox(idEntry, connectBtn, mapsBox),
		nil, nil, friendList,
	)

	// ================= connections tab =================
	var conns []connRow
	type connWidgets struct {
		title, detail, traffic *widget.Label
		kick, block            *widget.Button
	}
	items := map[fyne.CanvasObject]*connWidgets{}
	connSummary := widget.NewLabel("Aktif bağlantı yok")
	connSummary.Wrapping = fyne.TextWrapWord
	connList := widget.NewList(
		func() int { return len(conns) },
		func() fyne.CanvasObject {
			it := &connWidgets{
				title:   bold(""),
				detail:  widget.NewLabel(""),
				traffic: widget.NewLabel(""),
				kick:    widget.NewButtonWithIcon("Kes", theme.CancelIcon(), nil),
				block:   widget.NewButtonWithIcon("Engelle", theme.ErrorIcon(), nil),
			}
			for _, l := range []*widget.Label{it.title, it.detail, it.traffic} {
				l.Truncation = fyne.TextTruncateEllipsis
			}
			it.block.Importance = widget.DangerImportance
			o := container.NewBorder(nil, nil, nil, container.NewHBox(it.kick, it.block), container.NewVBox(it.title, it.detail, it.traffic))
			items[o] = it
			return o
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			it := items[o]
			if it == nil || i >= len(conns) {
				return
			}
			c := conns[i]
			dir := "sana bağlandı"
			if c.Outgoing {
				dir = "bağlandığın paylaşım"
			}
			it.title.SetText(fmt.Sprintf("%s — %s", c.Name, dir))
			ping := "—"
			if c.Ping >= 0 {
				ping = fmt.Sprintf("%d ms", c.Ping)
			}
			it.detail.SetText(fmt.Sprintf("%s %d  •  Ping %s  •  %s", c.Proto, c.Port, ping, c.Route))
			it.traffic.SetText(fmt.Sprintf("Alınan %s  •  Gönderilen %s  •  %s",
				humanBytes(c.Recv), humanBytes(c.Sent), fmtDur(time.Since(c.Since))))
			h, remote, name := c.H, c.Remote, c.Name
			it.kick.OnTapped = func() { kickConn(h) }
			if c.Outgoing {
				it.block.Hide()
			} else {
				it.block.Show()
				it.block.OnTapped = func() {
					dialog.ShowConfirm("Engelle", fmt.Sprintf("%s engellensin mi? Tüm bağlantıları kesilir ve program kapanana kadar tekrar bağlanamaz (kalıcı izni de silinir).", name),
						func(ok bool) {
							if ok {
								blockPeer(remote)
								saveAllow()
							}
						}, w)
				}
			}
		},
	)
	connList.OnSelected = func(widget.ListItemID) { connList.UnselectAll() }
	refreshConns := func() {
		if !steamReady {
			return
		}
		conns = snapshotConns()
		if len(conns) == 0 {
			connSummary.SetText("Aktif bağlantı yok")
		} else {
			var in, out int64
			peers := map[uint64]bool{}
			for _, c := range conns {
				in += c.Recv
				out += c.Sent
				peers[c.Remote] = true
			}
			connSummary.SetText(fmt.Sprintf("%d bağlantı, %d kişi  •  Toplam alınan %s  •  gönderilen %s",
				len(conns), len(peers), humanBytes(in), humanBytes(out)))
		}
		connList.Refresh()
	}
	connTab := container.NewBorder(connSummary, nil, nil, nil, connList)

	// ================= settings tab =================
	modeLabels := make([]string, len(accessModes))
	for i, m := range accessModes {
		modeLabels[i] = m.label
	}
	accessRadio := widget.NewRadioGroup(modeLabels, func(sel string) {
		for _, m := range accessModes {
			if m.label == sel {
				setAccessMode(m.key)
				prefs.SetString("access_mode", m.key)
			}
		}
	})
	accessRadio.Required = true
	for _, m := range accessModes {
		if m.key == mode {
			accessRadio.SetSelected(m.label)
		}
	}
	allowedLabel := widget.NewLabel("")
	allowedLabel.Wrapping = fyne.TextWrapWord
	refreshAllowed = func() {
		ids := allowAlwaysList()
		if len(ids) == 0 {
			allowedLabel.SetText("Her zaman izinli kimse yok.")
			return
		}
		names := make([]string, len(ids))
		for i, id := range ids {
			if steamReady {
				names[i] = displayName(id)
			} else {
				names[i] = fmt.Sprint(id)
			}
		}
		allowedLabel.SetText("Her zaman izinli: " + strings.Join(names, ", "))
	}
	clearAllowed := widget.NewButtonWithIcon("İzin listesini temizle", theme.DeleteIcon(), func() {
		setAllowAlways(nil)
		saveAllow()
	})
	trayCheck := widget.NewCheck("Kapatınca sistem tepsisine küçült", func(b bool) {
		prefs.SetBool("tray_close", b)
	})
	trayCheck.SetChecked(prefs.BoolWithFallback("tray_close", true))
	notifyCheck := widget.NewCheck("Masaüstü bildirimleri", func(b bool) {
		notifyOn.Store(b)
		prefs.SetBool("notify", b)
	})
	notifyCheck.SetChecked(notifyOn.Load())
	testNotify := widget.NewButtonWithIcon("Bildirimi dene", theme.InfoIcon(), func() {
		showToast("SteamKöprü", "Bildirimler çalışıyor")
	})
	hint := func(s string) *widget.Label {
		l := widget.NewLabel(s)
		l.Wrapping = fyne.TextWrapWord
		l.Importance = widget.LowImportance
		return l
	}
	settingsTab := container.NewVScroll(container.NewVBox(
		bold("Kimler bağlanabilir?"),
		accessRadio,
		hint("\"Herkes bağlanabilir\" seçilirse SteamID'ni bilen herkes paylaştığın portlara erişebilir."),
		allowedLabel,
		container.NewHBox(clearAllowed),
		widget.NewSeparator(),
		bold("Uygulama"),
		trayCheck,
		hint("Pencere kapansa da program arka planda çalışmaya devam eder, paylaşım ve bağlantılar kopmaz."),
		notifyCheck,
		hint("Biri bağlandığında, ayrıldığında ya da bağlantı isteği geldiğinde bildirim gösterir."),
		container.NewHBox(testNotify),
	))

	// ================= developer tab =================
	devLogo := canvas.NewImageFromResource(appIcon)
	devLogo.FillMode = canvas.ImageFillContain
	devLogo.SetMinSize(fyne.NewSize(96, 96))
	devTitle := widget.NewLabelWithStyle("SteamKöprü", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	devDesc := widget.NewLabelWithStyle("Steam üzerinden arkadaşınla port paylaş", fyne.TextAlignCenter, fyne.TextStyle{})
	devDesc.Wrapping = fyne.TextWrapWord
	devName := widget.NewLabelWithStyle("Geliştirici: "+developerName, fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	devSite, _ := url.Parse(developerURL)
	devLink := widget.NewHyperlinkWithStyle(strings.TrimPrefix(developerURL, "https://"), devSite, fyne.TextAlignCenter, fyne.TextStyle{})
	devOpen := widget.NewButtonWithIcon("Web sitesini aç", theme.ComputerIcon(), func() { _ = a.OpenURL(devSite) })
	devOpen.Importance = widget.HighImportance
	devTab := container.NewVScroll(container.NewVBox(
		layoutSpacer(),
		container.NewCenter(devLogo),
		devTitle,
		devDesc,
		widget.NewSeparator(),
		devName,
		devLink,
		container.NewCenter(devOpen),
	))

	hostItem := container.NewTabItemWithIcon("Paylaşan benim", theme.HomeIcon(), container.NewPadded(hostTab))
	clientItem := container.NewTabItemWithIcon("Arkadaşa bağlan", theme.LoginIcon(), container.NewPadded(clientTab))
	connItem := container.NewTabItemWithIcon("Bağlantılar", theme.ListIcon(), container.NewPadded(connTab))
	settingsItem := container.NewTabItemWithIcon("Ayarlar", theme.SettingsIcon(), container.NewPadded(settingsTab))
	devItem := container.NewTabItemWithIcon("Geliştirici", theme.AccountIcon(), container.NewPadded(devTab))
	tabs := container.NewAppTabs(hostItem, clientItem, connItem, settingsItem, devItem)
	tabs.OnSelected = func(t *container.TabItem) {
		switch t {
		case clientItem:
			refresh()
		case connItem:
			refreshConns()
		case settingsItem:
			refreshAllowed()
		}
	}

	split := container.NewVSplit(tabs, container.NewBorder(widget.NewLabel("Kayıt"), nil, nil, nil, logBox))
	split.Offset = 0.68
	logo := canvas.NewImageFromResource(appIcon)
	logo.FillMode = canvas.ImageFillContain
	logo.SetMinSize(fyne.NewSize(52, 52))
	title := canvas.NewText("SteamKöprü", theme.Color(theme.ColorNamePrimary))
	title.TextSize = 22
	title.TextStyle = fyne.TextStyle{Bold: true}
	sub := canvas.NewText("Steam üzerinden arkadaşınla port paylaş", color.Gray{Y: 0x99})
	sub.TextSize = 12
	brand := container.NewHBox(logo, container.NewVBox(layoutSpacer(), title, sub, layoutSpacer()))
	topBar := container.NewVBox(brand, widget.NewSeparator(), who, status, widget.NewSeparator())
	w.SetContent(container.NewBorder(topBar, nil, nil, nil, split))

	// --- system tray ---
	hasTray := false
	if desk, ok := a.(desktop.App); ok {
		hasTray = true
		show := fyne.NewMenuItem("Göster", func() { w.Show(); w.RequestFocus() })
		connsMI := fyne.NewMenuItem("Bağlantılar", func() { w.Show(); tabs.Select(connItem); w.RequestFocus() })
		quit := fyne.NewMenuItem("Çıkış", nil)
		quit.IsQuit = true
		desk.SetSystemTrayMenu(fyne.NewMenu("SteamKöprü", show, connsMI, fyne.NewMenuItemSeparator(), quit))
		desk.SetSystemTrayIcon(appIcon)
		desk.SetSystemTrayWindow(w)
	}
	w.SetCloseIntercept(func() {
		if !hasTray || !steamReady || !prefs.BoolWithFallback("tray_close", true) {
			a.Quit()
			return
		}
		if prefs.Bool("tray_told") {
			w.Hide()
			return
		}
		prefs.SetBool("tray_told", true)
		d := dialog.NewInformation("Arka planda çalışıyor",
			"SteamKöprü sistem tepsisinde (saatin yanında) çalışmaya devam edecek, paylaşım ve bağlantılar kopmaz.\n\nTamamen kapatmak için tepsideki simgeye sağ tıklayıp \"Çıkış\"ı seç. Bunu Ayarlar'dan kapatabilirsin.", w)
		d.SetOnClosed(w.Hide) // hide once the user has read it
		d.Show()
	})
	a.Lifecycle().SetOnStopped(func() {
		steamReady = false
		if ifSockets != 0 {
			shutdownCore()
		}
	})

	hostBtn.Disable()
	connectBtn.Disable()
	go func() {
		err := startCore()
		fyne.Do(func() {
			if err != nil {
				who.SetText("Steam'e bağlanılamadı")
				d := dialog.NewError(err, w)
				d.SetOnClosed(func() { a.Quit() })
				d.Show()
				return
			}
			steamReady = true
			who.SetText("Steam: " + myName())
			hostBtn.Enable()
			connectBtn.Enable()
			if tabs.Selected() == clientItem {
				refresh()
			}
			logf("Steam'e bağlanıldı: %s", myName())
		})
		if err != nil {
			return
		}
		fast := time.NewTicker(time.Second)
		slow := time.NewTicker(8 * time.Second)
		for {
			select {
			case <-fast.C: // live connection panel
				fyne.Do(func() {
					if tabs.Selected() == connItem {
						refreshConns()
					}
				})
			case <-slow.C: // keep the friend list fresh while the client tab is open
				fyne.Do(func() {
					if tabs.Selected() == clientItem {
						refresh()
					}
				})
			}
		}
	}()
	w.ShowAndRun()
}

// layoutSpacer is a tiny fixed gap used to vertically centre the title block.
func layoutSpacer() fyne.CanvasObject {
	r := canvas.NewRectangle(color.Transparent)
	r.SetMinSize(fyne.NewSize(1, 2))
	return r
}
