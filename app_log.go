//go:build windows

// Application log and status queues, task lifecycle, and shutdown.
package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (a *app) postLog(s string) {
	if s == "" || a.closing.Load() {
		return
	}
	a.recordLog(s)
	if a.job != nil {
		// The display mixes every task: tag each line with its task, and keep
		// the tagged copy for the window-wide 複製 Log / feedback as well.
		s = tagLines(s, fmt.Sprintf("[任務%d] ", a.job.ID))
		a.main.recordSession(s)
	}
	a.logMu.Lock()
	a.logPending = append(a.logPending, s)
	a.logPendingBytes += len(s)

	// If the UI is temporarily unable to drain output, retain the newest data
	// and release the oldest queued records once the pending queue exceeds
	// roughly 10 MiB. This prevents an unresponsive or resized window from
	// allowing Go-side memory to grow without bound.
	for a.logPendingBytes > maxPendingLogBytes && a.logHead < len(a.logPending) {
		a.logPendingBytes -= len(a.logPending[a.logHead])
		a.logPending[a.logHead] = ""
		a.logHead++
		a.logDropped++
	}
	a.logMu.Unlock()
	a.signalLogDrain()
}

func (a *app) queueLatestStatus(s string) {
	if a.closing.Load() {
		return
	}
	select {
	case a.statusQueue <- s:
	default:
		// Status text is transient. Drop one old entry so the newest progress
		// message remains visible instead of blocking the worker.
		select {
		case <-a.statusQueue:
		default:
		}
		select {
		case a.statusQueue <- s:
		default:
		}
	}
	a.signalStatusDrain()
}

func (a *app) postStatus(s string) {
	if a.job != nil {
		a.setJobStatus(strings.TrimPrefix(s, "狀態："))
		return
	}
	a.queueLatestStatus(s)
}

// tagLines puts tag in front of every non-empty line of s.
func tagLines(s, tag string) string {
	lines := strings.SplitAfter(s, "\n")
	var b strings.Builder
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			b.WriteString(tag)
		}
		b.WriteString(l)
	}
	return b.String()
}

func (a *app) postEnv(s string) {
	// Prefix lets UI distinguish environment status.
	a.queueLatestStatus("__ENV__" + s)
}

func (a *app) beginTask() context.Context {
	ctx, cancel := context.WithCancel(a.rootCtx)
	a.taskMu.Lock()
	if a.taskCancel != nil {
		a.taskCancel()
	}
	a.taskCancel = cancel
	a.taskMu.Unlock()
	return ctx
}

func (a *app) endTask() {
	a.taskMu.Lock()
	cancel := a.taskCancel
	a.taskCancel = nil
	a.taskMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *app) cancelCurrentTask() {
	a.taskMu.Lock()
	cancel := a.taskCancel
	a.taskMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *app) beginShutdown() {
	a.shutdownOnce.Do(func() {
		// Running tasks are saved as paused so they can be continued next time.
		a.pauseAllForShutdown()
		a.closing.Store(true)
		a.stopRequested.Store(true)
		a.cancelCurrentTask()
		if a.rootCancel != nil {
			a.rootCancel()
		}
		a.killCurrentProcessTree(3 * time.Second)
		a.stopAllWorkers()

		workersDone := make(chan struct{})
		go func() {
			a.workerWG.Wait()
			close(workersDone)
		}()
		select {
		case <-workersDone:
		case <-time.After(3 * time.Second):
		}
		if a.captureRunning.Load() {
			a.killAllCaptureProfileBrowsers()
		}

		a.logMu.Lock()
		a.logPending = nil
		a.logHead = 0
		a.logPendingBytes = 0
		a.logDropped = 0
		a.logMu.Unlock()
	})
}
