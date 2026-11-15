//go:build windows

package main

import (
	"strings"
)

func (capture *cdpCapture) notifyChallenge(u string) {
	if isTurnstileURL(u) {
		select {
		case capture.challengeCh <- struct{}{}:
		default:
		}
	}
}

func (capture *cdpCapture) emitFor(requestID string) {
	capture.stateMu.Lock()
	st := capture.states[requestID]
	if st == nil || !st.SeenResponse || st.URL == "" || (st.Status >= 400 && !st.Refused) {
		capture.stateMu.Unlock()
		return
	}
	reqHeaders := map[string]string{}
	for k, v := range st.RequestHeaders {
		reqHeaders[k] = v
	}
	respHeaders := map[string]string{}
	for k, v := range st.ResponseHeaders {
		respHeaders[k] = v
	}
	if captureHeader(respHeaders, "content-type") == "" && st.MimeType != "" {
		respHeaders["content-type"] = st.MimeType
	}
	ev := browserCaptureEvent{Kind: "cdp", URL: st.URL, PageURL: capture.rawURL, RequestType: st.RequestType, Initiator: st.Initiator, RequestHeaders: reqHeaders, ResponseHeaders: respHeaders, Refused: st.Refused}
	capture.stateMu.Unlock()
	if c, ok := mediaCandidateScore(ev); ok {
		select {
		case capture.candidates <- c:
		default:
		}
	}
}

func (capture *cdpCapture) handler(msg cdpWireMessage) {
	p := msg.Params
	reqID := cdpString(p, "requestId")
	if reqID == "" {
		return
	}
	switch msg.Method {
	case "Network.requestWillBeSent":
		capture.onRequestWillBeSent(p, reqID)
	case "Network.requestWillBeSentExtraInfo":
		capture.onRequestWillBeSentExtraInfo(p, reqID)
	case "Network.responseReceived":
		capture.onResponseReceived(p, reqID)
	case "Network.responseReceivedExtraInfo":
		capture.onResponseReceivedExtraInfo(p, reqID)
	case "Network.loadingFailed":
		capture.onLoadingFailed(p, reqID)
	case "Network.loadingFinished":
		capture.onLoadingFinished(p, reqID)
	}
}

func (capture *cdpCapture) onRequestWillBeSent(p map[string]interface{}, reqID string) {
	req, _ := p["request"].(map[string]interface{})
	initiator, _ := p["initiator"].(map[string]interface{})
	capture.stateMu.Lock()
	st := capture.states[reqID]
	if st == nil {
		st = &cdpRequestState{}
		capture.states[reqID] = st
	}
	newURL := cdpString(req, "url")
	if _, redirected := p["redirectResponse"]; redirected && st.URL != "" {
		// The same request continues at another address.
		from, to := adURLKey(st.URL), adURLKey(newURL)
		if from != to && len(capture.redirectFrom[to]) < 16 {
			capture.redirectFrom[to] = append(capture.redirectFrom[to], append([]string{from}, capture.redirectFrom[from]...)...)
		}
	}
	st.URL = newURL
	st.RequestType = cdpString(p, "type")
	st.Initiator = cdpString(initiator, "url")
	st.RequestHeaders = mergeStringMaps(st.RequestHeaders, cdpMapString(req["headers"]))
	capture.stateMu.Unlock()
	capture.notifyChallenge(newURL)
	if capture.tikDiag != nil {
		capture.tikDiag.request(cdpString(p, "type"), newURL)
	}
}

func (capture *cdpCapture) onRequestWillBeSentExtraInfo(p map[string]interface{}, reqID string) {
	capture.stateMu.Lock()
	st := capture.states[reqID]
	if st == nil {
		st = &cdpRequestState{}
		capture.states[reqID] = st
	}
	st.RequestHeaders = mergeStringMaps(st.RequestHeaders, cdpMapString(p["headers"]))
	capture.stateMu.Unlock()
	capture.emitFor(reqID)
}

