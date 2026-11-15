/* ---------------- feedback dialog ---------------- */
/* 回報視窗可拖曳標題列移動（每次開啟回到置中） */
(()=>{ const d=$("dlgFeedback"), h=d.querySelector(".dlg-h"); h.classList.add("movable"); h.title="拖曳可移動視窗";
  let sx=0, sy=0, ox=0, oy=0, on=false;
  h.addEventListener("pointerdown",e=>{ if(e.button!==0||e.target.closest("button,input,select,textarea,a")) return;
    on=true; sx=e.clientX; sy=e.clientY; ox=+d.dataset.dx||0; oy=+d.dataset.dy||0; h.setPointerCapture(e.pointerId); });
  h.addEventListener("pointermove",e=>{ if(!on) return;
    const r=d.getBoundingClientRect(), cx=r.left-(+d.dataset.dx||0), cy=r.top-(+d.dataset.dy||0);
    // 左右整個留在視窗內、標題列不超出上下緣（關閉鈕一直看得到）
    const dx=Math.min(Math.max(ox+e.clientX-sx, -cx), Math.max(-cx, innerWidth-r.width-cx));
    const dy=Math.min(Math.max(oy+e.clientY-sy, -cy), Math.max(-cy, innerHeight-h.offsetHeight-cy));
    d.dataset.dx=dx; d.dataset.dy=dy; d.style.translate=`${dx}px ${dy}px`; });
  const end=()=>{ on=false; }; h.addEventListener("pointerup",end); h.addEventListener("pointercancel",end);
})();
function resetFeedbackPos(){ const d=$("dlgFeedback"); d.dataset.dx=0; d.dataset.dy=0; d.style.translate=""; }
function openFeedback(prefill){
  resetFeedbackPos(); $("dlgFeedback").showModal(); $("fbResult").className="banner";
  if(prefill && !$("fbText").value) $("fbText").value="執行時出現錯誤：\n"+prefill.split("\n").slice(0,4).join("\n")+"\n\n（請補充：操作步驟）";
  $("fbText").focus();
  if(!state.logs.length) goLatestLogs().then(ls=>{ if(state.logs.length) return; (ls||[]).forEach(l=>{ if(l && l.path && !state.logsRemoved.has(l.name)) state.logs.push(l); }); renderLogs(); });
}
function renderLogs(){
  $("fbLogs").innerHTML=state.logs.map((l,i)=>`<div class="chip" title="${esc(l.path)}">${esc(l.name)}<button data-i="${i}" aria-label="移除">×</button></div>`).join("");
}
$("fbLogs").onclick=e=>{ const b=e.target.closest("button[data-i]"); if(b){ state.logsRemoved.add(state.logs[+b.dataset.i].name); state.logs.splice(+b.dataset.i,1); renderLogs(); } };
$("btnAddLog").onclick=async()=>{
  const r=await goPickLogs();
  (r.logs||[]).forEach(l=>{ if(!state.logs.some(x=>x.path===l.path)) state.logs.push(l); });
  renderLogs(); if(r.problem) toast(r.problem);
};
$("btnFeedback").onclick=()=>openFeedback();
document.querySelectorAll(".seg button").forEach(b=>b.onclick=()=>{ document.querySelectorAll(".seg button").forEach(x=>x.setAttribute("aria-pressed","false")); b.setAttribute("aria-pressed","true"); state.kind=b.dataset.kind; });
function renderThumbs(){
  $("thumbs").innerHTML=state.images.map((im,i)=>`<div class="thumb" title="${esc(im.name)}">${im.dataUrl?`<img src="${im.dataUrl}" alt="">`:esc(im.name)}<button data-i="${i}" aria-label="移除">×</button></div>`).join("");
}
$("thumbs").onclick=e=>{ const b=e.target.closest("button[data-i]"); if(b){ state.images.splice(+b.dataset.i,1); renderThumbs(); } };
/* Partial screenshot: Windows' own snipping overlay; the picture arrives as a "snip" event.
   The dialog steps aside so the window behind it can be captured, and comes back when
   this window gets the focus again (snip taken or cancelled with Esc). */
