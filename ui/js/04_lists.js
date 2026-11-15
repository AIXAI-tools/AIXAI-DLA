/* ---------------- address box tools ---------------- */
function urlLines(){ return $("urls").value.split(/\r?\n/).map(s=>s.trim()).filter(Boolean); }
function appendUrls(list){
  const have=new Set(urlLines()); const add=list.filter(u=>!have.has(u));
  if(add.length){ const cur=$("urls").value.replace(/\s+$/,""); $("urls").value=(cur?cur+"\n":"")+add.join("\n")+"\n"; }
  return add.length;
}
$("btnUrlCopy").onclick=async()=>{ const n=urlLines().length; if(!n){ toast("網址欄是空的"); return; } toast(await copyText($("urls").value.trim())?`已複製 ${n} 個網址`:"無法存取剪貼簿"); };
// The last cleared content stays restorable until the program closes.
let urlUndo="";
$("btnUrlClear").onclick=()=>{
  const old=$("urls").value; if(!old.trim()){ toast("網址欄已經是空的"); return; }
  urlUndo=old; $("urls").value=""; fieldError(null); $("btnUrlUndo").classList.remove("hidden");
  toast("已清除網址欄；需要時按「復原」找回（程式關閉前都有效）");
};
$("btnUrlUndo").onclick=()=>{
  if(!urlUndo) return;
  const now=urlLines(); $("urls").value=urlUndo; urlUndo=""; $("btnUrlUndo").classList.add("hidden");
  const n=appendUrls(now); toast(n?`已復原，剛才另外輸入的 ${n} 個網址接在後面`:"已復原");
};
function closeUrlMenu(){ $("urlMenu").classList.remove("open"); $("btnUrlMenu").setAttribute("aria-expanded","false"); }
$("btnUrlMenu").onclick=e=>{ e.stopPropagation(); const m=$("urlMenu"); const open=!m.classList.contains("open"); m.classList.toggle("open",open); $("btnUrlMenu").setAttribute("aria-expanded",String(open)); };
document.addEventListener("click",e=>{ if(!e.target.closest(".menu-wrap")) closeUrlMenu(); });
document.addEventListener("keydown",e=>{ if(e.key==="Escape") closeUrlMenu(); });
$("urlMenu").onclick=async e=>{
  const b=e.target.closest("button[data-m]"); if(!b) return; closeUrlMenu();
  switch(b.dataset.m){
    case "history": openHistory(); break;
    case "import": { const r=await goImportURLs(); if(r.error){ toast(r.error); break; } if(!r.urls||!r.urls.length) break;
      const n=appendUrls(r.urls); toast(n?`已加入 ${n} 個網址（${r.urls.length-n} 個已在網址欄）`:"這些網址都已經在網址欄了"); break; }
    case "export": { const m=await goExportURLs($("urls").value); if(m) toast(m); break; }
  }
};

