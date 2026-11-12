package main

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf16"
)

// Windows toast notifications.
//
// Fyne's own SendNotification embeds the text in a double-quoted PowerShell
// string, where "$(...)" is executed - and our texts contain Steam persona
// names, which anyone can choose. So we build the script ourselves: every
// dynamic value is whitelisted to letters/digits/safe punctuation (no quotes
// of any kind, no '$', no backtick) and placed in a single-quoted literal,
// which PowerShell never expands.

const toastAppID = "com.steamkopru.app"

var toastQueue = make(chan [2]string, 8)

func init() {
	go func() {
		for n := range toastQueue {
			runToast(n[0], n[1])
		}
	}()
}

// showToast queues a notification; drops it if too many are pending.
func showToast(title, body string) {
	select {
	case toastQueue <- [2]string{title, body}:
	default:
	}
}

// safeText keeps only characters that cannot break out of a PowerShell
// single-quoted string.
func safeText(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= max {
			b.WriteString("...")
			break
		}
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ':
		case strings.ContainsRune("-_.,:;!?()[]+#@&*/=~%", r):
		default:
			continue
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// iconFile writes the app icon once to the user cache dir for the toast image.
func iconFile() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(dir, "SteamKopru", "icon.png")
	if _, err := os.Stat(p); err != nil {
		os.MkdirAll(filepath.Dir(p), 0755)
		if os.WriteFile(p, iconPNG, 0644) != nil {
			return ""
		}
	}
	// the path goes into a single-quoted literal too
	if strings.ContainsAny(p, "'‘’‚‛`$\"“”„") {
		return ""
	}
	return p
}

func runToast(title, body string) { runPS(buildToastScript(title, body, iconFile())) }

func buildToastScript(title, body, icon string) string {
	// Registering the AppUserModelID under HKCU lets an unpackaged app show
	// toasts with its own name and icon (no Start-menu shortcut needed).
	script := `$ErrorActionPreference='Stop'
$id='` + toastAppID + `'
$k='HKCU:\Software\Classes\AppUserModelId\'+$id
if(-not(Test-Path $k)){New-Item -Path $k -Force|Out-Null}
Set-ItemProperty -Path $k -Name DisplayName -Value 'SteamKöprü'
` + iconLine(icon) + `
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
$t=[Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastImageAndText02)
$t.GetElementsByTagName('text').Item(0).AppendChild($t.CreateTextNode('` + safeText(title, 80) + `'))>$null
$t.GetElementsByTagName('text').Item(1).AppendChild($t.CreateTextNode('` + safeText(body, 200) + `'))>$null
` + imageLine(icon) + `
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($id).Show([Windows.UI.Notifications.ToastNotification]::new($t))`
	return script
}

func iconLine(icon string) string {
	if icon == "" {
		return ""
	}
	return `Set-ItemProperty -Path $k -Name IconUri -Value '` + icon + `'`
}

func imageLine(icon string) string {
	if icon == "" {
		return ""
	}
	return `$t.GetElementsByTagName('image').Item(0).SetAttribute('src','file:///` + strings.ReplaceAll(icon, `\`, "/") + `')`
}

// runPS runs a script hidden, passed as -EncodedCommand (UTF-16LE base64), so
// no temp file and no command-line quoting is involved.
func runPS(script string) error {
	u := utf16.Encode([]rune(script))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(b))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return cmd.Run()
}
