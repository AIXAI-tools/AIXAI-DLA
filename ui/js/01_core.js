"use strict";
const $ = id => document.getElementById(id);
const state = { init:null, jobs:{}, openJobs:new Set(), foldJobs:new Set(), kind:"bug", images:[], logs:[], logsRemoved:new Set(), snipping:false, releases:null, saveTimer:0 };
/* 版面偏好（左右寬度、區塊高度、收合、主題）：頁面不能用 localStorage，改存程式的 ui_state.json（ui_state.go） */
let uiState={}, uiSaveTimer=0;
function uiSet(k,v){ if(v==null) delete uiState[k]; else uiState[k]=v;
  clearTimeout(uiSaveTimer); uiSaveTimer=setTimeout(()=>{ try { if(window.goSaveUIState) goSaveUIState(JSON.stringify(uiState)); } catch {} },300); }

/* ---------------- theme ---------------- */
const themeOrder = ["system","light","dark"];
function getThemePref(){ return uiState.theme || "system"; }
function applyTheme(){
  const pref = getThemePref();
  const dark = pref === "dark" || (pref === "system" && matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.dataset.theme = dark ? "dark" : "light";
  $("btnTheme").title = "主題：" + {system:"跟隨系統",light:"淺色",dark:"深色"}[pref] + "（點擊切換）";
  window.goSetTheme && goSetTheme(dark);
}
$("btnTheme").onclick = () => {
  const next = themeOrder[(themeOrder.indexOf(getThemePref()) + 1) % themeOrder.length];
  uiSet("theme", next==="system"?null:next);
  applyTheme(); toast("主題：" + {system:"跟隨系統",light:"淺色",dark:"深色"}[next]);
};
matchMedia("(prefers-color-scheme: dark)").addEventListener("change", applyTheme);

/* ---------------- helpers ---------------- */
let toastTimer = 0;
function toast(msg){ const t=$("toast"); t.textContent=msg; t.classList.add("show"); clearTimeout(toastTimer); toastTimer=setTimeout(()=>t.classList.remove("show"),2600); }
function fieldError(field,msg){
  document.querySelectorAll(".invalid").forEach(e=>e.classList.remove("invalid"));
  document.querySelectorAll(".err-text").forEach(e=>{e.classList.remove("show");e.textContent="";});
  if(!field) return;
  const box=$("err-"+field); if(box){box.textContent=msg;box.classList.add("show");}
  const input={urls:"urls",sequence:"seqStart",fileName:"fileName",formatId:"formatId",output:"output",cookieFile:"cookieFile",fbText:"fbText"}[field];
  if(input){ $(input).classList.add("invalid"); $(input).focus(); }
  if(field==="cookieFile") $("adv").open=true;
}
function esc(s){ return s.replace(/[&<>"]/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;"}[c])); }

