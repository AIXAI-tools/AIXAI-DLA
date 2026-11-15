/* ---------------- tasks ---------------- */
const JOB_LABEL={queued:"排隊中",running:"下載中",paused:"已暫停",done:"已完成",partial:"部分失敗",failed:"失敗",aborted:"已中止"};
const JOB_BTNS={
  queued:[["prioritize","優先下載"],["pause","暫停"],["abort","中止"]],
  running:[["pause","暫停"],["abort","中止"]],
  paused:[["resume","繼續"],["abort","中止"],["remove","移除"]],
  failed:[["resume","繼續"],["abort","中止"],["remove","移除"]],
  partial:[["remove","移除"]], done:[["remove","移除"]], aborted:[["remove","移除"]]
};
function mergeJob(v){
  const old=state.jobs[v.id];
  if(old) v.items.forEach((t,i)=>{ if(t.state==="running" && old.items[i] && old.items[i].pct!=null) t.pct=old.items[i].pct; });
  state.jobs[v.id]=v;
}
function setJobs(list){ const keep={}; list.forEach(v=>{ mergeJob(v); keep[v.id]=true; }); Object.keys(state.jobs).forEach(id=>{ if(!keep[id]) delete state.jobs[id]; }); renderJobs(); }
function renderJobs(){
  if(dragJob){ dragPending=true; return; } // 拖曳中不重畫（把手被換掉拖曳會中斷），放開後補畫
  const ul=$("jobs"); const list=Object.values(state.jobs).sort((a,b)=>(a.order-b.order)||(a.id-b.id));
  const firstActive=list.find(j=>j.state==="running"||j.state==="queued");
  const running=list.filter(j=>j.state==="running").length, queued=list.filter(j=>j.state==="queued").length;
  $("btnPauseAll").disabled=!(running||queued); $("btnResumeAll").disabled=!list.some(j=>j.state==="paused");
  if(list.length) $("status").textContent = running||queued ? `執行中 ${running} 個任務${queued?`，排隊 ${queued} 個`:""}。` : "沒有執行中的任務。";
  if(!list.length){ ul.innerHTML='<li class="empty">尚無任務。貼上網址後按「開始下載」；下載中也可以繼續加入新任務。</li>'; if(state.foldJobs.size){ state.foldJobs.clear(); saveFolds(); } updateJobMove(); return; }
  const icon={pending:"○",running:'<span class="spin"></span>',done:"✓",failed:"✕"};
  // 原地更新每張任務卡片（不整段重建），項目清單的捲動位置、拖曳中的捲軸、展開狀態才不會每次更新就重設
  const empty=ul.querySelector(":scope > li.empty"); if(empty) empty.remove();
  const have={}; ul.querySelectorAll(":scope > li.job").forEach(li=>{ have[li.dataset.job]=li; });
  const setHTML=(el,h)=>{ if(el._h!==h){ el._h=h; el.innerHTML=h; } };
  let prev=null;
  list.forEach(j=>{
    const total=j.items.length, done=j.items.filter(t=>t.state==="done").length, failed=j.items.filter(t=>t.state==="failed").length;
    const cur=j.items.find(t=>t.state==="running"); const part=cur&&cur.pct?cur.pct/100:0;
    const line = j.state==="running" ? (j.status||"") : j.state==="queued" ? (j.waiting||"等待開始…") : (j.message||"");
    const btns=(JOB_BTNS[j.state]||[]).filter(([a])=>a!=="prioritize"||j!==firstActive).map(([a,l])=>`<button class="btn sm${a==="abort"?" danger":""}" data-act="${a}" data-id="${j.id}">${l}</button>`).join("")
      + `<button class="btn ghost sm" data-act="log" data-id="${j.id}">紀錄檔</button><button class="btn ghost sm" data-act="folder" data-id="${j.id}">開啟資料夾</button>`
      + (j.state==="failed"||j.state==="partial"||j.state==="done"||j.state==="paused"||j.state==="aborted" ? `<button class="btn ghost sm" data-act="feedback" data-id="${j.id}" title="這個任務有問題（失敗、沒有聲音、畫面不對…）？按這裡回報，會自動附上網址與這個任務的紀錄">回報問題</button><button class="btn ghost sm" data-act="report" data-id="${j.id}" title="把這個任務的診斷摘要與紀錄整理成一個檔案（已遮蔽敏感資訊），可直接傳給開發者">回報包</button>` : "");
    const items=j.items.map((t,i)=>`<span class="ic">${icon[t.state]||"○"}</span>
      <div style="min-width:0"><div class="u" title="${esc(j.urls[i]||"")}">${i+1}. ${esc(j.urls[i]||"")}</div>${t.msg?`<div class="m" title="${esc(t.msg)}">${esc(t.brief||t.msg)}</div>`:""}</div>
      <span class="p">${t.state==="running"&&t.pct!=null?t.pct.toFixed(0)+"%":""}</span>`);
    const movable=j.state==="running"||j.state==="queued";
    const site=(j.sites||[]).join("、");
    // 劇名、資料夾不固定字數：依標題列剩餘寬度自動縮短（CSS），完整內容看滑鼠提示
    const parts=[["id",`任務 ${j.id}`],["dm",site],["nm",j.name||""],["fd",j.folder?"📁 "+j.folder:""]].filter(p=>p[1]);
    const head=parts.map(([c,v],i)=>(i?'<span class="sp">|</span>':"")+`<span class="${c}">${esc(v)}</span>`).join("");
    const tip=[`任務 ${j.id}`, j.name?"劇名："+j.name:"", site?"網域："+site:"", j.output?"資料夾："+j.output:""].filter(Boolean).join("\n");
    const top=`<div class="job-h"><button class="fold" data-act="fold" data-id="${j.id}" title="收合／展開">▾</button>${movable?`<span class="grip" draggable="true" data-id="${j.id}" title="拖曳調整順序：越上面越先下載">⠿</span>`:""}<span class="t" title="${esc(tip)}">${head}</span><span class="n">${done}/${total}${failed?`（${failed} 失敗）`:""}</span><span class="badge ${j.state}">${JOB_LABEL[j.state]||j.state}</span></div>
      ${j.state==="failed"&&j.brief?`<div class="job-s">${esc(j.brief)}</div><details class="raw"><summary>原始訊息</summary><pre>${esc(j.message||"")}</pre></details>`:(line?`<div class="job-s">${esc(line)}</div>`:"")}
      <div class="bar"><i style="width:${total?((done+failed+part)/total*100):0}%"></i></div>`;
    let li=have[j.id]; delete have[j.id];
    if(!li){
      li=document.createElement("li"); li.dataset.job=j.id;
      li.innerHTML=`<div class="job-top"></div><div class="job-f"><span class="job-btns" style="display:contents"></span><details data-id="${j.id}" ${state.openJobs.has(j.id)?"open":""}><summary></summary><ul class="tasks"></ul></details></div>`;
    }
    li.className="job "+j.state+(state.foldJobs.has(j.id)?" collapsed":"")+(selJob===j.id?" sel":"");
    setHTML(li.querySelector(".job-top"), top);
    setHTML(li.querySelector(".job-btns"), btns);
    setHTML(li.querySelector(".job-f > details > summary"), `項目（${total}）`);
    // 項目逐列更新（整個清單換掉會讓捲動錨定跑掉）
    const tl=li.querySelector(".job-f .tasks");
    while(tl.children.length>items.length) tl.lastElementChild.remove();
    while(tl.children.length<items.length) tl.appendChild(document.createElement("li"));
    items.forEach((h,i)=>{ const row=tl.children[i]; row.className="task "+j.items[i].state; setHTML(row,h); });
    const want=prev?prev.nextSibling:ul.firstChild; if(li!==want) ul.insertBefore(li,want);
    prev=li;
  });
  Object.values(have).forEach(li=>li.remove());
  updateJobMove(true);
  // 收合記錄只留現有任務
  let pruned=false; state.foldJobs.forEach(id=>{ if(!state.jobs[id]){ state.foldJobs.delete(id); pruned=true; } }); if(pruned) saveFolds();
}
let rjTimer=0; function renderJobsSoon(){ if(!rjTimer) rjTimer=setTimeout(()=>{rjTimer=0;renderJobs();},250); }
$("jobs").addEventListener("toggle",e=>{ const d=e.target; if(d.tagName!=="DETAILS") return; const id=+d.dataset.id; if(d.open) state.openJobs.add(id); else state.openJobs.delete(id); },true);
/* 浮動排序按鈕：點標題列選取任務，按鈕移動它（只在下載中／排隊中的任務之間移動） */
var selJob=0, selScroll=false;
function movableJobs(){ return Object.values(state.jobs).sort((a,b)=>(a.order-b.order)||(a.id-b.id)).filter(j=>j.state==="running"||j.state==="queued"); }
function updateJobMove(rendered){
  const m=movableJobs(), i=m.findIndex(j=>j.id===selJob);
  if(selJob && i<0){ selJob=0; const li=$("jobs").querySelector("li.job.sel"); if(li) li.classList.remove("sel"); }
  $("jobMove").hidden=m.length<2;
  $("jobMove").querySelectorAll("button").forEach(b=>{ const up=b.dataset.move==="top"||b.dataset.move==="up"; b.disabled=i<0||(up?i===0:i===m.length-1); });
  if(rendered && selScroll){ selScroll=false; const li=$("jobs").querySelector(`li.job[data-job="${selJob}"]`); if(li) li.scrollIntoView({block:"nearest"}); }
}
function selectJob(id){
  selJob = selJob===id ? 0 : id;
  $("jobs").querySelectorAll(":scope > li.job").forEach(li=>li.classList.toggle("sel",+li.dataset.job===selJob));
  updateJobMove();
}
$("jobMove").onclick=async e=>{
  const b=e.target.closest("button[data-move]"); if(!b||b.disabled) return;
  const m=movableJobs(), i=m.findIndex(j=>j.id===selJob); if(i<0) return;
  const where=b.dataset.move, t={top:m[0],up:m[i-1],down:m[i+1],bottom:m[m.length-1]}[where];
  if(!t||t.id===selJob) return;
  selScroll=true;
  const err=await goJobMove(selJob,t.id,where==="down"||where==="bottom"); if(err){ selScroll=false; toast(err); }
};
$("jobs").onclick=async e=>{
  const b=e.target.closest("button[data-act]");
  if(!b){ const h=e.target.closest(".job-h"); if(h){ const id=+h.closest("li.job").dataset.job, j=state.jobs[id];
      if(j&&(j.state==="running"||j.state==="queued")) selectJob(id); else if(selJob) selectJob(selJob); } return; }
  const id=+b.dataset.id, act=b.dataset.act, j=state.jobs[id];
  if(act==="fold"){ setFold([id], !state.foldJobs.has(id)); return; }
  if(act==="log"){ const err=await goOpenLogFile(id); if(err) toast(err); return; }
  if(act==="report"){ const msg=await goReportPack(id); if(msg) toast(msg); return; }
  if(act==="feedback"){ if(j) openFeedbackForJob(j); return; }
  if(act==="folder"){ const err=await goOpenFolder(j?j.output:$("output").value); if(err) toast(err); return; }
  if(act==="prioritize"){ const err=await goJobPrioritize(id); if(err) toast(err); return; }
  if(act==="abort" && !confirm(`確定要中止任務 ${id}？已下載完成的檔案會保留，但這個任務不能再繼續。`)) return;
  const err=await goJobAction(id,act); if(err) toast(err);
};
/* 拖曳把手調整任務順序：放在目標卡片的上半部＝排在它前面，下半部＝排在它後面 */
var dragJob=0, dragPending=false;
const clearDrop=()=>{ $("jobs").querySelectorAll(".drop-before,.drop-after,.dragging").forEach(e=>e.classList.remove("drop-before","drop-after","dragging"));
  if(dragPending){ dragPending=false; renderJobs(); } };
