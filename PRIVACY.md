# 隱私權政策（Privacy Policy）

AIXAI 萬能下載工具（AIXAI-DLA）**不收集任何使用者資料**，沒有分析、追蹤或廣告功能。
只有在你按下回報視窗的「直接送出」時，才會把你確認過的回報內容送給開發者（見下表）。

## 程式會連線的地方

| 時機 | 連線對象 | 傳送的內容 |
|---|---|---|
| 每次啟動與程式開著時每 6 小時（可在「版本」視窗關閉）、按「版本」 | GitHub（`api.github.com`，受限時改讀 `github.com` 的發佈頁） | 一般的 HTTPS 請求（含程式版本的 User-Agent），用來查詢是否有新版本 |
| 第一次使用或元件需要更新時 | 各元件的官方發佈來源（GitHub、`gyan.dev`） | 下載 yt-dlp、FFmpeg、Deno 等元件及其校驗檔 |
| 你按下「開始下載」 | 你輸入的網址所在的網站 | 由 yt-dlp 或內建瀏覽器存取該網址，與你自己用瀏覽器開啟相同 |
| 你在回報視窗按「直接送出」 | 開發者的 Google Apps Script 回報收件程式（`script.google.com`），由它以 Email 轉寄給開發者，並保存一份在開發者的 Google 帳號（Google Drive，不公開）以便追蹤處理 | 你填寫的說明與聯絡方式、勾選時的診斷資訊（版本、系統、最近的紀錄），以及你留在附件區的截圖與紀錄檔；紀錄中的 Cookie、密碼與使用者資料夾路徑已遮蔽。不會公開，只用於處理回報 |
| 「直接送出」時勾選了「接收開發者的回覆」（預設勾選）之後：啟動時，以及程式開著時（送出後 24 小時內每 1 小時、之後約每 6 小時），最多 60 天，開發者回覆並結案後停止 | 同上的回報收件程式 | 只有該份回報的編號（送出時隨機產生，不含任何個人資訊）與程式版本的 User-Agent，用來查詢開發者是否回覆（例如「已修正，請更新」「請幫忙測試」）；沒有勾選就不會查詢 |
| 你在回報視窗按「透過 GitHub 回報」 | GitHub Issues | 只有你確認後的內容；程式只負責開啟已填好的頁面，由你自己按送出，內容會公開 |

## 存在你電腦上的資料

- 設定、任務清單、「無法下載的網址」清單與登入用的專用瀏覽器資料：`%LOCALAPPDATA%\AIXAI-YTDLP-OneClick`
  （在專用瀏覽器登入的網站，下載時會把該網站的登入 Cookie 暫時寫入同一資料夾交給 yt-dlp，下載結束立即刪除）
- 下載紀錄、回報包、程式紀錄與當機紀錄（程式沒有正常關閉時的判斷與錯誤內容，最近 7 天內有新內容時，回報會自動附上）：「文件\AIXAI 萬能下載工具」（可在進階設定更改；Cookie、密碼等敏感值會自動遮蔽）。如果你的「文件」資料夾由 OneDrive 等雲端服務同步，這些紀錄也會跟著同步
- 程式執行中的狀態標記（版本、開啟時間、最後確認執行中的時間，用來判斷上次是否正常關閉）：上述程式資料夾的 `sessions`，正常關閉時自動移除
- 歷史下載清單（下載過的網址、標題、時間、存放位置）：存在上述程式資料夾，可在「清單 ▾ → 歷史下載清單」刪除或全部清除
- 等待回覆的回報（回報編號、說明的第一行、送出時間，以及送出當下「無法下載的網址」清單，用於「重試當時失敗的網址」）：存在上述程式資料夾的 `feedback_replies.json`，60 天沒有新動態自動移除；網址清單不會送出

這些資料只存在你的電腦上，程式不會上傳。

## 第三方元件

yt-dlp、FFmpeg、Deno 等元件，以及你造訪的網站，各自有其隱私權政策，本程式無法控制。

---

**English summary:** AIXAI-DLA collects no user data and has no telemetry. It connects only to GitHub (update
check at startup and every 6 hours while running), the official sources of its components (yt-dlp, FFmpeg, Deno), the sites whose URLs you enter,
and — only when you choose to send feedback — GitHub Issues, or (when you press "直接送出") the developer's Google Apps
Script relay, which e-mails the report you reviewed (text, optional diagnostics, and the screenshots/logs you attached,
with secrets and user paths masked) to the developer and keeps a private copy in the developer's Google Drive for
follow-up. If you keep "接收開發者的回覆" checked, the program later asks the same relay for replies using only the
report's random ID (at startup and every 6 hours while running, for up to 60 days). Nothing else leaves your PC.
