//go:build windows

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v4.0.0", "4.0.0", 0},
		{"v4.0.1", "v4.0.0", 1},
		{"v3.9.0", "v4.0.0", -1},
		{"v3.10.0", "v3.9.0", 1}, // numeric, not lexical
		{"v4.1.0-beta.1", "v4.1.0", -1},
		{"v4.1.0-beta.2", "v4.1.0-beta.1", 1},
		{"v4.1.0-beta.1", "v4.0.0", 1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func fakeReleases() []ghRelease {
	now := time.Now()
	exe := func(v string) []ghAsset {
		return []ghAsset{{Name: "AIXAI_AllInOne_Downloader_" + v + "_Windows_x64.exe", URL: "https://example/x.exe"}, {Name: checksumAsset, URL: "https://example/sums"}}
	}
	return []ghRelease{
		{Tag: "v4.1.0-beta.1", Prerelease: true, Published: now, Assets: exe("v4_1_0")},
		{Tag: "v4.0.1", Published: now, Assets: exe("v4_0_1")},
		{Tag: "v4.0.0", Published: now, Assets: exe("v4_0_0")},
		{Tag: "v3.9.0", Published: now, Assets: []ghAsset{{Name: "notes.txt"}}}, // no exe: not installable
		{Tag: "v3.8.0", Published: now, Assets: exe("v3_8_0")},
		{Tag: "v9.9.9", Draft: true},
	}
}

func TestBuildUIReleases(t *testing.T) {
	list := fakeReleases()
	var visible []ghRelease
	for _, r := range list {
		if !r.Draft {
			visible = append(visible, r)
		}
	}
	got := buildUIReleasesFor(visible, "4.0.0")
	if got.LatestStable != "v4.0.1" {
		t.Errorf("latest stable = %s, want v4.0.1 (betas must not count)", got.LatestStable)
	}
	byTag := map[string]uiRelease{}
	for _, it := range got.Items {
		byTag[it.Tag] = it
	}
	if !byTag["v4.0.1"].Newer || byTag["v4.0.0"].Newer || !byTag["v4.0.0"].Current {
		t.Errorf("newer/current flags wrong: %+v", byTag)
	}
	if byTag["v3.9.0"].Installable {
		t.Error("a release without exe+SHA256SUMS must not be installable")
	}
	if byTag["v4.1.0-beta.1"].Channel != "beta" || byTag["v3.8.0"].Channel != "stable" {
		t.Error("channels wrong")
	}
}

func TestFetchReleasesFromAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/"+updateRepoOwner+"/"+updateRepoName+"/releases" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(fakeReleases())
	}))
	defer srv.Close()
	old := updateAPIBase
	updateAPIBase = srv.URL
	defer func() { updateAPIBase = old }()

	list, err := fetchReleases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range list {
		if r.Draft {
			t.Error("drafts must be filtered out")
		}
	}
	if len(list) != 5 {
		t.Errorf("got %d releases, want 5", len(list))
	}

	// Private / missing repository → friendly message, not a crash.
	updateAPIBase = srv.URL + "/nothing-here"
	if _, err := fetchReleases(context.Background()); err == nil || !strings.Contains(err.Error(), "尚未公開") {
		t.Errorf("want friendly not-public error, got %v", err)
	}
}

func TestChecksumGuardsDownload(t *testing.T) {
	payload := []byte("pretend this is the new exe")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(payload) }))
	defer srv.Close()
	dir := t.TempDir()
	dest := dir + `\new.exe`
	var last int
	if err := downloadWithProgress(context.Background(), srv.URL, dest, int64(len(payload)), func(p int) { last = p }); err != nil {
		t.Fatal(err)
	}
	if last != 100 {
		t.Errorf("progress ended at %d%%", last)
	}
	got, _ := fileSHA256(dest)
	sums := got + "  AIXAI_AllInOne_Downloader_v4_0_1_Windows_x64.exe\n" + strings.Repeat("0", 64) + "  AIXAI_AllInOne_Downloader_v4_0_1_Windows_x64_Full.zip\n"
	want, err := parseOfficialSHA256(sums, "AIXAI_AllInOne_Downloader_v4_0_1_Windows_x64.exe")
	if err != nil || want != got {
		t.Fatalf("checksum lookup picked the wrong line: %s vs %s (%v)", want, got, err)
	}
	_ = os.WriteFile(dest, []byte("tampered"), 0644)
	if tampered, _ := fileSHA256(dest); tampered == want {
		t.Fatal("tampered file must not match")
	}
}
