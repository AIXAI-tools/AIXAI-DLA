/* ---------------- terms / disclaimer ---------------- */
const TERMS_VERSION = "2026-10-28";
function openTerms(required){
  const d=$("dlgTerms"); d.dataset.required = required ? "1" : "";
  $("termsVer").textContent = "版本 " + TERMS_VERSION;
  $("consentRow").classList.toggle("hidden", !required);
  $("btnTermsAccept").classList.toggle("hidden", !required);
  $("btnTermsDecline").classList.toggle("hidden", !required);
  $("btnTermsClose").classList.toggle("hidden", required);
  $("termsCheck").checked=false; $("btnTermsAccept").disabled=true;
  d.showModal(); d.querySelector(".dlg-b").scrollTop=0;
}
$("dlgTerms").addEventListener("cancel", e=>{ if($("dlgTerms").dataset.required) e.preventDefault(); }); // Esc cannot skip consent
$("termsCheck").onchange=()=>{ $("btnTermsAccept").disabled=!$("termsCheck").checked; };
$("btnTermsAccept").onclick=async()=>{ await goAcceptDisclaimer(TERMS_VERSION); $("dlgTerms").dataset.required=""; $("dlgTerms").close(); $("urls").focus(); };
$("btnTermsDecline").onclick=()=>goQuit();
$("btnTermsClose").onclick=()=>$("dlgTerms").close();
$("btnTerms").onclick=()=>openTerms(false);

/* ---------------- boot ---------------- */
(async function boot(){
  applyTheme();
  for(let i=0;i<50 && !window.goInit;i++) await new Promise(r=>setTimeout(r,20));
  const init=await goInit(); state.init=init;
  try { uiState=JSON.parse(init.uiState||"{}")||{}; } catch { uiState={}; }
  applyTheme(); restoreView(); fillForm(init);
  if(init.settings.disclaimer_accepted !== TERMS_VERSION) openTerms(true); else $("urls").focus();
  if(!init.settings.no_startup_update_check) goCheckUpdates(); // a newer release opens the 版本 window (renderReleases)
  // Many people leave the program open for days: check again every 6 hours,
  // counted from this program's own start (not at the same time for everyone).
  setInterval(()=>{ if($("startupUpdCheck").checked && !$("dlgUpdate").open) goCheckUpdates(); },6*3600*1000);
})();

/* ---------------- 輸入框右鍵選單 ---------------- */
// WebView2 套件關掉了預設右鍵選單，輸入框改用自己的剪下／複製／貼上／全選
let ctxField=null;
const ctxEditable=el=>!!el && !el.disabled && (el.tagName==="TEXTAREA" || (el.tagName==="INPUT" && /^(text|search|url|email)$/.test(el.type)));
function closeCtx(){ $("ctxMenu").classList.remove("open"); ctxField=null; }
document.addEventListener("contextmenu",e=>{
  e.preventDefault();
  const f=e.target.closest("input,textarea");
  if(!ctxEditable(f)){ closeCtx(); return; }
  ctxField=f; f.focus();
  const m=$("ctxMenu"); (f.closest("dialog[open]")||document.body).appendChild(m); // 對話框開著時要放進對話框才不會被蓋住
  const sel=f.selectionEnd>f.selectionStart;
  m.querySelector('[data-c="cut"]').disabled=f.readOnly||!sel;
  m.querySelector('[data-c="copy"]').disabled=!sel;
  m.querySelector('[data-c="paste"]').disabled=f.readOnly;
  m.classList.add("open");
  const r=m.getBoundingClientRect();
  m.style.left=Math.max(4,Math.min(e.clientX,innerWidth-r.width-4))+"px";
  m.style.top=Math.max(4,Math.min(e.clientY,innerHeight-r.height-4))+"px";
});
$("ctxMenu").addEventListener("mousedown",e=>e.preventDefault()); // 保留輸入框的焦點與選取範圍
$("ctxMenu").onclick=async e=>{
  const b=e.target.closest("button"), f=ctxField; closeCtx();
  if(!b||!f) return;
  const start=f.selectionStart, end=f.selectionEnd, sel=f.value.slice(start,end);
  const back=()=>{ f.focus(); f.setSelectionRange(start,end); }; // copyText 的備用做法會移走焦點
  switch(b.dataset.c){
    case "all": f.focus(); f.select(); break;
    case "copy": await copyText(sel); back(); break;
    case "cut": if(await copyText(sel)){ back(); document.execCommand("delete"); } break;
    case "paste": {
      let t=""; try{ t=await goClipboardText(); }catch(_){} // 介面不是安全來源，沒有 navigator.clipboard
      f.focus();
      if(t) document.execCommand("insertText",false,t); // 會觸發 input 事件，也能用 Ctrl+Z 復原
      else toast("剪貼簿沒有文字；也可以按 Ctrl+V 貼上。");
    }
  }
};
document.addEventListener("click",e=>{ if(!e.target.closest("#ctxMenu")) closeCtx(); });
document.addEventListener("keydown",e=>{ if(e.key==="Escape" && ctxField){ e.preventDefault(); closeCtx(); } });
addEventListener("blur",closeCtx);
addEventListener("resize",closeCtx);
document.addEventListener("scroll",closeCtx,true);