func (capture *cdpCapture) onResponseReceived(p map[string]interface{}, reqID string) {
	resp, _ := p["response"].(map[string]interface{})
	capture.stateMu.Lock()
	st := capture.states[reqID]
	if st == nil {
		st = &cdpRequestState{}
		capture.states[reqID] = st
	}
	if u := cdpString(resp, "url"); u != "" {
		st.URL = u
	}
	if typ := cdpString(p, "type"); typ != "" {
		st.RequestType = typ
	}
	st.MimeType = cdpString(resp, "mimeType")
	st.Status = int(cdpFloat(resp, "status"))
	st.ResponseHeaders = mergeStringMaps(st.ResponseHeaders, cdpMapString(resp["headers"]))
	st.SeenResponse = true
	st.Refused = st.Status == 403 && isManifestURL(st.URL)
	if st.Status > 0 && st.Status < 400 && isSubtitleResponse(st.URL, st.MimeType) && !capture.subSeen[st.URL] && len(capture.subFiles) <= maxCaptureSubtitleFiles {
		capture.subSeen[st.URL] = true
		capture.subFiles = append(capture.subFiles, st.URL)
	}
	kind, status, respURL := st.RequestType, st.Status, st.URL
	capture.stateMu.Unlock()
	capture.notifyChallenge(respURL)
	if capture.tikDiag != nil {
		fromSW, _ := resp["fromServiceWorker"].(bool)
		capture.tikDiag.response(kind, status, fromSW)
	}
	capture.emitFor(reqID)
}

func (capture *cdpCapture) onResponseReceivedExtraInfo(p map[string]interface{}, reqID string) {
	capture.stateMu.Lock()
	st := capture.states[reqID]
	if st == nil {
		st = &cdpRequestState{}
		capture.states[reqID] = st
	}
	st.ResponseHeaders = mergeStringMaps(st.ResponseHeaders, cdpMapString(p["headers"]))
	capture.stateMu.Unlock()
	capture.emitFor(reqID)
}

func (capture *cdpCapture) onLoadingFailed(p map[string]interface{}, reqID string) {
	// A manifest blocked before any response (e.g. a refused cross-site
	// request) is still the address the player wanted.
	capture.stateMu.Lock()
	st := capture.states[reqID]
	refused := st != nil && !st.SeenResponse && isManifestURL(st.URL)
	if refused {
		st.SeenResponse, st.Refused = true, true
	}
	capture.stateMu.Unlock()
	if refused {
		capture.emitFor(reqID)
	}
}

func (capture *cdpCapture) onLoadingFinished(p map[string]interface{}, reqID string) {
	if !capture.isTikTokDrama {
		capture.stateMu.Lock()
		st := capture.states[reqID]
		keep := st != nil && st.Status == 200 && strings.Contains(strings.ToLower(st.MimeType), "json") &&
			(st.RequestType == "XHR" || st.RequestType == "Fetch") && cdpFloat(p, "encodedDataLength") <= maxPageJSONBytes
		vast := st != nil && st.Status == 200 && (st.RequestType == "XHR" || st.RequestType == "Fetch") &&
			(strings.Contains(strings.ToLower(st.MimeType), "xml") || strings.Contains(strings.ToLower(st.URL), "vast")) &&
			cdpFloat(p, "encodedDataLength") <= maxPageJSONBytes
		// The first document is the page itself (frames come later).
		doc := capture.sharedAddress && !capture.docRead && st != nil && st.Status == 200 && st.RequestType == "Document"
		var docURL string
		if doc {
			capture.docRead, docURL = true, st.URL
		}
		hlsKey := st != nil && st.Status == 200 && (st.RequestType == "XHR" || st.RequestType == "Fetch") &&
			mayBeKeyResponse(st.MimeType) && cdpFloat(p, "encodedDataLength") <= maxKeyResponseBytes
		var keyURL string
		if hlsKey {
			keyURL = st.URL
		}
		manifest := st != nil && st.Status == 200 && isManifestURL(st.URL)
		capture.stateMu.Unlock()
		if hlsKey {
			go capture.keepHLSKey(reqID, keyURL)
		}
		if manifest {
			go capture.readManifest(reqID)
		}
		if doc {
			capture.pageJSONReads.Add(1)
			go capture.readStaticHTML(reqID, docURL)
		}
		if keep {
			capture.pageJSONReads.Add(1)
			go capture.keepPageJSON(reqID)
		}
		if vast {
			go capture.inspectVAST(reqID)
		}
		return
	}
	capture.stateMu.Lock()
	st := capture.states[reqID]
	if st != nil && isTikTokAPIURL(st.URL) {
		capture.tikDiag.api(st.URL, st.Status)
	}
	ok := st != nil && st.Status == 200 && isTikTokDramaAPI(st.URL)
	var apiURL string
	reqHeaders := map[string]string{}
	if ok {
		capture.tikDiag.knownAPI()
		apiURL = st.URL
		for k, v := range st.RequestHeaders {
			reqHeaders[k] = v
		}
	}
	capture.stateMu.Unlock()
	if ok {
		go capture.inspectTikTokAPI(reqID, apiURL, reqHeaders)
	}
}