function snipBusy(on){ state.snipping=on; $("btnShot").textContent=on?"等待截圖…（按此取消）":"部分截圖"; }
$("btnShot").onclick=async()=>{
  if(state.snipping){ goCancelSnip(); return; }
  const d=$("dlgFeedback"); d.close(); await new Promise(r=>setTimeout(r,250));
  const err=await goStartSnip();
  if(err){ toast(err); d.showModal(); return; }
  snipBusy(true);
  setTimeout(()=>window.addEventListener("focus",()=>setTimeout(()=>{ if(!d.open) d.showModal(); },400),{once:true}),800);
};
function onSnip(p){
  snipBusy(false);
  const d=$("dlgFeedback"); if(!d.open) d.showModal();
  if(p.image){ state.images.push(p.image); renderThumbs(); toast("已加入截圖"); }
  else if(p.error) toast(p.error);
}
$("btnAddImg").onclick=async()=>{ const list=await goPickImages(); (list||[]).forEach(im=>state.images.push(im)); renderThumbs(); };
async function sendFeedback(channel){
  fieldError(null);
  if(!$("fbText").value.trim()){ fieldError("fbText","請先寫下想回報的內容。"); return; }
  const sendBtn=$("btnFbSend");
  if(channel==="send"){ sendBtn.disabled=true; sendBtn.textContent="送出中…"; }
  try{
    const res=await goSubmitFeedback({kind:state.kind,text:$("fbText").value,contact:$("fbContact").value,includeDiag:$("fbDiag").checked,images:state.images.map(i=>i.path),logs:state.logs.map(l=>l.path),channel,
      wantReply:channel==="send"&&$("fbWantReply").checked, ref:state.fbRef||""});
    const b=$("fbResult"); b.className="banner show ok"; b.textContent=res.message;
    if(channel==="send"){ $("fbText").value=""; state.images=[]; state.logs=[]; renderThumbs(); renderLogs(); setFeedbackRef(null); }
  }catch(e){ const b=$("fbResult"); b.className="banner show err"; b.textContent=String(e); }
  finally{ sendBtn.disabled=false; sendBtn.textContent="直接送出"; }
}
$("btnFbSend").onclick=()=>sendFeedback("send");
/* A report that follows up an earlier one (reply to the developer, test result). */
function setFeedbackRef(n){
  state.fbRef=n?n.thread:"";
  const el=$("fbRef"); el.classList.toggle("hidden",!n); el.textContent="";
  if(!n) return;
  el.append(`這份回報會接續你在 ${n.sent} 回報的「${n.title}」（開發者會看到是同一件事）。`);
  const x=mkBtn("不接續",()=>setFeedbackRef(null)); x.style.marginLeft="8px"; el.append(x);
}
// 從任務卡「回報問題」：帶入這個任務的網址與紀錄檔，使用者只要寫哪裡不對
async function openFeedbackForJob(j){
  const urls=(j.urls||[]).slice(0,10), more=(j.urls||[]).length-urls.length;
  setFeedbackRef(null);
  const NL=String.fromCharCode(10);
  $("fbText").value=[`任務 ${j.id} 的網址：`, ...urls, ...(more>0?[`…另外 ${more} 個`]:[]), "", "哪裡不對（例如：下載失敗、沒有聲音、只有音樂、畫面或集數不對）：", ""].join(NL);
  state.logs=[]; state.logsRemoved=new Set(); renderLogs();
  openFeedback();
  const t=$("fbText"); t.setSelectionRange(t.value.length,t.value.length);
  const ls=await goJobFeedbackLogs(j.id);
  (ls||[]).forEach(l=>{ if(l && l.path && !state.logs.some(x=>x.path===l.path)) state.logs.push(l); }); renderLogs();
}
function openFeedbackFor(n,text){
  setFeedbackRef(n);
  if(!$("fbText").value.trim()) $("fbText").value=text;
  $("fbWantReply").checked=true;
  openFeedback();
  const t=$("fbText"); t.setSelectionRange(t.value.length,t.value.length);
}

