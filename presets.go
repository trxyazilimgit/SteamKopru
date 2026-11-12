package main

import (
	"fmt"
	"strconv"
	"strings"
)

// presetRow = one row a preset fills into the share table.
type presetRow struct {
	Ports string // "25565", "2456-2458" or "7777,8888"
	Proto string
}

type preset struct {
	Name string
	Rows []presetRow
	Note string
}

var presets = []preset{
	{"Minecraft Java", []presetRow{{"25565", protoTCP}}, "Dünyayı LAN'a açınca sohbette yazan portu gir — her açışta değişir. Sunucu kullanıyorsan 25565 kalsın."},
	{"Minecraft Bedrock", []presetRow{{"19132", protoUDP}}, ""},
	{"Terraria", []presetRow{{"7777", protoTCP}}, ""},
	{"Valheim", []presetRow{{"2456-2458", protoUDP}}, ""},
	{"Factorio", []presetRow{{"34197", protoUDP}}, ""},
	{"Stardew Valley", []presetRow{{"24642", protoUDP}}, ""},
	{"Project Zomboid", []presetRow{{"16261-16262", protoUDP}}, ""},
	{"Palworld", []presetRow{{"8211", protoUDP}}, ""},
	{"Don't Starve Together", []presetRow{{"10999", protoUDP}}, ""},
	{"Satisfactory", []presetRow{{"7777", protoBoth}, {"8888", protoTCP}}, ""},
	{"7 Days to Die", []presetRow{{"26900", protoTCP}, {"26900-26902", protoUDP}}, ""},
	{"Rust", []presetRow{{"28015", protoUDP}}, ""},
	{"Web sunucusu (8080)", []presetRow{{"8080", protoTCP}}, ""},
}

const maxPortsPerRow = 100

// parsePortSpec turns "25565", "2456-2458" or "7777, 8888" into port numbers.
func parsePortSpec(spec string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(strings.TrimSpace(lo))
		if err != nil || a <= 0 || a > 65535 {
			return nil, fmt.Errorf("geçersiz port: %q", part)
		}
		b := a
		if isRange {
			b, err = strconv.Atoi(strings.TrimSpace(hi))
			if err != nil || b < a || b > 65535 {
				return nil, fmt.Errorf("geçersiz port aralığı: %q", part)
			}
		}
		if len(out)+(b-a+1) > maxPortsPerRow {
			return nil, fmt.Errorf("bir satırda en fazla %d port olabilir: %q", maxPortsPerRow, spec)
		}
		for p := a; p <= b; p++ {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("port boş")
	}
	return out, nil
}

// rowSpec = one row of the host table as typed by the user (persisted as-is).
type rowSpec struct {
	Ports, Proto, Name string
}

// rows are stored as "25565/tcp=Minecraft;2456-2458/udp=Valheim"
// (older "25565=Minecraft" entries are read as TCP).
func parseRowSpecs(s string) []rowSpec {
	var out []rowSpec
	for _, part := range strings.Split(s, ";") {
		ports, name, _ := strings.Cut(part, "=")
		ports, proto, _ := strings.Cut(ports, "/")
		if _, err := parsePortSpec(ports); err != nil {
			continue
		}
		if proto != protoUDP && proto != protoBoth {
			proto = protoTCP
		}
		out = append(out, rowSpec{strings.TrimSpace(ports), proto, strings.TrimSpace(name)})
	}
	return out
}

func formatRowSpecs(rs []rowSpec) string {
	clean := strings.NewReplacer(";", " ", "=", " ", "/", " ")
	parts := make([]string, len(rs))
	for i, r := range rs {
		parts[i] = fmt.Sprintf("%s/%s=%s", strings.ReplaceAll(r.Ports, " ", ""), r.Proto, clean.Replace(r.Name))
	}
	return strings.Join(parts, ";")
}

// expandRows turns table rows into individual shares; the same port given
// twice with different protocols becomes TCP+UDP.
func expandRows(rs []rowSpec) ([]share, error) {
	var out []share
	seen := map[int]int{}
	for _, r := range rs {
		if strings.TrimSpace(r.Ports) == "" {
			continue
		}
		ports, err := parsePortSpec(r.Ports)
		if err != nil {
			return nil, err
		}
		for _, p := range ports {
			if i, ok := seen[p]; ok {
				if out[i].Proto != r.Proto {
					out[i].Proto = protoBoth
				}
				continue
			}
			seen[p] = len(out)
			out = append(out, share{Port: p, Proto: r.Proto, Name: r.Name})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("en az bir port girmelisin")
	}
	if len(out) > 256 {
		return nil, fmt.Errorf("en fazla 256 port paylaşılabilir")
	}
	return out, nil
}
