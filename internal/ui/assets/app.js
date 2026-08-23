import { loadModels as fetchModels, loadStatus as fetchStatus, sendMessages } from "./api.js";
import { parseCodeFences } from "./code.js";
import { appendTurn, conversationTitle, conversationTurnText, createConversation, exportConversation, toRequestMessages } from "./conversation.js";
import { disclosureNarrative } from "./disclosure.js";
import { ConversationLifecycle } from "./lifecycle.js";
import { buildIdentityLabel, humanize, modelFacts, normalizeCatalog, relayVerificationLabel } from "./models.js";
import { connectionSnippets } from "./snippets.js";
import { readProviderTextStream } from "./sse.js";
import { conversationStore } from "./storage.js";

(function(){
"use strict";
var PREFIX="/_osanwe/";

var $=function(id){return document.getElementById(id)};
var thread=$("thread"),rail=$("rail"),opening=$("opening"),
    input=$("input"),send=$("send"),stop=$("stop"),seal=$("seal"),state=$("state"),model=$("model"),
    providerKeyInput=$("providerKey"),providerConsent=$("providerConsent"),
    panel=$("panel"),veil=$("veil"),providerSettings=$("providerSettings"),runnerFrame=$("runnerPreview");

var status=null,busy=false,stopping=false,broken=false,activeRequest=null,dialogOpener=null,catalogModels=[],modelsReady=false,preferredModel="",
    providerKey="",activeMode="chat",runnerOpen=false,runnerChannel="",runnerLines=[],runnerBusy=false,
    pendingRunnerRun=null,runnerStartupTimer=null,
    completedRequestHere=false,catalogRequest=0,transitionTail=Promise.resolve(),lifecycle=new ConversationLifecycle(),
    retentionMode="ephemeral",store=conversationStore("ephemeral"),
    conversation=createConversation({model:model.value});
var modeConversations={chat:conversation,code:createConversation({model:model.value})};
var modeCopy={
  chat:{kicker:"Private chat",title:"What are you thinking about?",placeholder:"Ask anything",assistant:"Osanwë",system:""},
  code:{
    kicker:"Code assistant",title:"What should we build?",placeholder:"Describe a coding task",assistant:"Osanwë Code",
    system:"You are Osanwë Code, a focused coding assistant. Help analyze, write, review, debug, and explain code. You cannot access the user's local files, run commands, or apply changes, so state that limitation whenever it matters. Prefer concise, directly usable code or patches. When JavaScript or HTML can demonstrate the answer, use a fenced code block. JavaScript may include tests with test(name, fn) and assert(condition, message) for the local sandbox."
  }
};
document.body.dataset.mode=activeMode;
try{preferredModel=localStorage.getItem("osanwe-model")||""}catch(e){}
try{if(localStorage.getItem("osanwe-retention")==="device")retentionMode="device"}catch(e){}
if(retentionMode==="device"){
  try{store=conversationStore("device")}catch(e){storageFailed(e)}
}

// ---- status ---------------------------------------------------------
function load(){
  return fetchStatus()
    .then(function(s){status=s;render();return s})
    .catch(function(e){
      // The client is what serves this page, so failing to reach it means it
      // is going away. Saying so beats showing stale numbers as current.
      state.textContent="Client unreachable";state.classList.add("warn");
    });
}

function plural(n,word){return n+" "+word+(n===1?"":"s")}

function render(){
  if(!status)return;
  var tokens=status.paying==="tokens";
	var chatAvailable=(tokens||providerKey)&&modelsReady&&catalogModels.some(function(item){return item.id===model.value});

  $("endpointText").textContent="http://"+status.endpoint;
  $("copyEndpoint").dataset.copy="http://"+status.endpoint;
  $("upstreamText").textContent=status.upstream;
  showSnippet(currentSnip);

  providerSettings.hidden=tokens;
  $("openProviderSettings").hidden=tokens||Boolean(providerKey);
  $("codeContext").hidden=activeMode!=="code";
  $("codeRunner").hidden=activeMode!=="code"||!runnerOpen;
  input.disabled=!chatAvailable;
	input.placeholder=!tokens&&!providerKey?"Connect a provider in Settings":(!modelsReady?"Checking active models":"No active model available");
	if(chatAvailable)input.placeholder=modeCopy[activeMode].placeholder;
  $("modeKicker").textContent=modeCopy[activeMode].kicker;
  $("openingTitle").textContent=modeCopy[activeMode].title;
  if(!tokens&&!providerKey){
    $("openingSub").textContent="Connect a session-only provider key in Settings to begin.";
  }else if(!chatAvailable){
    $("openingSub").textContent="The gateway currently reports no active model. No request can be sent.";
  }else if(activeMode==="code"){
    $("openingSub").textContent=localHistoryDescription()+" Code mode can reason about text you provide, but it cannot access your files or terminal yet.";
  }else if(tokens){
    $("openingSub").textContent=localHistoryDescription()+" Nothing leaves this device unsealed.";
  }else{
    $("openingSub").textContent=localHistoryDescription()+" Your provider account remains visible to the provider.";
  }

  $("walletBlock").hidden=!tokens||!status.wallet;
  if(status.wallet){
    $("walletOnHand").textContent=status.wallet.on_hand;
    $("walletUnit").textContent="on hand · buys "+plural(status.wallet.on_hand,"request");
    $("walletSpent").textContent=status.wallet.spent;
  }

  var rows=[];
  if(status.relay){
    rows.push(["Relay",status.relay.nickname||status.relay.address]);
	rows.push(["Key",relayVerificationLabel(status)]);
    if(status.relay.since_seconds)rows.push(["In use for",humanAge(status.relay.since_seconds)]);
  }else{
    rows.push(["Relay","none chosen yet"]);
  }
  if(status.directory){
    rows.push(["Directory",plural(status.directory.signed_by,"authority")+" signed the list in force"]);
    rows.push(["Relays known",String(status.directory.relays_known)]);
  }
  rows.push(["Client build",buildIdentityLabel(status.build)]);
  rows.push(["Paying with",status.paying]);
  rows.push(["Daemon history",status.retained]);

  var list=$("connList");list.textContent="";
  rows.forEach(function(r){
    var d=document.createElement("div");
    var k=document.createElement("span");k.textContent=r[0];
    var v=document.createElement("b");v.textContent=r[1];
    d.appendChild(k);d.appendChild(v);list.appendChild(d);
  });

  $("connAside").textContent=status.directory
    ? "The relay stays put until it stops answering. Rotating for its own sake spreads the same knowledge across more parties rather than dividing it."
    : "This relay was pinned by hand, so there is no directory behind it and nothing to fail over to.";

  if(catalogModels.length)renderModelCards();

  refresh();
}

function humanAge(s){
  if(s<90)return plural(Math.round(s),"second");
  if(s<5400)return plural(Math.round(s/60),"minute");
  if(s<172800)return plural(Math.round(s/3600),"hour");
  return plural(Math.round(s/86400),"day");
}

// ---- the disclosure -------------------------------------------------
function fillPanel(){
  var told=$("told");told.textContent="";
  var lines=disclosureNarrative(status,completedRequestHere);

  lines.forEach(function(l){
    var li=document.createElement("li");
    if(l[0])li.appendChild(document.createTextNode(l[0]));
    var s=document.createElement("span");s.textContent=l[1];li.appendChild(s);
    told.appendChild(li);
  });

  var f=$("facts");f.textContent="";
  var facts=[];
  if(status&&status.relay)facts.push(["relay",status.relay.nickname||status.relay.address]);
	if(status&&status.relay)facts.push(["relay check",relayVerificationLabel(status)]);
  if(status&&status.directory)facts.push(["signed by",status.directory.signed_by+" authorities"]);
  if(status)facts.push(["paying",status.paying]);
  if(status)facts.push(["daemon retained",status.retained]);
  facts.push(["browser history",retentionMode==="device"?"saved on this device":"ephemeral"]);
  if(status&&status.privacy){
    facts.push(["gateway",humanize(status.privacy.gateway_content_access)]);
    facts.push(["operators",humanize(status.privacy.operator_separation)]);
    facts.push(["server history",humanize(status.privacy.conversation_history)]);
  }
  facts.forEach(function(p){
    var k=document.createElement("span");k.className="k";k.textContent=p[0];
    var v=document.createElement("b");v.textContent=p[1];
    f.appendChild(k);f.appendChild(v);f.appendChild(document.createElement("br"));
  });
}

function openPanel(o){
  if(o){
    fillPanel();dialogOpener=document.activeElement;
    panel.hidden=false;veil.hidden=false;
  }
  panel.classList.toggle("open",o);veil.classList.toggle("open",o);
  seal.setAttribute("aria-expanded",String(o));
  document.querySelectorAll(".chrome,.view").forEach(function(node){node.inert=o});
  if(o){
    $("closePanel").focus();
  }else{
    panel.hidden=true;veil.hidden=true;
    var target=dialogOpener&&dialogOpener.isConnected?dialogOpener:seal;
    dialogOpener=null;target.focus();
  }
}

function keepDialogFocus(e){
  if(e.key!=="Tab"||!panel.classList.contains("open"))return;
  var controls=Array.from(panel.querySelectorAll("button,[href],input,select,textarea,[tabindex]:not([tabindex='-1'])"))
    .filter(function(node){return !node.disabled&&!node.hidden});
  if(!controls.length){e.preventDefault();panel.focus();return}
  var first=controls[0],last=controls[controls.length-1];
  if(e.shiftKey&&document.activeElement===first){e.preventDefault();last.focus()}
  else if(!e.shiftKey&&document.activeElement===last){e.preventDefault();first.focus()}
}

// ---- chat -----------------------------------------------------------
function autosize(){input.style.height="auto";input.style.height=Math.min(input.scrollHeight,128)+"px"}
function scroll(){thread.scrollTop=thread.scrollHeight}
function setEmpty(is){thread.classList.toggle("is-empty",is);opening.hidden=!is}

function stopActiveRequest(){
  var request=activeRequest;
  if(!request)return Promise.resolve();
  if(!request.controller.signal.aborted){stopping=true;refresh();request.controller.abort()}
  return request.done.catch(function(){return undefined});
}

function runTransition(operation){
  var result=transitionTail.then(operation,operation);
  transitionTail=result.catch(function(){return undefined});
  return result;
}

function turn(kind,label){
  var d=document.createElement("div");d.className="turn "+kind;
  var h=document.createElement("div");h.className="who";h.textContent=label;
  var b=document.createElement("div");b.className="body";
  d.appendChild(h);d.appendChild(b);rail.appendChild(d);return b;
}

function renderAssistantContent(body,text,withRunner){
  body.textContent="";
  parseCodeFences(text).forEach(function(part){
    if(part.kind==="text"){
      var span=document.createElement("span");span.className="assistant-text";span.textContent=part.content;body.appendChild(span);return;
    }
    var block=document.createElement("section");block.className="generated-code";
    var head=document.createElement("div");head.className="generated-code-head";
    var label=document.createElement("span");label.textContent=part.language||"code";head.appendChild(label);
    if(withRunner&&part.runnerLanguage){
      var loadButton=document.createElement("button");loadButton.type="button";loadButton.textContent="Load into runner";
      loadButton.addEventListener("click",function(){loadRunnerCode(part.runnerLanguage,part.content)});
      head.appendChild(loadButton);
    }
    var pre=document.createElement("pre"),code=document.createElement("code");
    code.textContent=part.content;pre.appendChild(code);block.appendChild(head);block.appendChild(pre);body.appendChild(block);
  });
}

function refresh(){
  var tokens=status&&status.paying==="tokens";
	var hasCredential=tokens||Boolean(providerKey);
	var activeModel=catalogModels.some(function(item){return item.id===model.value});
  send.hidden=busy;stop.hidden=!busy;
  stop.disabled=!busy||stopping;
  send.disabled=busy||!hasCredential||!modelsReady||!activeModel||!input.value.trim();
  rail.setAttribute("aria-busy",String(busy));
  if(broken){state.textContent="Seal broken";state.classList.add("warn");return}
  state.classList.remove("warn");
	state.textContent=busy?(stopping?"Stopping":"Answering"):(hasCredential?(modelsReady&&activeModel?"Ready":(modelsReady?"No active models":"Checking models")):"Key needed");
}

function fail(body,message){
  broken=true;seal.classList.add("broken");
  body.parentElement.className="turn err";
  body.textContent=message;
  refresh();scroll();
}

function submit(){
  var text=input.value.trim();
  var tokens=status&&status.paying==="tokens";
  if(!text||busy||!status||(!tokens&&!providerKey)||!modelsReady||
	!catalogModels.some(function(item){return item.id===model.value}))return;

  setEmpty(false);
  var requestConversation=conversation;
  var requestMode=activeMode;
  appendTurn(requestConversation,"user",text);
  persistConversation(requestConversation);
  turn("you","You").textContent=text;
  input.value="";autosize();scroll();

  broken=false;seal.classList.remove("broken","pressing");
  void seal.offsetWidth;seal.classList.add("pressing");

  var reply=appendTurn(requestConversation,"assistant","",{status:"streaming"});
  var body=turn("reply",modeCopy[requestMode].assistant);
  var caret=document.createElement("span");caret.className="caret";
  body.appendChild(caret);
  var request={controller:new AbortController(),conversation:requestConversation,done:null};
  activeRequest=request;
	busy=true;stopping=false;refresh();
  request.done=sendMessages({
    model:model.value,
    system:modeCopy[requestMode].system,
    messages:toRequestMessages(requestConversation)
  },{
    signal:request.controller.signal,
    apiStyle:status.api_style||"anthropic",
    apiKey:tokens?"":providerKey
  }).then(function(resp){
    return stream(resp,body,caret,reply,requestConversation,requestMode);
  }).catch(function(e){
    if(e.name==="AbortError"){
      reply.status="stopped";caret.remove();
      body.textContent=conversationTurnText(reply);
	  return persistConversation(requestConversation);
    }
    reply.status="error";reply.content=e.message||String(e);
    caret.remove();
    fail(body,reply.content);return persistConversation(requestConversation);
  }).finally(function(){
	if(activeRequest===request){busy=false;stopping=false;activeRequest=null;refresh();input.focus()}
    // Spending a token changes the wallet, so the numbers behind Connect are
    // stale the moment a request finishes.
    Promise.all([load(),loadModels()]);
  });
}

// stream reads server-sent events and appends only the text deltas.
//
// Everything else on the wire -- message_start, usage records, ping -- carries
// no words. Appending them would put JSON in the middle of a sentence.
function stream(resp,body,caret,reply,requestConversation,requestMode){
  return readProviderTextStream(resp.body,function(text){
	reply.content+=text;
	caret.remove();
	body.appendChild(document.createTextNode(text));
	body.appendChild(caret);
	scroll();
  }).then(function(){
	reply.status="complete";
	caret.remove();
	if(requestMode==="code")renderAssistantContent(body,reply.content,true);
	completedRequestHere=true;
	return persistConversation(requestConversation);
  });
}

// ---- snippets -------------------------------------------------------
var currentSnip="shell";
function showSnippet(name){
  currentSnip=name;
  var s=connectionSnippets({
    endpoint:status?status.endpoint:"127.0.0.1:8080",
    paying:status?status.paying:"tokens",
    model:model.value||"MODEL_FROM_LIVE_CATALOG"
  })[name];
  $("snippet").textContent=s.code;$("snipNote").textContent=s.note;
}

// ---- local code runner ----------------------------------------------
function showRunnerView(name){
  var preview=name==="preview";
  $("runnerPreview").hidden=!preview;$("runnerResults").hidden=preview;
  document.querySelectorAll("[data-runner-view]").forEach(function(button){
    var selected=button.dataset.runnerView===name;
    button.setAttribute("aria-selected",String(selected));button.tabIndex=selected?0:-1;
  });
}

function resetRunnerFrame(reason){
  runnerChannel="";runnerBusy=false;pendingRunnerRun=null;$("runCode").disabled=false;
  if(runnerStartupTimer)window.clearTimeout(runnerStartupTimer);runnerStartupTimer=null;
  runnerFrame.onload=null;
  runnerFrame.src=PREFIX+"assets/runner.html?idle="+encodeURIComponent(String(Date.now()));
  if(reason)$("runnerStatus").textContent=reason;
}

function setRunnerOpen(open){
  runnerOpen=open;
  if(!open)resetRunnerFrame("Runner stopped. Loaded code remains in the editor.");
  render();
  if(open)$("runnerEditor").focus();
}

function loadRunnerCode(language,code){
  $("runnerLanguage").value=language;$("runnerEditor").value=code;
  runnerLines=[];$("runnerResults").textContent="No output yet.";
  $("runnerStatus").classList.remove("warn");$("runnerStatus").textContent="Loaded. Review the code, then choose Run & test.";
  runnerOpen=true;render();$("runnerEditor").focus();
}

function addRunnerLine(kind,text){
  var prefix={log:"LOG",info:"INFO",warn:"WARN",error:"ERROR",test:"TEST",preview:"PREVIEW",complete:"DONE"}[kind]||"OUTPUT";
  runnerLines.push(prefix+"  "+String(text).slice(0,4000));
  if(runnerLines.length>200)runnerLines=runnerLines.slice(-200);
  $("runnerResults").textContent=runnerLines.join("\n")||"No output yet.";
}

function runEditorCode(){
  var code=$("runnerEditor").value,language=$("runnerLanguage").value;
  if(!code.trim()){
    $("runnerStatus").textContent="Add code before running.";$("runnerStatus").classList.add("warn");$("runnerEditor").focus();return;
  }
  runnerChannel=crypto.randomUUID();runnerLines=[];runnerBusy=true;
  $("runnerResults").textContent="Waiting for output…";$("runnerStatus").textContent="Running in the isolated sandbox…";
  $("runnerStatus").classList.remove("warn");$("runCode").disabled=true;
  showRunnerView(language==="html"?"preview":"results");
  pendingRunnerRun={type:"osanwe-run",channel:runnerChannel,language:language,code:code};
  var expectedChannel=runnerChannel;
  runnerStartupTimer=window.setTimeout(function(){
    if(!pendingRunnerRun||pendingRunnerRun.channel!==expectedChannel)return;
    pendingRunnerRun=null;runnerBusy=false;$("runCode").disabled=false;
    $("runnerStatus").textContent="The sandbox did not start. Try running again.";$("runnerStatus").classList.add("warn");
  },1500);
  runnerFrame.src=PREFIX+"assets/runner.html?run="+encodeURIComponent(expectedChannel);
}

window.addEventListener("message",function(event){
  var message=event.data;
  if(event.source!==runnerFrame.contentWindow||!message)return;
  if(message.type==="osanwe-runner-ready"){
    if(!pendingRunnerRun)return;
    if(runnerStartupTimer)window.clearTimeout(runnerStartupTimer);runnerStartupTimer=null;
    runnerFrame.contentWindow.postMessage(pendingRunnerRun,"*");pendingRunnerRun=null;return;
  }
  if(message.type!=="osanwe-runner"||message.channel!==runnerChannel)return;
  if(["log","info","warn","error","test","preview","complete"].indexOf(message.kind)<0)return;
  addRunnerLine(message.kind,message.text||"");
  if(message.kind==="error")$("runnerStatus").classList.add("warn");
  if(message.kind==="preview")$("runnerStatus").textContent=message.text||"Preview ready.";
  if(message.kind==="complete"){
    runnerBusy=false;$("runCode").disabled=false;
    var failed=Number.isSafeInteger(message.failed)?message.failed:0;
    var tests=Number.isSafeInteger(message.testCount)?message.testCount:0;
    if(message.timedOut){$("runnerStatus").textContent="Stopped at the 2.5 second limit.";$("runnerStatus").classList.add("warn")}
    else if(failed){$("runnerStatus").textContent=message.text||"One or more tests failed.";$("runnerStatus").classList.add("warn")}
    else if(tests){$("runnerStatus").textContent=message.text||"All declared tests passed."}
    else if($("runnerLanguage").value==="javascript"){$("runnerStatus").textContent="Run completed. No tests were declared."}
  }
});

$("openCodeRunner").addEventListener("click",function(){setRunnerOpen(true)});
$("closeCodeRunner").addEventListener("click",function(){setRunnerOpen(false)});
$("runCode").addEventListener("click",runEditorCode);
$("clearRunner").addEventListener("click",function(){
  $("runnerEditor").value="";runnerLines=[];$("runnerResults").textContent="No output yet.";$("runnerStatus").classList.remove("warn");
  resetRunnerFrame("Cleared. Nothing has run.");$("runnerEditor").focus();
});
document.querySelectorAll("[data-runner-view]").forEach(function(button){button.addEventListener("click",function(){showRunnerView(button.dataset.runnerView)})});

// ---- wiring ---------------------------------------------------------
function connectProviderKey(){
  if(!status||status.paying==="tokens")return;
  var candidate=providerKeyInput.value.trim();
  if(!providerConsent.checked){
    $("providerKeyStatus").textContent="Confirm the non-sensitive test boundary before connecting.";
    providerConsent.focus();return;
  }
  if(!candidate||/[\r\n\0\s]/.test(candidate)){
    $("providerKeyStatus").textContent="Paste one API key without spaces or line breaks.";
    providerKeyInput.focus();return;
  }
  providerKey=candidate;
  providerKeyInput.value="";
  providerKeyInput.disabled=true;providerConsent.disabled=true;
  $("connectProviderKey").hidden=true;$("forgetProviderKey").hidden=false;
  $("providerKeyStatus").textContent="Connected for this page only. Reload or choose Forget key to clear it.";
  render();$("settingsDialog").close();input.focus();
}

function forgetProviderKey(){
  providerKey="";providerKeyInput.value="";providerKeyInput.disabled=false;providerConsent.disabled=false;
  $("connectProviderKey").hidden=false;$("forgetProviderKey").hidden=true;
  $("providerKeyStatus").textContent="The page released its reference to the provider key.";
  render();providerKeyInput.focus();
}

$("connectProviderKey").addEventListener("click",connectProviderKey);
$("forgetProviderKey").addEventListener("click",forgetProviderKey);
$("openProviderSettings").addEventListener("click",openSettings);
providerKeyInput.addEventListener("keydown",function(e){if(e.key==="Enter"){e.preventDefault();connectProviderKey()}});
window.addEventListener("pagehide",function(){providerKey="";providerKeyInput.value=""});
input.addEventListener("input",function(){autosize();refresh()});
input.addEventListener("keydown",function(e){
  if(e.key==="Enter"&&!e.shiftKey){e.preventDefault();submit()}
});
send.addEventListener("click",submit);
stop.addEventListener("click",function(){
	if(!busy||!activeRequest||stopping)return;
	stopping=true;refresh();activeRequest.controller.abort();
});
seal.addEventListener("click",function(){openPanel(!panel.classList.contains("open"))});
$("closePanel").addEventListener("click",function(){openPanel(false)});
veil.addEventListener("click",function(){openPanel(false)});
document.addEventListener("keydown",function(e){
  if(e.key==="Escape"&&panel.classList.contains("open"))openPanel(false);
  keepDialogFocus(e);
});

$("newBtn").addEventListener("click",function(){
  runTransition(async function(){
    await stopActiveRequest();await lifecycle.idle();
    conversation=createConversation({model:model.value});
    modeConversations[activeMode]=conversation;
    rail.querySelectorAll(".turn").forEach(function(n){n.remove()});
    broken=false;seal.classList.remove("broken");
    setEmpty(true);input.focus();refresh();
  }).catch(storageFailed);
});

document.querySelectorAll("[data-mode]").forEach(function(btn){
  btn.addEventListener("click",function(){
    var want=btn.dataset.mode;
    if((want!=="chat"&&want!=="code")||want===activeMode)return;
    runTransition(async function(){
      await stopActiveRequest();await lifecycle.idle();
      modeConversations[activeMode]=conversation;
      activeMode=want;conversation=modeConversations[want];conversation.model=model.value;
      if(activeMode!=="code"&&runnerOpen){runnerOpen=false;resetRunnerFrame("Runner stopped. Loaded code remains in the editor.")}
      document.body.dataset.mode=activeMode;
      document.querySelectorAll("[data-mode]").forEach(function(b){
        b.setAttribute("aria-selected",String(b===btn));
        b.tabIndex=b===btn?0:-1;
      });
      $("chatView").setAttribute("aria-labelledby",btn.id);
      renderConversation();render();input.focus();
    }).catch(storageFailed);
  });
});

document.querySelectorAll("[data-snip]").forEach(function(btn){
  btn.addEventListener("click",function(){
    document.querySelectorAll("[data-snip]").forEach(function(b){
      b.setAttribute("aria-selected",String(b===btn));
      b.tabIndex=b===btn?0:-1;
    });
    $("snippet").setAttribute("aria-labelledby",btn.id);
    showSnippet(btn.dataset.snip);
  });
});

function wireTabKeys(list){
  list.addEventListener("keydown",function(e){
    var tabs=Array.from(list.querySelectorAll("[role='tab']:not(:disabled)"));
    var current=tabs.indexOf(document.activeElement),next=current;
    if(e.key==="Home")next=0;
    else if(e.key==="End")next=tabs.length-1;
    else if(e.key==="ArrowRight")next=(current+1)%tabs.length;
    else if(e.key==="ArrowLeft")next=(current-1+tabs.length)%tabs.length;
    else return;
    e.preventDefault();tabs[next].focus();tabs[next].click();
  });
}
document.querySelectorAll("[role='tablist']").forEach(wireTabKeys);

$("copyEndpoint").addEventListener("click",function(){
  var btn=this,feedback=function(message,ok){
    btn.textContent=message;btn.classList.toggle("done",ok);
    setTimeout(function(){btn.textContent="Copy";btn.classList.remove("done")},1400);
  };
  var failed=function(){
    var range=document.createRange(),selection=window.getSelection();
    range.selectNodeContents($("endpointText"));selection.removeAllRanges();selection.addRange(range);
    feedback("Copy failed",false);
  };
  if(navigator.clipboard&&navigator.clipboard.writeText){
    navigator.clipboard.writeText(btn.dataset.copy).then(function(){feedback("Copied",true)},failed);
  }else{failed()}
});

// ---- what this gateway actually carries -----------------------------
//
// The list is asked for rather than assumed. A hardcoded picker offering a
// model the gateway does not route would spend nothing -- the request is
// refused before payment -- but it would still be a menu with dishes that are
// not available, which is its own kind of lie.
//
// The catalog is free and needs no token, so asking costs nothing.
function loadModels(){
  var request=++catalogRequest;
  return fetchModels()
    .then(function(cat){
      if(request!==catalogRequest)return;
	  catalogModels=normalizeCatalog(cat);
	  if(status&&Array.isArray(status.models)&&status.models.length){
		var allowed=new Set(status.models);
		catalogModels=catalogModels.filter(function(item){return allowed.has(item.id)});
	  }
	  modelsReady=true;
      if(!catalogModels.length){
        $("catalogState").textContent="The connected endpoint did not report a model catalog.";
		model.textContent="";model.disabled=true;conversation.model="";render();renderModelCards();
        return;
      }
	  model.disabled=false;
      var current=preferredModel||model.value;
      model.textContent="";
      catalogModels.forEach(function(m){
        var o=document.createElement("option");
        o.value=m.id;o.textContent=m.id;
        model.appendChild(o);
      });
      // Keep the selection if it survived, otherwise take the first.
      if(catalogModels.some(function(m){return m.id===current}))model.value=current;
      conversation.model=model.value;
	  render();renderModelCards();
    })
    .catch(function(){
	  if(request!==catalogRequest)return;
	  modelsReady=true;catalogModels=[];model.textContent="";model.disabled=true;conversation.model="";render();renderModelCards();
      $("catalogState").textContent="The local model catalog is unavailable.";
    });
}

function renderModelCards(){
  var grid=$("modelGrid");grid.textContent="";
  $("catalogState").textContent=catalogModels.length
    ? plural(catalogModels.length,"model")+" available through this gateway."
    : "No models are currently reported.";
  catalogModels.forEach(function(item){
    var card=document.createElement("article");card.className="model-card";
    var selected=item.id===model.value;
    card.classList.toggle("is-selected",selected);
    var title=document.createElement("h2");title.textContent=item.id;card.appendChild(title);

    var caps=document.createElement("div");caps.className="capabilities";
    [["Text",item.capabilities.text],["Streaming",item.capabilities.streaming],
      ["Tools",item.capabilities.tools],["Images",item.capabilities.images]].forEach(function(entry){
      var badge=document.createElement("span");badge.className="capability"+(entry[1]?" yes":"");
      badge.textContent=entry[1]?entry[0]:"No "+entry[0].toLowerCase();caps.appendChild(badge);
    });
    card.appendChild(caps);

    var facts=document.createElement("dl");facts.className="model-facts";
    modelFacts(item,status,retentionMode).forEach(function(pair){
      var row=document.createElement("div"),term=document.createElement("dt"),value=document.createElement("dd");
      term.textContent=pair[0];value.textContent=pair[1];row.appendChild(term);row.appendChild(value);facts.appendChild(row);
    });
    card.appendChild(facts);

    var choose=document.createElement("button");choose.type="button";choose.className="choose-model";
    choose.setAttribute("aria-pressed",String(selected));
    choose.textContent=selected?"Selected":"Use in Chat and Code";
    choose.addEventListener("click",function(){
      model.value=item.id;conversation.model=item.id;rememberModel(item.id);persistConversation();renderModelCards();
    });
    card.appendChild(choose);grid.appendChild(card);
  });
}

function rememberModel(id){
  preferredModel=id;
  try{localStorage.setItem("osanwe-model",id)}catch(e){}
}

model.addEventListener("change",function(){
  conversation.model=model.value;rememberModel(model.value);persistConversation();renderModelCards();
});

function retentionLabel(){
  $("retentionState").textContent=retentionMode==="device"?"Saved on this device":"Ephemeral";
  document.querySelectorAll("input[name='retention']").forEach(function(input){input.checked=input.value===retentionMode});
  if(catalogModels.length)renderModelCards();
  render();
}

function localHistoryDescription(){
  return retentionMode==="device"?"This conversation is saved only in this browser.":"This conversation is ephemeral.";
}

function setRetention(mode){
  if(mode!=="device"){
    retentionMode="ephemeral";store=conversationStore("ephemeral");
    try{localStorage.setItem("osanwe-retention","ephemeral")}catch(e){}
	clearStorageWarning();
    $("settingsStatus").textContent="This conversation is now ephemeral. Previously saved history remains until deleted.";
    retentionLabel();return Promise.resolve();
  }
  var candidate;
  try{candidate=conversationStore("device")}catch(e){storageFailed(e);return Promise.reject(e)}
  return lifecycle.persist(candidate,conversation).then(function(){
    store=candidate;
    retentionMode="device";
    try{localStorage.setItem("osanwe-retention","device")}catch(e){}
	clearStorageWarning();
    $("settingsStatus").textContent="This conversation is saved only in this browser profile.";
    retentionLabel();refreshHistory();
  }).catch(function(error){storageFailed(error);throw error});
}

function clearStorageWarning(){
	$("storageWarning").textContent="";$("storageWarning").hidden=true;
}

function storageFailed(){
  retentionMode="ephemeral";store=conversationStore("ephemeral");retentionLabel();
	var message="This conversation could not be saved. It is now ephemeral and will be lost when this page closes.";
	$("storageWarning").textContent=message;$("storageWarning").hidden=false;
	$("settingsStatus").textContent=message;
}

function deletionFailed(){
  retentionMode="ephemeral";store=conversationStore("ephemeral");retentionLabel();
  var message="Saved history could not be deleted. Osanwë stopped saving this page; close other Osanwë tabs and try the deletion again.";
  $("storageWarning").textContent=message;$("storageWarning").hidden=false;
  $("settingsStatus").textContent=message;
}

function persistConversation(target){
  if(retentionMode!=="device")return Promise.resolve(false);
  var destination=store;
	return lifecycle.persist(destination,target||conversation).catch(function(error){
    if(retentionMode==="device"&&store===destination)storageFailed(error);
    return false;
  });
}

function renderConversation(){
  rail.querySelectorAll(".turn").forEach(function(node){node.remove()});
  conversation.turns.forEach(function(item){
    var kind=item.role==="user"?"you":(item.status==="error"?"err":"reply");
    var label=item.role==="user"?"You":modeCopy[activeMode].assistant;
    var body=turn(kind,label),text=conversationTurnText(item);
    if(activeMode==="code"&&item.role==="assistant"&&item.status==="complete")renderAssistantContent(body,text,true);
    else body.textContent=text;
  });
  setEmpty(conversation.turns.length===0);
  if(catalogModels.some(function(item){return item.id===conversation.model}))model.value=conversation.model;
  scroll();refresh();
}

function refreshHistory(){
  var saved;
  try{saved=conversationStore("device")}catch(e){return Promise.resolve()}
  return saved.list().then(function(records){
    var list=$("historyList");list.textContent="";
    if(!records.length){
      var empty=document.createElement("p");empty.textContent="None saved in this browser.";list.appendChild(empty);return;
    }
    records.forEach(function(record){
      var button=document.createElement("button");button.type="button";button.textContent=conversationTitle(record);
      button.addEventListener("click",function(){
        runTransition(async function(){
          await stopActiveRequest();await lifecycle.idle();
          conversation=record;modeConversations[activeMode]=conversation;renderConversation();
          $("settingsDialog").close();
        }).catch(storageFailed);
      });
      list.appendChild(button);
    });
	}).catch(storageFailed);
}

function openSettings(){
  retentionLabel();$("settingsStatus").textContent="";refreshHistory();$("settingsDialog").showModal();
}
$("settingsBtn").addEventListener("click",openSettings);
$("retentionState").addEventListener("click",openSettings);
$("closeSettingsBtn").addEventListener("click",function(){$("settingsDialog").close()});
$("closeSettingsIcon").addEventListener("click",function(){$("settingsDialog").close()});
document.querySelectorAll("input[name='retention']").forEach(function(input){
  input.addEventListener("change",function(){setRetention(input.value).catch(function(){})});
});
$("deleteHistoryBtn").addEventListener("click",function(){
  if(!window.confirm("Delete every conversation saved by Osanwë in this browser? Exported files and provider copies cannot be deleted here."))return;
  runTransition(async function(){
    await stopActiveRequest();
    var saved=conversationStore("device");
    var clearing=lifecycle.clear(saved);
    retentionMode="ephemeral";store=conversationStore("ephemeral");
    try{localStorage.setItem("osanwe-retention","ephemeral")}catch(e){}
    await clearing;
    $("settingsStatus").textContent="All conversations saved by Osanwë in this browser were deleted.";retentionLabel();refreshHistory();
  }).catch(deletionFailed);
});
$("deleteConversationBtn").addEventListener("click",function(){
  if(!window.confirm("Delete this conversation from this page and device-only history? This cannot delete provider copies or exported files."))return;
  var target=conversation;
  runTransition(async function(){
    await stopActiveRequest();
    var saved=conversationStore("device");
    await lifecycle.delete(saved,target.id);
    if(conversation.id===target.id){conversation=createConversation({model:model.value});modeConversations[activeMode]=conversation;renderConversation()}
    refreshHistory();
    $("settingsStatus").textContent="The current conversation was deleted from this page and Osanwë device-only history.";
  }).catch(deletionFailed);
});
$("exportConversationBtn").addEventListener("click",function(){
  if(!conversation.turns.length){$("settingsStatus").textContent="There is no conversation to export.";return}
  var documentBody=JSON.stringify(exportConversation(conversation),null,2)+"\n";
  var blob=new Blob([documentBody],{type:"application/json"}),url=URL.createObjectURL(blob);
  var link=document.createElement("a");link.href=url;link.download="osanwe-conversation-"+new Date().toISOString().slice(0,10)+".json";
  document.body.appendChild(link);link.click();link.remove();setTimeout(function(){URL.revokeObjectURL(url)},0);
  $("settingsStatus").textContent="Exported as plaintext JSON. Anyone who can read that file can read the conversation.";
});

// ---- appearance -----------------------------------------------------
// Theme, model identifier, and the retention-mode choice are the only values
// kept in localStorage. They say nothing about what was asked, or when;
// conversation text never belongs in this small synchronous settings store.
(function(){
  var btn=$("themeBtn"),icon=$("themeIcon"),stored="",mode="light";
  try{stored=localStorage.getItem("osanwe-theme")||""}catch(e){}
  mode=stored==="light"||stored==="dark"
    ? stored
    : (window.matchMedia&&window.matchMedia("(prefers-color-scheme: dark)").matches?"dark":"light");

  function apply(){
    var root=document.documentElement;
    root.setAttribute("data-theme",mode);
    root.style.colorScheme=mode;
    var next=mode==="dark"?"light":"dark";
    icon.textContent=next==="light"?"☀":"☾";
    btn.setAttribute("aria-label","Switch to "+next+" mode");
    btn.title="Switch to "+next+" mode";
  }
  btn.addEventListener("click",function(){
    mode=mode==="dark"?"light":"dark";
    try{ localStorage.setItem("osanwe-theme",mode) }catch(e){}
    apply();
  });
  apply();
})();

showSnippet("shell");
retentionLabel();
load().then(loadModels);
// The client is local, so polling it is nearly free, and a relay that failed
// over should not sit behind a stale label until the page is reloaded.
setInterval(function(){if(!busy)Promise.all([load(),loadModels()])},10000);
autosize();refresh();
})();
