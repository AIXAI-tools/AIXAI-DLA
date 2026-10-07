# 隱私權政策（Privacy Policy）

AIXAI 萬能下載工具（AIXAI-DLA）**不收集、不上傳任何使用者資料**，沒有分析、追蹤或廣告功能。
本專案沒有自己的伺服器。

## 程式會連線的地方

| 時機 | 連線對象 | 傳送的內容 |
|---|---|---|
| 每次啟動（可在「版本」視窗關閉）、按「版本」 | GitHub（`api.github.com`，受限時改讀 `github.com` 的發佈頁） | 一般的 HTTPS 請求（含程式版本的 User-Agent），用來查詢是否有新版本 |
| 第一次使用或元件需要更新時 | 各元件的官方發佈來源（GitHub、`gyan.dev`） | 下載 yt-dlp、FFmpeg、Deno 等元件及其校驗檔 |
| 你按下「開始下載」 | 你輸入的網址所在的網站 | 由 yt-dlp 或內建瀏覽器存取該網址，與你自己用瀏覽器開啟相同 |
| 你按「回報」並選擇送出方式 | GitHub Issues 或你的郵件程式 | 只有你確認後的內容；程式只負責開啟頁面或郵件程式，不會自動送出 |

## 存在你電腦上的資料

- 設定、任務清單與登入用的專用瀏覽器資料：`%LOCALAPPDATA%\AIXAI-YTDLP-OneClick`
- 下載紀錄：下載資料夾內的「AIXAI_下載紀錄」資料夾（Cookie、密碼等敏感值會自動遮蔽）

這些資料只存在你的電腦上，程式不會上傳。

## 第三方元件

yt-dlp、FFmpeg、Deno 等元件，以及你造訪的網站，各自有其隱私權政策，本程式無法控制。

---

**English summary:** AIXAI-DLA collects no user data and has no telemetry. It connects only to GitHub (update
check at startup), the official sources of its components (yt-dlp, FFmpeg, Deno), the sites whose URLs you enter,
and — only when you choose to send feedback — GitHub Issues or your own mail client. Settings and logs stay on your PC.
