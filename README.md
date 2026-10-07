# AIXAI 萬能下載工具（AIXAI-DLA）

Windows 一鍵影音下載工具：免安裝 Python，首次使用自動準備 yt-dlp、FFmpeg、Deno；
對 yt-dlp 尚未支援的公開網頁，提供「瀏覽器播放擷取」作為備援。

> ⚠️ **只下載你依法有權保存或使用的內容。** 使用前請閱讀 [免責聲明與使用條款](DISCLAIMER.md)；程式第一次啟動時也會要求你閱讀並同意。

## 功能

- **yt-dlp 為主要引擎**，支援 yt-dlp 能處理的網站；失敗時自動檢查元件並重試。
- **瀏覽器播放擷取**：對需要按「播放」才載入串流的公開網頁，以專用瀏覽器在背景播放並擷取 HLS／DASH／MP4。
  - 透過私有管線（`--remote-debugging-pipe`）連線，不開放任何本機網路連接埠。
  - 背景執行、不搶滑鼠鍵盤；只有需要你登入或操作時才會顯示。
- **帳號安全模式**（預設開啟）：依網站分級隨機間隔、分批休息，偵測到限流或驗證立即停止整批。
- **多任務**：下載中可隨時加入新任務，可設定同時執行數；同一網站的任務自動排隊。每個任務可暫停、繼續、中止，關閉程式後未完成的任務會保留。
- 連續集數、接續下載、MP3 轉檔、格式選擇。
- **紀錄**：每次下載各自一個紀錄檔（下載資料夾內的「AIXAI_下載紀錄」資料夾），可一鍵複製；Cookie、密碼等敏感值自動遮蔽。
- **版本更新／退版**：程式內可更新或退回任一版本，SHA256 核對通過才替換。
- **意見回報**：可附截圖與已遮蔽敏感資訊的診斷資料。
- 淺色／深色主題跟隨 Windows，寬窄視窗自動調整。

## 使用方式

1. 到 [Releases](../../releases) 下載 `AIXAI_AllInOne_Downloader_<版本>_Windows_x64.exe`（需要 WebView2 Runtime，Windows 11 已內建）。
2. 第一次啟動請閱讀並同意使用條款。
3. 貼上網址、選擇處理方式與儲存位置，按「開始下載」（或 Ctrl+Enter）。
4. 首次執行會自動下載必要元件到 `%LOCALAPPDATA%\AIXAI-YTDLP-OneClick\bin`（不修改系統 PATH、不需系統管理員權限）。
5. 之後可在右上角「版本」直接更新。

## 帳號安全

網站可能限制或封鎖大量、自動化存取的帳號，處罰會落在「提供登入的那個帳號」上。
本工具預設開啟 **帳號安全模式**：

| 網站類型 | 間隔（隨機） | 分批休息 |
|---|---|---|
| 需要登入的頁面，或你已指定登入方式 | 15–45 秒 | 每 25 個休息 10–15 分鐘 |
| 風控較嚴格的網站 | 5–10 秒 | 每 100 個休息 10–15 分鐘 |
| 一般網站 | 2–5 秒 | — |

- 偵測到限流（HTTP 429）、「確認你不是機器人」、驗證碼等訊號時立即停止，不自動重試。
- 不會自動借用你日常瀏覽器中的登入。
- **需要登入的網站，請使用專用帳號，不要使用主帳號。**

## 免責聲明

> **使用前請務必閱讀完整的 [免責聲明與使用條款](DISCLAIMER.md)。**

- 本工具僅供下載**你依法有權保存或使用**的內容。
- **嚴禁**用於侵害著作權、規避或破解 DRM／付費牆／存取控制，或任何商業用途。
- 本工具**不破解 DRM**，**不提供、不儲存、不索引、不推薦**任何影音內容；本專案**不以營利為目的**。
- 使用者須自行遵守所在地法律及各網站服務條款，並自行承擔一切使用後果。
- 依「現狀」提供，不附任何擔保（MIT 授權）；與任何影音網站均無關聯。
- 權利人如有疑慮，請透過 [Issues](../../issues) 或回報信箱聯繫，我們會儘速處理。

## 回報問題與建議

程式右上角「回報」提供兩種管道：
- **GitHub Issues**（需 GitHub 帳號）：預先填好內容並開啟回報頁，截圖資料夾會同時開啟，拖曳即可上傳。
- **Email**（不需 GitHub 帳號）：以你自己的郵件程式或網頁信箱寄到 aixai19861201@gmail.com，截圖請手動附加。

## 從原始碼建置

需要 Go（測試版本 1.27.0，Go modules）：

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet .
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test .
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-H=windowsgui" -o AIXAI_AllInOne_Downloader_Windows_x64.exe .
```

- 介面原始碼在 `ui/index.html`（以 `go:embed` 打包進 exe）；設定 `AIXAI_UI_DEBUG=1` 可開啟 WebView2 開發者工具。
- 發佈版本請附上 `.exe`、完整版 `.zip` 與 `SHA256SUMS.txt`；程式內更新只安裝列在校驗檔中的執行檔。
- 更換 `APP_ICON.ico` 或修改 `main.go` 的 `appVersion` 後，以 `python make_icon_syso.py APP_ICON.ico rsrc_windows_amd64.syso --version <版本號>` 重新產生圖示與版本資訊資源。

## 第三方元件

見 [THIRD_PARTY_NOTICES.txt](THIRD_PARTY_NOTICES.txt)。yt-dlp、FFmpeg、Deno 等元件於執行時從其官方來源下載，**不隨本程式散布**。

## 版本紀錄

見 [CHANGELOG.md](CHANGELOG.md)。

## 授權

[MIT](LICENSE)