const dropTarget=e=>{ const li=e.target.closest("li.job"); if(!li||!dragJob||+li.dataset.job===dragJob) return null;
  const j=state.jobs[+li.dataset.job]; return j&&(j.state==="running"||j.state==="queued")?li:null; };
$("jobs").addEventListener("dragstart",e=>{ const g=e.target.closest&&e.target.closest(".grip"); if(!g) return;
  dragJob=+g.dataset.id; e.dataTransfer.effectAllowed="move"; e.dataTransfer.setData("text/plain",String(dragJob));
  const li=g.closest("li.job"); e.dataTransfer.setDragImage(li,20,20); li.classList.add("dragging"); });
$("jobs").addEventListener("dragover",e=>{ const li=dropTarget(e); if(!li) return; e.preventDefault();
  const r=li.getBoundingClientRect(), after=e.clientY>r.top+r.height/2;
  $("jobs").querySelectorAll(".drop-before,.drop-after").forEach(x=>{ if(x!==li) x.classList.remove("drop-before","drop-after"); });
  li.classList.toggle("drop-after",after); li.classList.toggle("drop-before",!after); });
$("jobs").addEventListener("drop",async e=>{ const li=dropTarget(e); if(!li) return; e.preventDefault();
  const after=li.classList.contains("drop-after"), id=dragJob; dragJob=0; clearDrop();
  const err=await goJobMove(id,+li.dataset.job,after); if(err) toast(err); });
$("jobs").addEventListener("dragend",()=>{ dragJob=0; clearDrop(); });
$("btnClearDone").onclick=()=>goClearFinished();
/* 收合／展開：記在本機，重開程式沿用 */
function saveFolds(){ uiSet("foldJobs", state.foldJobs.size?[...state.foldJobs]:null); }
function setFold(ids, on){ ids.forEach(id=>on?state.foldJobs.add(id):state.foldJobs.delete(id)); saveFolds();
  ids.forEach(id=>{ const li=$("jobs").querySelector(`li.job[data-job="${id}"]`); if(li) li.classList.toggle("collapsed",on); }); }
$("btnFoldAll").onclick=()=>setFold(Object.keys(state.jobs).map(Number), true);
$("btnUnfoldAll").onclick=()=>setFold(Object.keys(state.jobs).map(Number), false);
$("btnGroupJobs").onclick=async()=>{ if(Object.keys(state.jobs).length<2){ toast("任務少於 2 個，不需要整理"); return; } await goJobsGroup(); toast("已把同一個網站的任務排在一起"); };

