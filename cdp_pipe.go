//go:build windows

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

type cdpWireMessage struct {
	ID        int                    `json:"id,omitempty"`
	Method    string                 `json:"method,omitempty"`
	SessionID string                 `json:"sessionId,omitempty"`
	Params    map[string]interface{} `json:"params,omitempty"`
	Result    map[string]interface{} `json:"result,omitempty"`
	Error     map[string]interface{} `json:"error,omitempty"`
}

type cdpRequestState struct {
	URL             string
	RequestType     string
	Initiator       string
	RequestHeaders  map[string]string
	ResponseHeaders map[string]string
	MimeType        string
	Status          int
	SeenResponse    bool
	// Refused: a stream manifest the server refused to the browser (403 or a
	// failed request); see capture_referer.go.
	Refused bool
}

// cdpPipe is a private DevTools connection over two anonymous pipes
// (--remote-debugging-pipe). Unlike --remote-debugging-port no TCP port is
// opened, so no other local program can attach to the capture browser while
// it runs and read the TikTok login cookies kept in its profile.
// Messages are JSON objects terminated by a NUL byte.
type cdpPipe struct {
	w   *os.File
	rf  *os.File
	r   *bufio.Reader
	wmu sync.Mutex
}

func (p *cdpPipe) WriteJSON(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_, err = p.w.Write(append(data, 0))
	return err
}

func (p *cdpPipe) ReadMessage() ([]byte, error) {
	msg, err := p.r.ReadBytes(0)
	if err != nil {
		return nil, err
	}
	if len(msg) > 64<<20 {
		return nil, errors.New("DevTools 訊息異常過大")
	}
	return msg[:len(msg)-1], nil
}

func (p *cdpPipe) Close() {
	_ = p.w.Close()
	_ = p.rf.Close()
}

// startBrowserWithPipe launches the browser with DevTools bound to private
// pipes. On Windows the inheritable pipe handles are announced through
// --remote-debugging-io-pipes=<read>,<write> (verified with Edge 154).
func startBrowserWithPipe(path string, args []string, hidden bool) (*exec.Cmd, *cdpPipe, error) {
	toChildR, toChildW, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	fromChildR, fromChildW, err := os.Pipe()
	if err != nil {
		toChildR.Close()
		toChildW.Close()
		return nil, nil, err
	}
	pipeArgs := []string{
		"--remote-debugging-pipe",
		fmt.Sprintf("--remote-debugging-io-pipes=%d,%d", toChildR.Fd(), fromChildW.Fd()),
		// Edge relaunches itself when it sees a compatibility layer; the
		// relaunched copy does not get our pipe handles.
		"--edge-skip-compat-layer-relaunch",
	}
	cmd := exec.Command(path, append(pipeArgs, args...)...)
	cmd.Env = browserEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags:              CREATE_NEW_PROCESS_GROUP,
		HideWindow:                 hidden,
		AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(toChildR.Fd()), syscall.Handle(fromChildW.Fd())},
	}
	startErr := cmd.Start()
	if startErr == nil {
		bindToAppLifetime(cmd.Process.Pid) // the browser can never outlive the app
	}
	// The child owns its ends now; keep only ours.
	toChildR.Close()
	fromChildW.Close()
	if startErr != nil {
		toChildW.Close()
		fromChildR.Close()
		return nil, nil, startErr
	}
	return cmd, &cdpPipe{w: toChildW, rf: fromChildR, r: bufio.NewReaderSize(fromChildR, 1<<20)}, nil
}

// browserEnv is the app environment without __COMPAT_LAYER, which Windows adds
// when the app was started from Explorer with a compatibility record.
func browserEnv() []string {
	env := os.Environ()
	out := env[:0:0]
	for _, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), "__COMPAT_LAYER=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func cdpCommand(id int, method string, params map[string]interface{}, sessionID string) map[string]interface{} {
	m := map[string]interface{}{"id": id, "method": method}
	if params != nil {
		m["params"] = params
	}
	if sessionID != "" {
		m["sessionId"] = sessionID
	}
	return m
}
