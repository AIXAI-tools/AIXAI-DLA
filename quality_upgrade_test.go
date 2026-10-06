package main

import "testing"

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