/* ---------------- settings form ---------------- */
function collect(){
  return {
    urls:$("urls").value, mode:+$("mode").value, formatId:$("formatId").value, output:$("output").value.trim(),
    subtitlesOnly:$("subsOnly").checked,
    sequence:$("sequence").checked, sequenceStart:+$("seqStart").value||0, sequenceEnd:+$("seqEnd").value||0,
    seriesName:$("seriesName").value.trim(), fileName:$("fileName").disabled?"":$("fileName").value.trim(), episodeFormat:$("epFormat").value, episodeSep:$("epSep").value,
    safe:$("safe").checked, noPlaylist:$("noPlaylist").checked, playlistFolder:$("playlistFolder").checked, convertH264:$("convertH264").checked,
    extraArgs:$("extra").value, cookieBrowser:$("login").value, cookieFile:$("cookieFile").value, captureBrowser:$("capBrowser").value, advancedOpen:$("adv").open,
    maxJobs:+$("maxJobs").value||0, dataFolder:$("dataFolder").value.trim()
  };
}
function saveSoon(){ clearTimeout(state.saveTimer); state.saveTimer=setTimeout(()=>window.goSaveSettings&&goSaveSettings(collect()),400); }
function refreshDependent(){
  const subsOnly=$("subsOnly").checked;
  $("mode").disabled=subsOnly;
  $("modeBox").classList.toggle("dim", subsOnly);
  $("formatBox").classList.toggle("hidden", subsOnly || $("mode").value !== "4");
  $("subsOnlyHint").classList.toggle("hidden", !subsOnly);
  $("goLabel").textContent=subsOnly ? "開始下載字幕" : "開始下載";
  $("cookieBox").classList.toggle("hidden", $("login").value !== "cookiefile");
  const parts=[];
  const lo=$("login"); if(lo.value!=="auto") parts.push(lo.options[lo.selectedIndex].text.replace("…",""));
  const cb=$("capBrowser"); if(cb.value && cb.value!=="default" && cb.selectedIndex>=0) parts.push("擷取："+cb.options[cb.selectedIndex].text);
  if($("extra").value.trim()) parts.push("參數："+$("extra").value.trim());
  $("advSum").textContent = parts.length ? "· "+parts.join("　") : "";
  $("seqBox").style.opacity = $("sequence").checked ? 1 : .75;
  $("seqNameBox").classList.toggle("hidden", !$("sequence").checked);
  // 自訂檔名與連續集數的劇名放在同一處：勾選連續集數就換成劇名設定
  $("fileNameBox").classList.toggle("hidden", $("sequence").checked);
  const many=urlCount()>1, fn=$("fileName");
  fn.disabled=many;
  fn.placeholder=many?"自訂檔名只能用在單一網址":"自訂檔名（留空＝用影片標題）";
  $("epSep").disabled = !$("epFormat").value;
  $("epPreview").textContent = "檔名範例："+episodeName(+$("seqStart").value||1)+(subsOnly?".srt":".mp4");
}
function urlCount(){ return $("urls").value.split(/\r?\n/).filter(l=>l.trim() && !l.trim().startsWith("#")).length; }
const EP_FORMATS={zh:n=>`第${n}集`,E:n=>`E${n}`,EP:n=>`EP${n}`,Episode:n=>`Episode ${n}`};
const EP_SEPS={none:"",space:" ",dash:"-",underscore:"_",space_dash:" - ",space_underscore:" _ "};
// 與 episode_naming.go 相同的規則（只用來顯示範例）
function episodeName(ep){
  const name=$("seriesName").value.trim()||"（網頁標題）", n=String(ep).padStart(3,"0"), f=EP_FORMATS[$("epFormat").value];
  if(!f) return `${name}_E${n}`;
  const sep=EP_SEPS[$("epSep").value]; return name+(sep==null?" ":sep)+f(n);
}
["subsOnly","mode","login","capBrowser","sequence","seqStart","seqEnd","epFormat","epSep","safe","noPlaylist","playlistFolder","convertH264","extra","output","formatId","maxJobs"].forEach(id=>{
  $(id).addEventListener("input",()=>{refreshDependent();saveSoon();});
  $(id).addEventListener("change",()=>{refreshDependent();saveSoon();});
});
$("adv").addEventListener("toggle",saveSoon);
$("seriesName").addEventListener("input",refreshDependent);
$("urls").addEventListener("input",refreshDependent);
$("btnBrowse").onclick = async()=>{ const p=await goPickFolder($("output").value); if(p){$("output").value=p;saveSoon();} };
$("btnOpenFolder").onclick = async()=>{ const e=await goOpenFolder($("output").value); if(e) toast(e); };
$("btnCookie").onclick = async()=>{ const p=await goPickCookieFile(); if(p){$("cookieFile").value=p;saveSoon();} };
$("btnResume").onclick = async()=>{ const dir=await goPickResumeFile($("output").value); if(dir){$("output").value=dir;$("resumeInfo").textContent="✓ 已選擇接續檔，請貼上原本的網址後開始。";saveSoon();} };