/* ---------------- history ---------------- */
const hist={list:[],sel:new Set()};
const HIST_RENDER_MAX=800;
function histFiltered(){
  const q=$("histQ").value.trim().toLowerCase(), f=$("histF").value;
  return hist.list.filter(h=>(!f||h.state===f)&&(!q||h.url.toLowerCase().includes(q)||(h.title||"").toLowerCase().includes(q)));
}
function renderHistory(){
  const rows=histFiltered(); const ok=hist.list.filter(h=>h.state==="done").length;
  $("histCount").textContent=hist.list.length?`共 ${hist.list.length} 筆（成功 ${ok}、失敗 ${hist.list.length-ok}）`:"";
  const ids=new Set(hist.list.map(h=>h.id)); [...hist.sel].forEach(id=>{ if(!ids.has(id)) hist.sel.delete(id); });
  $("histSel").textContent=hist.sel.size?`已選取 ${hist.sel.size} 筆`:(rows.length?`未選取：匯出會包含目前顯示的 ${rows.length} 筆`:"");
  $("histAll").checked=rows.length>0&&rows.every(h=>hist.sel.has(h.id));
  ["histAdd","histDel"].forEach(id=>$(id).disabled=!hist.sel.size);
  ["histCsv","histTxt"].forEach(id=>$(id).disabled=!(hist.sel.size||rows.length));
  $("histClear").disabled=!hist.list.length;
  const ul=$("histList");
  if(!rows.length){ ul.innerHTML=`<li class="empty">${hist.list.length?"沒有符合條件的紀錄。":"還沒有下載紀錄。每個網址下載完成或失敗後都會記在這裡。"}</li>`; return; }
  ul.innerHTML=rows.slice(0,HIST_RENDER_MAX).map(h=>`<li class="hrow ${h.state}">
    <input type="checkbox" data-id="${h.id}" ${hist.sel.has(h.id)?"checked":""} aria-label="選取">
    <div style="min-width:0">
      <div class="t" title="${esc(h.title||h.url)}">${esc(h.title||h.url)}</div>
      ${h.title?`<div class="u" title="${esc(h.url)}">${esc(h.url)}</div>`:""}
      <div class="m"><span class="badge ${h.state}">${h.state==="done"?"成功":"失敗"}</span> ${esc(h.time)}</div>
      ${h.state==="failed"?reasonHTML(h.brief,h.reason):""}
    </div>
    <div class="ra">
      ${h.state==="done"?`<button class="btn ghost sm" data-ha="reveal" data-id="${h.id}" title="在檔案總管中顯示">開啟位置</button>`:""}
      <button class="btn ghost sm" data-ha="copy" data-id="${h.id}">複製網址</button>
    </div></li>`).join("")+(rows.length>HIST_RENDER_MAX?`<li class="empty">只顯示前 ${HIST_RENDER_MAX} 筆，請用搜尋縮小範圍。</li>`:"");
}
async function loadHistory(){ hist.list=await goHistoryList()||[]; renderHistory(); }
async function openHistory(){ hist.sel.clear(); $("histQ").value=""; $("histF").value=""; $("dlgHistory").showModal(); await loadHistory(); $("histQ").focus(); }
function histTargetIds(){ return hist.sel.size?[...hist.sel]:histFiltered().map(h=>h.id); }
$("histQ").oninput=renderHistory; $("histF").onchange=renderHistory;
$("histAll").onchange=()=>{ const rows=histFiltered().slice(0,HIST_RENDER_MAX); if($("histAll").checked) rows.forEach(h=>hist.sel.add(h.id)); else rows.forEach(h=>hist.sel.delete(h.id)); renderHistory(); };
$("histList").onchange=e=>{ const c=e.target.closest("input[data-id]"); if(!c) return; const id=+c.dataset.id; if(c.checked) hist.sel.add(id); else hist.sel.delete(id); renderHistory(); };
$("histList").onclick=async e=>{
  const b=e.target.closest("button[data-ha]"); if(!b) return; const h=hist.list.find(x=>x.id===+b.dataset.id); if(!h) return;
  if(b.dataset.ha==="reveal"){ const m=await goHistoryReveal(h.id); if(m) toast(m); }
  else toast(await copyText(h.url)?"已複製網址":"無法存取剪貼簿");
};
$("histAdd").onclick=()=>{ const urls=hist.list.filter(h=>hist.sel.has(h.id)).map(h=>h.url); const n=appendUrls([...new Set(urls)]); $("dlgHistory").close(); toast(n?`已加入 ${n} 個網址到網址欄，按「開始下載」即可下載`:"這些網址都已經在網址欄了"); };
$("histCsv").onclick=async()=>{ const m=await goHistoryExport(histTargetIds(),"csv"); if(m) toast(m); };
$("histTxt").onclick=async()=>{ const m=await goHistoryExport(histTargetIds(),"txt"); if(m) toast(m); };
$("histDel").onclick=async()=>{ if(!hist.sel.size||!confirm(`從歷史清單刪除選取的 ${hist.sel.size} 筆？（已下載的檔案不受影響）`)) return; await goHistoryRemove([...hist.sel]); hist.sel.clear(); await loadHistory(); };
$("histClear").onclick=async()=>{ if(!confirm(`清除全部 ${hist.list.length} 筆歷史紀錄？（已下載的檔案不受影響）建議先匯出備份。`)) return; await goHistoryRemove([]); hist.sel.clear(); await loadHistory(); };

/* ---------------- data folder ---------------- */
$("btnDataFolder").onclick=async()=>{ const e=await goOpenDataFolder(); if(e) toast(e); };
$("btnDataPick").onclick=async()=>{ const p=await goPickFolder($("dataFolder").value||$("dataFolder").placeholder); if(p){ $("dataFolder").value=p; saveSoon(); toast("之後的下載紀錄與回報包會存到："+p); } };
$("btnDataDefault").onclick=()=>{ $("dataFolder").value=""; saveSoon(); toast("已改回預設位置："+$("dataFolder").placeholder); };
$("dataFolder").onchange=saveSoon;

