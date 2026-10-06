//go:build windows

package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Fallback release source used when the GitHub API limit is reached. The
// public release pages are not counted against that limit:
//   - releases.atom lists the tags, dates and notes;
//   - each release's SHA256SUMS.txt (at a fixed download address) names its
//     files, so the executable's address is known without the API.
// Installing still requires the executable to be listed in SHA256SUMS.txt and
// to match it, exactly as with the API.

// updateWebBase is a variable only so tests can point it at a local server.
var updateWebBase = "https://github.com"

// webReleaseLimit caps how many releases (and SHA256SUMS.txt downloads) the
// fallback reads.
const webReleaseLimit = 15

type atomFeed struct {
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID      string    `xml:"id"`
	Title   string    `xml:"title"`
	Updated time.Time `xml:"updated"`
	Content string    `xml:"content"`
	Link    struct {
		Href string `xml:"href,attr"`
	} `xml:"link"`
}

func releaseDownloadURL(tag, name string) string {
	return fmt.Sprintf("%s/%s/%s/releases/download/%s/%s", updateWebBase, updateRepoOwner, updateRepoName, tag, name)
}

func fetchReleasesFromWeb(ctx context.Context) ([]ghRelease, error) {
	url := fmt.Sprintf("%s/%s/%s/releases.atom", updateWebBase, updateRepoOwner, updateRepoName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "AIXAI-AllInOne-Downloader/"+appVersion)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var feed atomFeed
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&feed); err != nil {
		return nil, err
	}
	if len(feed.Entries) > webReleaseLimit {
		feed.Entries = feed.Entries[:webReleaseLimit]
	}
	list := make([]ghRelease, len(feed.Entries))
	var wg sync.WaitGroup
	for i, e := range feed.Entries {
		tag := e.ID[strings.LastIndex(e.ID, "/")+1:]
		_, pre := versionParts(tag)
		list[i] = ghRelease{Tag: tag, Name: strings.TrimSpace(e.Title), Prerelease: pre != "",
			Published: e.Updated, Body: htmlToText(e.Content), HTMLURL: e.Link.Href}
		wg.Add(1)
		go func(r *ghRelease) {
			defer wg.Done()
			r.Assets = webReleaseAssets(ctx, r.Tag)
		}(&list[i])
	}
	wg.Wait()
	return list, nil
}

// webReleaseAssets returns the release's executable and SHA256SUMS.txt when
// the checksum file exists and lists an executable; otherwise nothing (the
// release is then shown as not installable).
func webReleaseAssets(ctx context.Context, tag string) []ghAsset {
	sumsURL := releaseDownloadURL(tag, checksumAsset)
	text, err := fetchText(ctx, sumsURL)
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if strings.HasSuffix(name, exeAssetSuffix) && !strings.ContainsAny(name, `/\`) {
			return []ghAsset{
				{Name: name, URL: releaseDownloadURL(tag, name)},
				{Name: checksumAsset, URL: sumsURL},
			}
		}
	}
	return nil
}

var (
	htmlBreakPattern = regexp.MustCompile(`(?i)<br\s*/?>|</(p|h[1-6]|li|ul|ol|blockquote|pre)>`)
	htmlItemPattern  = regexp.MustCompile(`(?i)<li[^>]*>`)
	htmlTagPattern   = regexp.MustCompile(`<[^>]+>`)
	blankRunPattern  = regexp.MustCompile(`\n{2,}`)
)

// htmlToText turns the feed's rendered release notes back into plain text.
func htmlToText(s string) string {
	s = htmlBreakPattern.ReplaceAllString(s, "\n")
	s = htmlItemPattern.ReplaceAllString(s, "- ")
	s = htmlTagPattern.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.TrimSpace(blankRunPattern.ReplaceAllString(strings.Join(lines, "\n"), "\n"))
}
