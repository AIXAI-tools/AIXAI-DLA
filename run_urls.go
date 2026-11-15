//go:build windows

// URL download orchestration and download error classification.
package main

import (
	"context"
	"errors"
	"fmt"
)

func (a *app) runURLs(ctx context.Context, urls []string, mode int, formatID, output string, cfg settings) error {
	mode = subtitlesOnlyMode(mode, cfg)
	batchFailures := make([]string, 0)
	safe := !cfg.UnsafeMode
	// The capture browser is shared by all tasks and closed by the scheduler
	// when the last running task ends (jobs.go).
	a.captureStartFailed.Store(false)
	a.loginCancelled.Store(false)
	pacer := newSafePacer()
	episodes := a.savedEpisodeBatch()
	envRefreshed := false
	unsupportedHosts := map[string]bool{}
	a.seqHashes = nil
	if cfg.Sequence {
		a.seqHashes = a.savedSeqFiles()
	}
	defer func() { a.seqHashes, a.diag = nil, nil }()
	if safe {
		a.postLog(safeModeIntro(urls, cfg))
	} else {
		a.postLog("⚠ 帳號安全模式已關閉：不加間隔、失敗會自動重試。大量下載需要登入的網站時，帳號可能被限制或封鎖。\r\n")
	}
	// order is the visiting order: items whose episode the site withholds are
	// appended again at the end (deferItem), so the episodes the page gives
	// directly are saved first and the ones needing the user come together.
	order := make([]int, len(urls))
	for k := range order {
		order[k] = k
	}
	deferred := map[int]bool{} // items put off and not yet visited again
	visited := make([]bool, len(urls))
	defer func() { a.deferWithheld = false }()
	r := &urlRun{
		a:                a,
		ctx:              ctx,
		urls:             urls,
		mode:             mode,
		formatID:         formatID,
		output:           output,
		cfg:              cfg,
		safe:             safe,
		pacer:            pacer,
		episodes:         episodes,
		envRefreshed:     envRefreshed,
		unsupportedHosts: unsupportedHosts,
		order:            order,
		deferred:         deferred,
		visited:          visited,
		batchFailures:    batchFailures,
	}
	deferItem := r.deferItem
	for qi := 0; qi < len(order); qi++ {
		i, rawURL := order[qi], urls[order[qi]]
		a.deferWithheld = !visited[i] && qi < len(order)-1
		visited[i] = true
		delete(deferred, i)
		// Finished items are not redone by 繼續下載; an item put off to the
		// end keeps the resume point at or before it.
		pos := i
		for k := range deferred {
			if k < pos {
				pos = k
			}
		}
		a.runPos.Store(int32(pos))
		if a.stopRequested.Load() {
			return nil
		}
		if safe && !pacer.beforeItem(ctx, a, i, rawURL, cfg) {
			return nil
		}
		cont, done, err := r.runItem(i, rawURL)
		order, batchFailures = r.order, r.batchFailures
		if cont {
			continue
		}
		if done {
			return err
		}
		if a.stopRequested.Load() {
			return nil
		}
		if deferItem(i, err) {
			order = r.order
			continue
		}
		a.diagFinishItem(err)
		if err != nil {
			if safe && isBlockSignal(err) {
				return blockStopError(err)
			}
			if a.captureStartFailed.Load() {
				return captureStartStopError(err)
			}
			if a.loginCancelled.Load() {
				return &commandRunError{Cause: err, Summary: "已關閉登入視窗、取消登入，因此停止整批下載。需要時重新按「開始下載」即可再次登入。"}
			}
			// One bad address must not hold back the rest of the batch (繼續
			// would otherwise restart at the same failing item every time).
			if len(urls) == 1 {
				a.emitTask(i, "failed", firstLine(err.Error()))
				return fmt.Errorf("第 %d 個任務失敗：%w", i+1, err)
			}
			msg := fmt.Sprintf("第 %d/%d 項失敗：%s", i+1, len(urls), firstLine(err.Error()))
			batchFailures = append(batchFailures, msg)
			r.batchFailures = batchFailures
			a.postLog("⚠ " + msg + "；跳過此項，繼續下一項。\r\n")
			a.emitTask(i, "failed", firstLine(err.Error()))
			continue
		}
		a.postLog("✓ 本項任務完成。\r\n")
		doneMsg := ""
		if cfg.SubtitlesOnly {
			doneMsg = subtitleDoneMessage(a.itemSubs)
		}
		a.emitTask(i, "done", doneMsg)
	}
	if len(batchFailures) > 0 {
		return &batchPartialError{Failures: batchFailures}
	}
	return nil
}

func (r *urlRun) deferItem(i int, err error) bool {
	a, deferred, order := r.a, r.deferred, r.order
	var d *errEpisodeDeferred
	if !errors.As(err, &d) || a.stopRequested.Load() {
		return false
	}
	deferred[i] = true
	order = append(order, i)
	r.order = order
	a.diag = nil
	a.postLog(fmt.Sprintf("ℹ 網站要求先在播放畫面解鎖第 %d 集，先下載其他集數，最後再一起處理（屆時會打開瀏覽器視窗並提醒您）。\r\n", d.ep))
	a.emitTask(i, "pending", "需要在網站上解鎖，排到最後處理")
	return true
}