/* ---------------- failed addresses ---------------- */
let failedList=[];
function setFailed(list){
  failedList=list||[];
  const ul=$("fails"), n=failedList.length;
  $("failCount").textContent=n?String(n):"";
  $("failCard").hidden=!n; // 沒有失敗時整區隱藏，避免被當成輸入網址的地方
  ["btnFailRetryAll","btnFailCopyAll","btnFailClear"].forEach(id=>$(id).disabled=!n);
  if(!n){ ul.innerHTML='<li class="empty">目前沒有。批次中有網址無法下載時會列在這裡，其他網址照常下載完成。</li>'; return; }
  ul.innerHTML=failedList.slice().reverse().map(f=>`<li class="fail">
    <div class="u" title="${esc(f.url)}">${esc(f.url)}</div>
    ${reasonHTML(f.brief,f.reason)}
    <div class="meta">任務 ${f.jobId} 第 ${f.item} 項 · ${esc(f.time)}${f.count>1?` · 已失敗 ${f.count} 次`:""}</div>
    <div class="acts">
      <button class="btn sm" data-fact="retry" data-id="${f.id}" title="用原本的下載設定，加入一個新任務重新下載">重試</button>
      <button class="btn ghost sm" data-fact="copy" data-id="${f.id}">複製網址</button>
      <button class="btn ghost sm" data-fact="open" data-id="${f.id}" title="在瀏覽器開啟，確認影片是否還在、是否需要登入">在瀏覽器開啟</button>
      <button class="btn ghost sm" data-fact="remove" data-id="${f.id}">從清單移除</button>
    </div></li>`).join("");
}
async function copyText(text){
  try{ await navigator.clipboard.writeText(text); return true; }catch(e){}
  const t=document.createElement("textarea"); t.value=text; document.body.appendChild(t); t.select();
  let ok=false; try{ ok=document.execCommand("copy"); }catch(e){} t.remove(); return ok;
}
$("fails").onclick=async e=>{
  const b=e.target.closest("button[data-fact]"); if(!b) return;
  const id=+b.dataset.id, f=failedList.find(x=>x.id===id); if(!f) return;
  switch(b.dataset.fact){
    case "retry": { const err=await goFailedRetry([id]); toast(err||"已加入新任務重新下載；成功後會自動從清單移除。"); break; }
    case "copy": toast(await copyText(f.url)?"已複製網址":"無法存取剪貼簿"); break;
    case "open": if(/^https:\/\//i.test(f.url)) goOpenURL(f.url); else toast("只能開啟 https:// 開頭的網址"); break;
    case "remove": goFailedRemove([id]); break;
  }
};
$("btnFailRetryAll").onclick=async()=>{ if(!failedList.length) return; const err=await goFailedRetry([]); toast(err||`已把 ${failedList.length} 個網址加入新任務重新下載。`); };
$("btnFailCopyAll").onclick=async()=>{ if(!failedList.length) return; toast(await copyText(failedList.map(f=>f.url).join("\n"))?`已複製 ${failedList.length} 個網址`:"無法存取剪貼簿"); };
$("btnFailClear").onclick=()=>{ if(failedList.length && confirm(`清除「下載失敗清單」中的 ${failedList.length} 筆？已下載的檔案不受影響。`)) goFailedRemove([]); };

/* ---------------- log ---------------- */
const LOG_MAX=3000;
function classify(line){
  if(/^(===|---)/.test(line)) return "head";
  if(/^(✓|\[download\] 100%)/.test(line)) return "ok";
  if(/^(⚠|WARNING)/.test(line)) return "warn";
  if(/^(錯誤|ERROR|✕)|失敗/.test(line)) return "err";
  if(/^(→|🛡)/.test(line)) return "step";
  if(/^(ℹ|\[)/.test(line)) return "info";
  return "";
}
let logPending="";
function appendLog(text){
  logPending+=text;
  const parts=logPending.split(/\r?\n/); logPending=parts.pop();
  const box=$("log"); const atBottom=box.scrollHeight-box.scrollTop-box.clientHeight<40;
  const frag=document.createDocumentFragment();
  for(const raw of parts){
    let line=raw.replace(/\r/g,""); if(!line.trim()) continue;
    let jobId=0; const tag=line.match(/^\[任務(\d+)\] /);
    if(tag){ jobId=+tag[1]; line=line.slice(tag[0].length); }
    const m=line.match(/^\[download\]\s+(\d+(?:\.\d+)?)%/);
    if(m){
      const j=state.jobs[jobId]; const cur=j&&j.items.find(t=>t.state==="running");
      if(cur){ cur.pct=parseFloat(m[1]); renderJobsSoon(); }
      if(!line.includes("100%")) continue;
    }
    const div=document.createElement("div"); const c=classify(line); if(c) div.className=c;
    if(jobId){ const t=document.createElement("span"); t.className="jobtag"; t.textContent="#"+jobId; div.appendChild(t); }
    div.appendChild(document.createTextNode(line)); frag.appendChild(div);
  }
  box.appendChild(frag);
  while(box.childElementCount>LOG_MAX) box.firstElementChild.remove();
  if(atBottom) box.scrollTop=box.scrollHeight;
}
$("btnCopyLog").onclick=async()=>{ const e=await goCopyLog(); toast(e||"已複製完整紀錄（敏感資訊已遮蔽）"); };
$("btnOpenLog").onclick=async()=>{ const e=await goOpenLogFile(0); if(e) toast(e); };
$("btnClearLog").onclick=()=>{ $("log").innerHTML=""; toast("已清除畫面（紀錄檔不受影響）"); };

