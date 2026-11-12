package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParsePortSpec(t *testing.T) {
	ok := map[string][]int{
		"25565":       {25565},
		"2456-2458":   {2456, 2457, 2458},
		" 7777, 8888": {7777, 8888},
		"1-2,5":       {1, 2, 5},
	}
	for in, want := range ok {
		got, err := parsePortSpec(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q -> %v %v, want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "0", "70000", "10-5", "abc", "1-200", "5-"} {
		if _, err := parsePortSpec(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestExpandRowsMerge(t *testing.T) {
	// 7 Days to Die preset: 26900 TCP + 26900-26902 UDP
	got, err := expandRows([]rowSpec{{"26900", protoTCP, "7d"}, {"26900-26902", protoUDP, "7d"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []share{{26900, "7d", protoBoth}, {26901, "7d", protoUDP}, {26902, "7d", protoUDP}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestRowSpecsRoundTrip(t *testing.T) {
	in := []rowSpec{{"2456-2458", protoUDP, "Valheim"}, {"25565", protoTCP, "Mine;craft=x"}}
	got := parseRowSpecs(formatRowSpecs(in))
	if len(got) != 2 || got[0] != in[0] || got[1].Name != "Mine craft x" {
		t.Errorf("got %+v", got)
	}
	// old format
	if r := parseRowSpecs("25565=Minecraft"); len(r) != 1 || r[0].Proto != protoTCP {
		t.Errorf("old format: %+v", r)
	}
}

// Steam persona names are attacker-controlled; they must never be able to run
// code in the toast script. Parse the script with PowerShell's own parser and
// make sure no expandable string / subexpression / extra command appears.
func TestToastScriptInjection(t *testing.T) {
	evil := []string{
		`$(Start-Process calc)`,
		`"; Start-Process calc; "`,
		`'; Start-Process calc; '`,
		"‘; Start-Process calc; ’", // PowerShell treats smart quotes as quotes
		"“$(calc)”",
		"`$(calc)`",
		"a\r\nStart-Process calc",
		`${env:USERNAME} @(calc)`,
		"Ömer Çağrı ŞİMŞEK 123 [TR]", // must survive
	}
	for _, e := range evil {
		s := buildToastScript("Bağlantı isteği", e+" paylaşımına bağlanmak istiyor", `C:\Users\x\AppData\Local\SteamKopru\icon.png`)
		dir := t.TempDir()
		f := filepath.Join(dir, "s.ps1")
		os.WriteFile(f, []byte("\ufeff"+s), 0644)
		check := `$t=$null;$e=$null;$ast=[System.Management.Automation.Language.Parser]::ParseFile('` + f + `',[ref]$t,[ref]$e);` +
			`if($e.Count){'PARSEERR'};` +
			`$ast.FindAll({param($n) $n -is [System.Management.Automation.Language.ExpandableStringExpressionAst] -or $n -is [System.Management.Automation.Language.SubExpressionAst]},$true).Count;` +
			`($ast.FindAll({param($n) $n -is [System.Management.Automation.Language.CommandAst]},$true) | % { $_.GetCommandName() }) -join ','`
		out, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", check).CombinedOutput()
		if err != nil {
			t.Fatalf("powershell: %v %s", err, out)
		}
		lines := strings.Fields(strings.TrimSpace(string(out)))
		if len(lines) < 2 || lines[0] != "0" {
			t.Errorf("%q: expandable strings/subexpressions found or parse error: %s", e, out)
			continue
		}
		// the only commands the script may run
		for _, c := range strings.Split(lines[1], ",") {
			switch c {
			case "Test-Path", "New-Item", "Out-Null", "Set-ItemProperty":
			default:
				t.Errorf("%q: unexpected command %q", e, c)
			}
		}
	}
	if got := safeText("Ömer Çağrı ŞİMŞEK 123 [TR]", 100); got != "Ömer Çağrı ŞİMŞEK 123 [TR]" {
		t.Errorf("names mangled: %q", got)
	}
}