/* ---------------- replies to reports ---------------- */
state.replyQueue=[]; state.replyDone=new Set();
const REPLY_HEAD={fixed:"已修正",test:"請幫忙測試",info:"需要更多資訊",wontfix:"無法處理"};
function queueReplies(list){
  (list||[]).forEach(n=>{
    n.k=n.thread+"|"+n.reply.rid+"|"+n.stage;
    if(!state.replyDone.has(n.k) && !state.replyQueue.some(q=>q.k===n.k)) state.replyQueue.push(n);
  });
  maybeShowReply();
}
function replyBusy(){ return Object.values(state.jobs).some(j=>j.status==="running") || !!document.querySelector("dialog[open]"); }
function maybeShowReply(){
  if(!state.replyQueue.length || $("dlgReply").open) return;
  if(replyBusy()){
    showBanner("ok",`開發者回覆了你的回報（${state.replyQueue.length} 則）。`,[mkBtn("查看",()=>{ hideBanner(); openReply(); })]);
    return;
  }
  openReply();
}
// A reply that came in during a download opens once everything is idle.
setInterval(()=>{ if(state.replyQueue.length && !replyBusy()){ hideBanner(); openReply(); } },30000);
function fillURLs(list){
  const cur=$("urls").value.trim();
  $("urls").value=(cur?cur+"\n":"")+list.join("\n");
  $("urls").dispatchEvent(new Event("input"));
  toast("已填入網址，按「開始下載」即可");
}
function openUpdateTo(tag){ state.preselectTag=tag; $("btnUpdate").click(); }
function openReply(){
  const n=state.replyQueue[0]; if(!n || $("dlgReply").open) return;
  const r=n.reply, follow=n.stage==="testFollowup";
  $("rpHead").textContent=follow?"測試結果":"開發者的回覆："+(REPLY_HEAD[r.kind]||"");
  $("rpAbout").textContent=`你在 ${n.sent} 回報的：「${n.title}」`+(state.replyQueue.length>1?`（還有 ${state.replyQueue.length-1} 則）`:"");
  $("rpText").textContent=follow
    ? `你正在使用測試版 ${r.version}。試過之後結果如何？告訴開發者，就能決定是否放進正式版。\n\n開發者當時的說明：\n${r.text}`
    : r.text;
  const urls=r.kind==="test"?(r.urls||[]):[];
  $("rpUrls").textContent=urls.length?"測試網址：\n"+urls.join("\n"):"";
  const acts=$("rpActs"); acts.innerHTML="";
  const add=(label,fn,primary)=>{ const b=mkBtn(label,fn); b.classList.remove("sm"); if(primary) b.classList.add("primary"); acts.appendChild(b); };
  const done=(action,then)=>{ goReplyAction(n.thread,r.rid,action); state.replyDone.add(n.k); state.replyQueue.shift(); $("dlgReply").close(); if(then) then(); setTimeout(maybeShowReply,400); };
  if(follow){
    add("還沒試",()=>done("later"));
    add("還是不行",()=>done("testFail",()=>openFeedbackFor(n,`測試結果（${r.version}，${n.title}）：還是不行。\n\n`)));
    add("可以了",()=>done("testOK",()=>openFeedbackFor(n,`測試結果（${r.version}，${n.title}）：可以了，問題已解決。\n`)),true);
  } else if(r.kind==="fixed"){
    const failed=n.urls||[];
    if(n.newer){ add("知道了",()=>done("ack")); add(`更新到 ${r.version}`,()=>done("ack",()=>openUpdateTo(r.version)),true); }
    else if(failed.length){ add("知道了",()=>done("ack")); add(`重試當時失敗的網址（${failed.length} 個）`,()=>done("ack",()=>fillURLs(failed)),true); }
    else add("知道了",()=>done("ack"),true);
  } else if(r.kind==="test"){
    if(urls.length) add("填入測試網址",()=>fillURLs(urls));
    if(n.newer) add(`安裝測試版 ${r.version}`,()=>done("ack",()=>openUpdateTo(r.version)),true);
    else add("知道了，開始測試",()=>done("ack",()=>queueReplies([{...n,stage:"testFollowup"}])),true);
  } else if(r.kind==="info"){
    add("回覆開發者",()=>done("ack",()=>openFeedbackFor(n,`回覆開發者（${n.title}）：\n\n`)),true);
  } else {
    add("知道了",()=>done("ack"),true);
  }
  $("rpLater").onclick=()=>done("later");
  $("dlgReply").oncancel=e=>{ e.preventDefault(); done("later"); };
  $("dlgReply").showModal();
}
$("btnFbGitHub").onclick=()=>sendFeedback("github");
$("btnFbCopy").onclick=()=>sendFeedback("copy");
document.querySelectorAll("dialog [data-close]").forEach(b=>b.onclick=()=>b.closest("dialog").close());

