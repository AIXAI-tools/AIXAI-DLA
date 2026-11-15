/* ---------------- layout: column splitter + section heights ---------------- */
// uiState.layout = {ratio: 左欄占比, h: {區塊: 高度px}}；「恢復預設版位」或雙擊分隔條清除
const COL_MIN=[380,360];
const V_SECTIONS={jobs:{min:160,cap:true},fails:{min:100,cap:true},log:{min:120}};
let layout={};
const saveLayout=()=>uiSet("layout", Object.keys(layout).length?layout:null);
// 程式啟動時套用存下的版面與收合（boot 讀到 uiState 之後）
function restoreView(){
  layout=(uiState.layout&&typeof uiState.layout==="object")?uiState.layout:{};
  state.foldJobs=new Set((Array.isArray(uiState.foldJobs)?uiState.foldJobs:[]).map(Number));
  applyCols(); Object.keys(V_SECTIONS).forEach(applyHeight);
}
function applyCols(){
  const r=layout.ratio;
  // fr 乘 100：比例總和小於 1 時，一欄被最小寬度卡住後另一欄不會吃滿剩下的寬度
  if(typeof r==="number" && r>0 && r<1) $("main").style.setProperty("--cols",`minmax(${COL_MIN[0]}px,${(r*100).toFixed(2)}fr) 18px minmax(${COL_MIN[1]}px,${((1-r)*100).toFixed(2)}fr)`);
  else $("main").style.removeProperty("--cols");
}
function applyHeight(id){
  const el=$(id), h=(layout.h||{})[id];
  if(h){ el.style.height=Math.max(h,V_SECTIONS[id].min)+"px"; if(V_SECTIONS[id].cap) el.style.maxHeight="none"; }
  else { el.style.height=""; el.style.maxHeight=""; }
}
function resetLayout(){ layout={}; saveLayout(); applyCols(); Object.keys(V_SECTIONS).forEach(applyHeight); toast("已恢復預設版位"); }
(()=>{
  const sp=$("splitter"), main=$("main");
  sp.addEventListener("pointerdown",e=>{
    if(e.button!==0) return; e.preventDefault(); sp.setPointerCapture(e.pointerId);
    const left=main.querySelector(".left-col"), right=main.querySelector(".right-col");
    const x0=e.clientX, l0=left.getBoundingClientRect().width, total=l0+right.getBoundingClientRect().width;
    sp.classList.add("active"); document.body.classList.add("resizing");
    const move=ev=>{ const l=Math.min(Math.max(l0+ev.clientX-x0, COL_MIN[0]), total-COL_MIN[1]); layout.ratio=l/total; applyCols(); };
    const up=()=>{ sp.removeEventListener("pointermove",move); sp.removeEventListener("pointerup",up); sp.removeEventListener("pointercancel",up);
      sp.classList.remove("active"); document.body.classList.remove("resizing"); saveLayout(); };
    sp.addEventListener("pointermove",move); sp.addEventListener("pointerup",up); sp.addEventListener("pointercancel",up);
  });
  sp.addEventListener("dblclick",()=>{ delete layout.ratio; saveLayout(); applyCols(); toast("左右寬度已恢復預設"); });
  Object.keys(V_SECTIONS).forEach(id=>{
    const el=$(id), g=document.createElement("div");
    // 任務清單旁有排序按鈕欄：拉把放在兩者外層的下面
    g.className="vgrip"; g.title="拖曳調整高度；雙擊恢復預設"; (el.parentElement.classList.contains("jobs-wrap")?el.parentElement:el).after(g);
    g.addEventListener("pointerdown",e=>{
      if(e.button!==0) return; e.preventDefault(); g.setPointerCapture(e.pointerId);
      const y0=e.clientY, h0=el.getBoundingClientRect().height;
      g.classList.add("active"); document.body.classList.add("resizing-v");
      const move=ev=>{ layout.h=layout.h||{}; layout.h[id]=Math.round(Math.max(h0+ev.clientY-y0, V_SECTIONS[id].min)); applyHeight(id); };
      const up=()=>{ g.removeEventListener("pointermove",move); g.removeEventListener("pointerup",up); g.removeEventListener("pointercancel",up);
        g.classList.remove("active"); document.body.classList.remove("resizing-v"); saveLayout(); };
      g.addEventListener("pointermove",move); g.addEventListener("pointerup",up); g.addEventListener("pointercancel",up);
    });
    g.addEventListener("dblclick",()=>{ if(layout.h){ delete layout.h[id]; saveLayout(); } applyHeight(id); });
  });
  $("btnLayoutReset").onclick=resetLayout;
})();
$("btnPauseAll").onclick=async()=>{ const n=await goPauseAll(); toast(n?`已暫停 ${n} 個任務`:"沒有下載中或排隊中的任務"); };
$("btnResumeAll").onclick=async()=>{ const n=await goResumeAll(); toast(n?`已繼續 ${n} 個任務`:"沒有已暫停的任務"); };
function showBanner(kind,msg,acts){ const b=$("banner"); b.className="banner show "+kind; b.textContent=msg; if(acts){const d=document.createElement("div");d.className="acts";acts.forEach(a=>d.appendChild(a));b.appendChild(d);} }
function hideBanner(){ $("banner").className="banner"; }
function mkBtn(label,fn){ const b=document.createElement("button"); b.className="btn sm"; b.textContent=label; b.onclick=fn; return b; }

/* Failure reason: a short Chinese line, with the original message kept below. */
function reasonHTML(brief, raw){
  raw=raw||""; if(!raw && !brief) return "";
  if(!brief) return `<div class="why" title="${esc(raw)}">${esc(raw)}</div>`;
  return `<div class="why">${esc(brief)}</div>`+(raw?`<details class="raw"><summary>原始訊息</summary><pre>${esc(raw)}</pre></details>`:"");
}

