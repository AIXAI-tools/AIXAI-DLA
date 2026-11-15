/* ---------------- events from Go ---------------- */
window.aixai={ on(name,p){
  switch(name){
    case "log": appendLog(p); break;
    case "status": $("status").textContent=p; break;
    case "env": $("env").textContent=p; break;
    case "job": mergeJob(p); renderJobs(); break;
    case "jobs": setJobs(p||[]); break;
    case "failed": setFailed(p||[]); break;
    case "historyChanged": if($("dlgHistory").open) loadHistory(); break;
    case "jobRemoved": delete state.jobs[p.id]; renderJobs(); break;
    case "jobStatus": { const j=state.jobs[p.id]; if(j){ j.status=p.status; renderJobsSoon(); } break; }
    case "task": {
      const j=state.jobs[p.job]; const t=j&&j.items[p.index]; if(!t) break;
      t.state=p.state; if(p.message){ t.msg=p.message; t.brief=p.brief||""; }
      if(p.state==="running"){ t.pct=null; t.msg=""; }
      renderJobsSoon(); break; }
    case "login":
      if(p.waiting && p.reason==="action"){
        showBanner("warn","正片還沒開始播放：請在跳出的瀏覽器視窗操作（例如按播放、看完或關閉廣告）。\n開始播放後會自動繼續，最多等待 10 分鐘。",
          [mkBtn("顯示瀏覽器視窗",()=>goShowLoginWindow().then(e=>{ if(e) toast(e); }))]);
      } else if(p.waiting){
        showBanner("warn","需要登入：請在跳出的瀏覽器視窗完成登入。\n程式會一直等待，直到登入完成或按「停止」。",
          [mkBtn("顯示登入視窗",()=>goShowLoginWindow().then(e=>{ if(e) toast(e); }))]);
      } else { hideBanner(); }
      break;
    case "snip": onSnip(p||{}); break;
    case "releases": renderReleases(p); break;
    case "update": renderUpdate(p); break;
    case "replies": queueReplies(p); break;
  }
}};

/* ---------------- update dialog ---------------- */
$("btnUpdate").onclick=()=>{ $("dlgUpdate").showModal(); if(state.installing) return; $("relState").textContent="正在讀取版本清單…"; $("relState").classList.remove("hidden"); $("relList").innerHTML=""; $("btnInstall").disabled=true; goCheckUpdates(); };
$("startupUpdCheck").onchange=()=>goSetStartupUpdateCheck($("startupUpdCheck").checked);
$("btnRelPage").onclick=()=>goOpenURL((state.releases&&state.releases.releasesUrl)||"https://github.com/");
function renderReleases(r){
  state.releases=r;
  if(r.error){ $("relState").textContent="無法取得版本清單："+r.error; return; }
  const newer=r.items.filter(i=>i.newer && i.channel==="stable");
  $("btnUpdate").classList.toggle("has-update", newer.length>0);
  $("relState").textContent = newer.length ? `有新版本可用：${newer[0].tag}` : "目前已是最新的正式版本。";
  $("relList").innerHTML=r.items.map((it,i)=>{
    const badges=[it.current?'<span class="badge cur">目前使用</span>':"", it.tag===r.latestStable?'<span class="badge stable">最新正式版</span>':"",
      it.channel==="beta"?'<span class="badge beta">測試版</span>':"", !it.current&&!it.newer?'<span class="badge old">較舊</span>':""].join("");
    return `<label class="rel"><input type="radio" name="rel" value="${esc(it.tag)}" ${it.current||!it.installable?"disabled":""} ${!it.current&&it.tag===r.latestStable&&it.newer?"checked":""}>
      <div><div class="t">${esc(it.tag)} ${badges}</div><div class="d">${esc(it.published)} · ${esc(it.title)}${it.installable?"":" · 此版本沒有可自動安裝的檔案"}</div>${it.notes?`<pre>${esc(it.notes)}</pre>`:""}</div></label>`;
  }).join("");
  const sync=()=>{ const sel=document.querySelector('input[name=rel]:checked'); const it=sel&&r.items.find(x=>x.tag===sel.value);
    $("btnInstall").disabled=!it||state.installing; $("btnInstall").textContent = !it ? "安裝所選版本" : (it.newer ? `更新到 ${it.tag}` : `退回到 ${it.tag}`); };
  $("relList").onchange=sync; sync();
  if(state.preselectTag){ // opened from a reply: select that version
    const tag=state.preselectTag; state.preselectTag=null;
    const radio=[...document.querySelectorAll("input[name=rel]")].find(x=>x.value===tag);
    if(radio && !radio.disabled){ radio.checked=true; sync(); }
    else $("relState").textContent+=`（版本清單中找不到可安裝的 ${tag}）`;
  }
  // 有新版本：每次啟動、以及程式開著時每個新版本各跳出一次版本視窗（已自己打開就不再跳）
  if(newer.length && state.notifiedTag!==newer[0].tag){
    state.notifiedTag=newer[0].tag;
    if(!$("dlgUpdate").open){ state.updatePopup=true; maybeShowUpdate(); }
  }
}
function maybeShowUpdate(){
  if(!state.updatePopup || document.querySelector("dialog[open]")) return; // 其他視窗開著時，等它關掉再跳
  state.updatePopup=false;
  $("dlgUpdate").showModal();
}
setInterval(maybeShowUpdate,30000);
// 這個版本起，更新前暫停的任務會在更新後自動恢復；退回更舊的版本則不會
const AUTO_RESUME_SINCE="4.2.2";
function verLess(a,b){ const p=v=>String(v).replace(/^v/,"").split(/[.-]/).map(x=>parseInt(x,10)||0); const x=p(a),y=p(b);
  for(let i=0;i<3;i++){ if((x[i]||0)!==(y[i]||0)) return (x[i]||0)<(y[i]||0); } return false; }
$("btnInstall").onclick=()=>{
  const sel=document.querySelector('input[name=rel]:checked'); if(!sel) return;
  const active=Object.values(state.jobs).filter(j=>j.state==="running"||j.state==="queued").length;
  if(active){
    const how = verLess(sel.value, AUTO_RESUME_SINCE)
      ? `將暫時暫停所有的下載任務。退回的 ${sel.value} 不會自動恢復，切換版本後請到任務列表按「繼續」。`
      : "將暫時暫停所有的下載任務，更新完會自動恢復！";
    if(!confirm([`目前有 ${active} 個下載任務進行中。`, "", how, "（你自己暫停的任務維持暫停）", "", "確定要繼續嗎？"].join("\n"))) return;
  }
  state.installing=true; // 安裝中換選版本、重開視窗都不再啟用按鈕（後端也會擋第二次）
  $("btnInstall").disabled=true; $("updProgress").classList.remove("hidden"); $("updBar").style.width="0"; $("updMsg").textContent="準備中…";
  goInstallRelease(sel.value);
};
function renderUpdate(p){
  $("updProgress").classList.remove("hidden"); $("updBar").style.width=p.percent+"%"; $("updMsg").textContent=p.message;
  $("updMsg").style.color = p.stage==="error" ? "var(--danger)" : "var(--muted)";
  if(p.stage==="error"){ state.installing=false; $("btnInstall").disabled=!document.querySelector('input[name=rel]:checked'); }
}

