package server

import (
	"net"
	"strings"
	"testing"

	"rsc.io/qr"
)

// unblock reads QR's lines back into the light/dark matrix they draw,
// quiet zone included.
func unblock(t *testing.T, lines []string) [][]bool {
	t.Helper()
	var rows [][]bool
	for _, l := range lines {
		top, bottom := []bool{}, []bool{}
		for _, r := range l {
			switch r {
			case '█':
				top, bottom = append(top, true), append(bottom, true)
			case '▀':
				top, bottom = append(top, true), append(bottom, false)
			case '▄':
				top, bottom = append(top, false), append(bottom, true)
			case ' ':
				top, bottom = append(top, false), append(bottom, false)
			default:
				t.Fatalf("unexpected rune %q", r)
			}
		}
		rows = append(rows, top, bottom)
	}
	return rows
}

func TestQRMatchesTheCode(t *testing.T) {
	url := "http://192.168.1.23:7879/#token=0123456789abcdef0123456789abcdef"
	lines, err := QR(url)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := qr.Encode(url, qr.L)
	n := code.Size + 2*QRQuiet
	if len(lines) != (n+1)/2 {
		t.Fatalf("%d lines for %d modules", len(lines), n)
	}
	m := unblock(t, lines)
	for y := range n {
		if len(m[y]) != n {
			t.Fatalf("row %d is %d wide, want %d", y, len(m[y]), n)
		}
		for x := range n {
			quiet := x < QRQuiet || y < QRQuiet || x >= n-QRQuiet || y >= n-QRQuiet
			want := quiet || !code.Black(x-QRQuiet, y-QRQuiet) // drawn = light
			if m[y][x] != want {
				t.Fatalf("module %d,%d: drawn %v, want %v", x, y, m[y][x], want)
			}
		}
	}
	// The finder pattern's dark corner sits right inside the quiet zone.
	if m[QRQuiet][QRQuiet] || !m[QRQuiet-1][QRQuiet-1] {
		t.Fatal("finder pattern is not where it belongs")
	}
	if strings.ContainsRune(strings.Join(lines, ""), '\x1b') {
		t.Fatal("QR lines must not depend on terminal colors")
	}
}

func TestPickHosts(t *testing.T) {
	a := func(name, ip string, up, loop bool) HostAddr {
		return HostAddr{Iface: name, IP: net.ParseIP(ip), Up: up, Loopback: loop}
	}
	got := PickHosts([]HostAddr{
		a("lo0", "127.0.0.1", true, true),
		a("utun3", "100.101.102.103", true, false), // Tailscale
		a("en5", "172.20.1.4", true, false),
		a("en0", "fe80::1", true, false), // link-local
		a("en0", "2001:db8::5", true, false),
		a("en9", "192.168.7.9", false, false), // down
		a("en1", "10.0.0.8", true, false),
		a("en0", "192.168.1.23", true, false),
		a("en2", "172.32.0.1", true, false), // not private
		a("en0", "192.168.1.23", true, false),
	})
	want := "192.168.1.23 10.0.0.8 172.20.1.4 100.101.102.103 172.32.0.1 [2001:db8::5]"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %v\nwant %s", got, want)
	}
}

func TestServeBannerQR(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	var loop, lan strings.Builder
	banner(&loop, "v", "/w", "127.0.0.1:7878", "tok")
	if out := loop.String(); strings.Contains(out, "█") || strings.Contains(out, "warning") || !strings.Contains(out, "http://127.0.0.1:7878/#token=tok") {
		t.Fatalf("loopback banner:\n%s", out)
	}
	banner(&lan, "v", "/w", "192.168.1.23:7878", "tok") // nothing listens: only the text
	if out := lan.String(); !strings.Contains(out, "http://192.168.1.23:7878/#token=tok") || !strings.Contains(out, "without TLS") || !strings.Contains(out, "█") {
		t.Fatalf("LAN banner:\n%s", out)
	}
}

func TestWebLinks(t *testing.T) {
	if got := WebLinks("127.0.0.1:7879", "tok"); len(got) != 1 || got[0] != "http://127.0.0.1:7879/#token=tok" {
		t.Fatalf("loopback %v", got)
	}
	if got := WebLinks("[::1]:80", "tok"); len(got) != 1 || got[0] != "http://[::1]:80/#token=tok" {
		t.Fatalf("v6 %v", got)
	}
	for _, l := range WebLinks("0.0.0.0:7879", "tok") { // this machine's addresses
		if !strings.HasSuffix(l, ":7879/#token=tok") || strings.Contains(l, "0.0.0.0") {
			t.Fatalf("wildcard link %q", l)
		}
	}
}
