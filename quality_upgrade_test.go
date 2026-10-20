package main

import (
	"strings"
	"testing"
)

const samplePlayJSON = `{"data":{"episode_no":3,"streams":[
 {"quality":"576p","codec":"h264","protocol":"mp4","width":576,"height":1024,"url":"https://cdn.example.com/a/576h264.mp4?sig=1"},
 {"quality":"576p","codec":"h265","protocol":"mp4","width":576,"height":1024,"url":"https://cdn.example.com/a/576h265.mp4?sig=1"},
 {"quality":"720p","codec":"h265","protocol":"mp4","width":720,"height":1280,"url":"https://cdn.example.com/a/720h265.mp4?sig=1"},
 {"quality":"1080p","codec":"h265","protocol":"mp4","width":1080,"height":1920,"url":"https://cdn.example.com/a/1080h265.mp4?sig=1"}
]}}`

func TestBetterVariantPicksHighest(t *testing.T) {
	bodies := [][]byte{[]byte(`{"other":[1,2]}`), []byte(samplePlayJSON)}
	best, played, ok := betterVariantFromPageJSON(bodies, "https://cdn.example.com/a/576h264.mp4?sig=1", 0)
	if !ok || best.Height != 1920 || played.Height != 1024 {
		t.Fatalf("got ok=%v best=%+v played=%+v", ok, best, played)
	}
}

func TestBetterVariantRespects720Limit(t *testing.T) {
	best, _, ok := betterVariantFromPageJSON([][]byte{[]byte(samplePlayJSON)}, "https://cdn.example.com/a/576h264.mp4?sig=1", 720)
	if !ok || best.Width != 720 {
		t.Fatalf("got ok=%v best=%+v", ok, best)
	}
}

func TestBetterVariantMatchesReissuedQuery(t *testing.T) {
	_, _, ok := betterVariantFromPageJSON([][]byte{[]byte(samplePlayJSON)}, "https://cdn.example.com/a/576h264.mp4?sig=2", 0)
	if !ok {
		t.Fatal("same host and path with a new signature should match")
	}
}

func TestBetterVariantNoChange(t *testing.T) {
	// Already the best rendition.
	if _, _, ok := betterVariantFromPageJSON([][]byte{[]byte(samplePlayJSON)}, "https://cdn.example.com/a/1080h265.mp4?sig=1", 0); ok {
		t.Fatal("expected no upgrade")
	}
	// Played URL not in any list.
	if _, _, ok := betterVariantFromPageJSON([][]byte{[]byte(samplePlayJSON)}, "https://cdn.example.com/b/other.mp4", 0); ok {
		t.Fatal("expected no upgrade for unrelated URL")
	}
	// List without resolution information is ignored.
	noRes := `{"episodes":[{"url":"https://x.example.com/1.mp4"},{"url":"https://x.example.com/2.mp4"}]}`
	if _, _, ok := betterVariantFromPageJSON([][]byte{[]byte(noRes)}, "https://x.example.com/1.mp4", 0); ok {
		t.Fatal("expected no upgrade without resolution data")
	}
}

func TestBestVariantPrefersH264AtSameSize(t *testing.T) {
	list := []streamVariant{
		{URL: "https://e.example.com/1", Width: 720, Height: 1280, Codec: "h265"},
		{URL: "https://e.example.com/2", Width: 720, Height: 1280, Codec: "h264"},
	}
	b, _ := bestVariant(list, 0)
	if b.URL != "https://e.example.com/2" {
		t.Fatalf("got %+v", b)
	}
}

func TestVariantFromQualityLabel(t *testing.T) {
	v, ok := variantFromObject(map[string]interface{}{"src": "https://e.example.com/v.m3u8", "label": "1080p"})
	if !ok || v.shortSide() != 1080 {
		t.Fatalf("got ok=%v v=%+v", ok, v)
	}
}

func TestUpgradeWithQualityLabelOnly(t *testing.T) {
	// Player data that names renditions only by "720p"/"1080p" (width/height null).
	body := []byte(`{"data":{"episode_no":3,"selected":{"quality":"720p","width":null,"height":null,"url":"https://cdn.example.com/oA?auth_key=1"},
		"streams":[{"quality":"720p","codec":"h264","width":null,"height":null,"url":"https://cdn.example.com/oA?auth_key=1"},
		{"quality":"1080p","codec":"h264","width":null,"height":null,"url":"https://cdn.example.com/oB?auth_key=1"}]}}`)
	best, played, ok := betterVariantFromPageJSON([][]byte{body}, "https://cdn.example.com/oA?auth_key=2", 0)
	if !ok || !strings.Contains(best.URL, "/oB") || played.shortSide() != 720 {
		t.Fatalf("best=%+v played=%+v ok=%v", best, played, ok)
	}
	// The 720p limit keeps the played rendition.
	if _, _, ok := betterVariantFromPageJSON([][]byte{body}, "https://cdn.example.com/oA", 720); ok {
		t.Fatal("720p limit must not upgrade")
	}
}

func TestBetterVariantComparesEveryList(t *testing.T) {
	old := []byte(`{"s":[{"url":"https://x.example/a.mp4","width":576,"height":1024},{"url":"https://x.example/b.mp4","width":1080,"height":1920}]}`)
	newer := []byte(`{"s":[{"url":"https://x.example/a.mp4","width":576,"height":1024},{"url":"https://x.example/c.mp4","width":360,"height":640}]}`)
	best, played, ok := betterVariantFromPageJSON([][]byte{old, newer}, "https://x.example/a.mp4", 0)
	if !ok || best.URL != "https://x.example/b.mp4" || played.URL != "https://x.example/a.mp4" {
		t.Fatalf("older response's larger variant missed: %+v %+v %v", best, played, ok)
	}
	// Two lists in one response (per codec): the same answer every time.
	one := []byte(`{"h264":[{"url":"https://x.example/a.mp4","width":576,"height":1024},{"url":"https://x.example/c.mp4","width":360,"height":640}],"h265":[{"url":"https://x.example/a.mp4","width":576,"height":1024},{"url":"https://x.example/b.mp4","width":1080,"height":1920}]}`)
	for i := 0; i < 50; i++ {
		best, _, ok := betterVariantFromPageJSON([][]byte{one}, "https://x.example/a.mp4", 0)
		if !ok || best.URL != "https://x.example/b.mp4" {
			t.Fatalf("run %d: %+v %v", i, best, ok)
		}
	}
	// Equal sizes in two lists: always the same (smaller) address.
	tie := []byte(`{"p":[{"url":"https://x.example/a.mp4","width":576,"height":1024},{"url":"https://x.example/z.mp4","width":1080,"height":1920}],"q":[{"url":"https://x.example/a.mp4","width":576,"height":1024},{"url":"https://x.example/m.mp4","width":1080,"height":1920}]}`)
	for i := 0; i < 50; i++ {
		if best, _, _ := betterVariantFromPageJSON([][]byte{tie}, "https://x.example/a.mp4", 0); best.URL != "https://x.example/m.mp4" {
			t.Fatalf("run %d: tie gave %s", i, best.URL)
		}
	}
}
