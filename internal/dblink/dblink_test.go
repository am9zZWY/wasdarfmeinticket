package dblink

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseShareLink(t *testing.T) {
	const id = "a30982d3-9bbf-492d-828c-43a46ce38546"
	ok := []string{
		id,
		"https://www.bahn.de/buchung/start?vbid=" + id,
		"https://int.bahn.de/en/buchung/start?vbid=" + id,
		"Tübingen Hbf → Stuttgart Hbf, 24.11. 09:58\nhttps://www.bahn.de/buchung/start?vbid=" + id + ".",
		"https://www.bahn.de/buchung/fahrplan/suche#sts=true&vbid=" + id,
	}
	for _, input := range ok {
		ref, err := Parse(input)
		if err != nil || ref.VBID != id || ref.Search != nil {
			t.Errorf("Parse(%q) = %+v, %v", input, ref, err)
		}
	}
	bad := []string{
		"",
		"https://example.com/?vbid=" + id,
		"https://evilbahn.de/?vbid=" + id,
		"https://www.bahn.de/buchung/start?vbid=nope",
		"https://www.bahn.de/buchung/fahrplan/suche#sts=true&so=T%C3%BCbingen",
	}
	for _, input := range bad {
		if _, err := Parse(input); err == nil {
			t.Errorf("Parse(%q): expected error", input)
		}
	}
}

// A search link as bahn.de's web app produces it.
const searchLink = "https://www.bahn.de/buchung/fahrplan/suche#sts=true&so=Singen(Hohentwiel)&zo=Kreuzlingen&kl=2&r=13:16:KLASSENLOS:1" +
	"&soid=A%3D1%40O%3DSingen(Hohentwiel)%40X%3D8841268%40Y%3D47758879%40U%3D81%40L%3D8000073%40p%3D1795086023%40i%3DU%C3%97008014558%40" +
	"&zoid=A%3D1%40O%3DKreuzlingen%40X%3D9169078%40Y%3D47652555%40U%3D81%40L%3D8506131%40p%3D1795086023%40i%3DU%C3%97008506131%40" +
	"&sot=ST&zot=ST&soei=8000073&zoei=8506131&hd=2026-11-23T09:47:58&hza=D&hz=%5B%5D&ar=false&s=true&d=false" +
	"&vm=00,01,02,03,04,05,06,07,08,09&fm=false&bp=false&dlt=false&nfv=false&dltv=false"

func TestParseSearchLink(t *testing.T) {
	ref, err := Parse("schau mal " + searchLink)
	if err != nil || ref.VBID != "" || ref.Search == nil {
		t.Fatalf("Parse = %+v, %v", ref, err)
	}
	search := ref.Search
	if search.From.Name != "Singen(Hohentwiel)" || search.From.ID != "8000073" || search.From.Lat != 47.758879 || search.From.Lon != 8.841268 {
		t.Errorf("from = %+v", search.From)
	}
	if search.To.Name != "Kreuzlingen" || search.To.ID != "8506131" || search.To.Lat != 47.652555 || search.To.Lon != 9.169078 {
		t.Errorf("to = %+v", search.To)
	}
	// 09:47:58 local time in Germany, cut to the minute.
	if want := time.Date(2026, 11, 23, 8, 47, 0, 0, time.UTC); !search.Departure.Equal(want) {
		t.Errorf("departure = %v, want %v", search.Departure, want)
	}

	for name, link := range map[string]string{
		"arrival search": strings.Replace(searchLink, "hza=D", "hza=A", 1),
		"no time":        strings.Replace(searchLink, "hd=2026-11-23T09:47:58", "hd=", 1),
		"no position":    strings.Replace(searchLink, "%40X%3D9169078%40Y%3D47652555", "", 1),
	} {
		if ref, err := Parse(link); err == nil || err == ErrNoLink {
			t.Errorf("%s: Parse = %+v, %v; want a specific error", name, ref, err)
		}
	}
}

// The address bahn.de shows while a connection is open, as pasted by a user.
func TestParseConnectionLink(t *testing.T) {
	raw, err := os.ReadFile("testdata/connection_link.txt")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := Parse("hier: " + string(raw))
	if err != nil || ref.VBID != "" || ref.Search != nil {
		t.Fatalf("Parse = %+v, %v", ref, err)
	}
	if !strings.HasPrefix(ref.Recon, "¶HKI¶T$A=1@O=Singen(Hohentwiel)@") ||
		!strings.Contains(ref.Recon, "$202611211109$202611211112$IR           2118$") {
		t.Errorf("recon = %q", ref.Recon)
	}
	// Base64 survives: "+" must not have become a space.
	if !strings.Contains(ref.Recon, "jSz+WRyZm9EYP5DRbZgCs5QpEp7DB/LFLDC") || !strings.Contains(ref.Recon, "Sq1myH+ska72rS1czA") || strings.Contains(ref.Recon, "jSz WRy") {
		t.Error("base64 part of the recon was altered")
	}
	if strings.Contains(ref.Recon, "&bp=false") {
		t.Error("recon runs into the next parameter")
	}
}

func TestParseLocation(t *testing.T) {
	stop := ParseLocation("A=1@O=Horb@X=8688982@Y=48441475@L=8000177@a=128@")
	if stop.Name != "Horb" || stop.ID != "8000177" || stop.Lon != 8.688982 || stop.Lat != 48.441475 {
		t.Errorf("stop = %+v", stop)
	}
	if stop := ParseLocation("nonsense"); stop.Name != "" || stop.HasCoord() {
		t.Errorf("nonsense = %+v", stop)
	}
}