function fillForm(init){
  const s=init.settings;
  $("appTitle").textContent=init.title; $("ver").textContent="v"+init.version; $("curVer").textContent="v"+init.version; $("startupUpdCheck").checked=!init.settings.no_startup_update_check;
  document.title=init.title;
  $("mode").innerHTML=init.modes.map(m=>`<option value="${m.value}" title="${esc(m.hint||"")}">${esc(m.label)}</option>`).join("");
  $("capBrowser").innerHTML=(init.browsers||[]).map(m=>`<option value="${m.value}" title="${esc(m.hint||"")}">${esc(m.label)}</option>`).join("");
  $("login").innerHTML=init.logins.map(m=>`<option value="${m.value}">${esc(m.label)}</option>`).join("");
  $("chips").innerHTML=init.presets.map((p,i)=>`<button class="chip" data-i="${i}" title="${esc(p.value)}">${esc(p.hint||p.label)}</button>`).join("");
  $("chips").onclick=e=>{
    const b=e.target.closest(".chip"); if(!b) return;
    const arg=init.presets[+b.dataset.i].value; const cur=$("extra").value.trim();
    if((" "+cur+" ").includes(" "+arg+" ")){toast("這個參數已經加入了");return;}
    $("extra").value=(cur?cur+" ":"")+arg; refreshDependent(); saveSoon(); toast("已加入「"+arg+"」："+(init.presets[+b.dataset.i].hint||""));
  };
  $("mode").value=String(s.mode||0); $("output").value=s.output_folder||"";
  $("sequence").checked=!!s.sequence; $("seqStart").value=s.sequence_start||1; $("seqEnd").value=s.sequence_end||50;
  $("epFormat").value=s.episode_format||""; if($("epFormat").selectedIndex<0) $("epFormat").value="";
  $("epSep").value=s.episode_sep||"space"; if($("epSep").selectedIndex<0) $("epSep").value="space";
  $("safe").checked=!s.unsafe_mode; $("subsOnly").checked=!!s.subtitles_only; $("noPlaylist").checked=!!s.no_playlist; $("playlistFolder").checked=!!s.playlist_folder; $("convertH264").checked=!!s.convert_h264;
  $("extra").value=s.extra_args||"";
  if(s.cookie_file){$("login").value="cookiefile";$("cookieFile").value=s.cookie_file;} else {$("login").value=s.cookie_browser||"auto";}
  $("capBrowser").value=s.capture_browser||"default"; if($("capBrowser").selectedIndex<0) $("capBrowser").value="default";
  $("adv").open=!!s.advanced_open;
  $("maxJobs").max=init.maxLimit||8; $("maxJobs").value=s.max_jobs||2;
  setJobs(init.jobs||[]);
  setFailed(init.failed||[]);
  $("dataFolder").value=s.data_folder||""; $("dataFolder").placeholder=init.dataDefault||"";
  $("fbWhere").innerHTML = (init.directSend
      ? `<b>直接送出</b>：不需要登入，說明、聯絡方式、診斷資訊與上方列出的截圖／紀錄檔會以 Email 寄給開發者，並保存一份在開發者的 Google 帳號以便追蹤處理（不公開）。<br>`
      : "")
    + `<b>透過 GitHub 回報</b>：公開在專案的 GitHub Issues（<span style="font-family:var(--mono)">${esc(init.repo)}</span>），需要 GitHub 帳號；會在瀏覽器開啟已填好的回報頁，並開啟附件所在資料夾，把檔案拖進去即可。`
    + `<br><b>複製回報內容</b>：整理好的內容複製到剪貼簿，可貼到任何聊天或信件。`;
  $("btnFbSend").classList.toggle("hidden", !init.directSend);
  $("fbReplyRow").classList.toggle("hidden", !init.directSend);
  $("btnFbGitHub").classList.toggle("primary", !init.directSend);
  refreshDependent();
}

/* ---------------- add task ---------------- */
async function addTask(){
  fieldError(null);
  const res = await goStart(collect());
  if(res.error){ fieldError(res.field, res.error); if(!res.field) showBanner("err", res.error); return; }
  hideBanner(); toast(`已加入任務 ${res.jobId}（${res.urls.length} 項）`);
  $("fileName").value=""; // 檔名只給這一個任務，避免下一個網址沿用
}
$("btnGo").onclick=addTask;
document.addEventListener("keydown",e=>{ if(e.ctrlKey && e.key==="Enter"){ e.preventDefault(); addTask(); } });

