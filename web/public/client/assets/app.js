import { activateInviteBook, loadModels as fetchModels, loadStatus as fetchStatus, sendMessages, testProviderConnection } from "./api.js";
import { buildPreviewBundle, parseCodeFences, selectRunnableCode } from "./code.js";
import { appendTurn, conversationTitle, conversationTurnText, createConversation, exportConversation, toRequestMessages } from "./conversation.js";
import { disclosureNarrative } from "./disclosure.js";
import { isNearConversationEnd } from "./follow-scroll.js";
import { ConversationLifecycle } from "./lifecycle.js";
import { buildIdentityLabel, catalogRow, humanize, normalizeCatalog, relayVerificationLabel } from "./models.js";
import { connectionSnippets } from "./snippets.js";
import { readProviderTextStream } from "./sse.js";
import { conversationStore } from "./storage.js";

(function(){
"use strict";
var PREFIX="/client/";

var $=function(id){return document.getElementById(id)};
var thread=$("thread"),rail=$("rail"),opening=$("opening"),
    input=$("input"),send=$("send"),stop=$("stop"),seal=$("seal"),state=$("state"),model=$("model"),
    modelPicker=$("modelPicker"),modelTrigger=$("modelTrigger"),modelMenu=$("modelMenu"),
    modelChoiceToggle=$("modelChoiceToggle"),modelChoices=$("modelChoices"),modelAdvancedToggle=$("modelAdvancedToggle"),
    providerKeyInput=$("providerKey"),providerConsent=$("providerConsent"),providerSelect=$("providerSelect"),
    providerModel=$("providerModel"),providerModelSuggestions=$("providerModelSuggestions"),
    panel=$("panel"),veil=$("veil"),providerSettings=$("providerSettings"),runnerFrame=$("runnerPreview");

var status=null,busy=false,stopping=false,broken=false,requestPhase="idle",lastTTFT=null,activeRequest=null,dialogOpener=null,catalogModels=[],modelsReady=false,preferredModel="",
    providerKey="",providerId="groq",providers=[],activeMode="chat",runnerOpen=false,runnerChannel="",runnerLines=[],runnerBusy=false,runnerHadError=false,
    pendingRunnerRun=null,runnerStartupTimer=null,runnerLastSnapshot=null,runnerActiveSnapshot=null,runnerReturnFocus=null,runnerModal=false,
    completedRequestHere=false,catalogRequest=0,transitionTail=Promise.resolve(),lifecycle=new ConversationLifecycle(),
    retentionMode="ephemeral",store=conversationStore("ephemeral"),
    conversation=createConversation({model:model.value});
var modeConversations={chat:conversation,code:createConversation({model:model.value})};
var modeCopy={
  chat:{kicker:"Bring your own key",title:"What are you thinking about?",placeholder:"Ask anything",assistant:"Osanwë",system:""},
  code:{
    kicker:"Code assistant",title:"What should we build?",placeholder:"Describe a coding task",assistant:"Osanwë Code",
    system:"You are Osanwë Code, a focused coding assistant. Help analyze, write, review, debug, and explain code. You cannot access the user's local files, run commands, or apply changes, so state that limitation whenever it matters. Prefer concise, directly usable code or patches. When creating or revising a web interface, return a complete runnable preview using fenced HTML, CSS, and JavaScript; Osanwë combines those blocks and runs them automatically after the response completes. Fence executable standalone JavaScript as javascript. JavaScript may include tests with test(name, fn) and assert(condition, message) for the local sandbox. The preview has no network, persistent storage, terminal, or ambient file access. A person may deliberately choose a file inside the preview, so never request sensitive files."
  }
};
document.body.dataset.mode=activeMode;
try{preferredModel=localStorage.getItem("osanwe-model")||""}catch(e){}
try{providerId=localStorage.getItem("osanwe-provider")||"groq"}catch(e){}
try{if(localStorage.getItem("osanwe-retention")==="device")retentionMode="device"}catch(e){}
if(retentionMode==="device"){
  try{store=conversationStore("device")}catch(e){storageFailed(e)}
}

// ---- status ---------------------------------------------------------
function load(){
  return fetchStatus()
    .then(function(s){
      status=s;providers=Array.isArray(s.providers)?s.providers:[];
      if(!providers.some(function(item){return item.id===providerId}))providerId=providers[0]?providers[0].id:"groq";
      status.selected_provider=providerLabel();syncProviderControls();render();return s;
    })
    .catch(function(e){
      // The client is what serves this page, so failing to reach it means it
      // is going away. Saying so beats showing stale numbers as current.
      state.textContent="Client unreachable";state.classList.add("warn");
    });
}

function plural(n,word){return n+" "+word+(n===1?"":"s")}

function currentProvider(){return providers.find(function(item){return item.id===providerId})||null}
function providerLabel(){var item=currentProvider();return item?item.label:providerId}

function syncProviderControls(){
  var current=providerSelect.value;
  providerSelect.textContent="";
  providers.forEach(function(item){
    var option=document.createElement("option");option.value=item.id;option.textContent=item.label;providerSelect.appendChild(option);
  });
  providerSelect.value=providers.some(function(item){return item.id===providerId})?providerId:(current||providerId);
  providerModelSuggestions.textContent="";
  var selected=currentProvider();
  (selected&&Array.isArray(selected.models)?selected.models:[]).forEach(function(id){
    var option=document.createElement("option");option.value=id;providerModelSuggestions.appendChild(option);
  });
  providerModel.value=model.value||preferredModel||"";
}

function render(){
  if(!status)return;
  var tokens=false;
	var trial=null;
	var trialReady=true;
	var trialAvailable=true;
	var chatAvailable=Boolean(providerKey)&&modelsReady&&catalogModels.some(function(item){return item.id===model.value});

  status.selected_provider=providerLabel();
  $("endpointText").textContent=status.endpoint;
  $("copyEndpoint").dataset.copy=status.endpoint;
  $("upstreamText").textContent=providerLabel();
  showSnippet(currentSnip);

  providerSettings.hidden=false;
	$("trialAccessSettings").hidden=true;
	if(trial){
		$("trialActivationControls").hidden=trial.activated;
		$("trialAccessFacts").hidden=!trial.activated;
		$("trialAccessSummary").textContent=trial.activated
			?"Free test access is active in this local wallet. The invitation and tokens never leave this device except as unlinkable one-shot vouchers and blind-signed tokens."
			:"Choose the invitation JSON file supplied by your beta inviter. It is imported only by this local app and is never sent to Groq, the relay, or the gateway.";
		if(trial.activated){
			$("trialRequestsAvailable").textContent=String((status.wallet.on_hand||0)+(trial.remaining_epoch||0));
			$("trialEpochEnds").textContent=trial.epoch_ends?formatUTC(trial.epoch_ends):"No current allowance";
			$("trialExpires").textContent=trial.expires?formatUTC(trial.expires):"—";
		}
	}
  $("openProviderSettings").hidden=Boolean(providerKey);
  $("codeContext").hidden=activeMode!=="code";
  var runnerVisible=activeMode==="code"&&runnerOpen;
  $("codeRunner").hidden=!runnerVisible;
  $("runnerResizer").hidden=!runnerVisible;
  $("openCodeRunner").hidden=runnerVisible;
  $("codePreviewToggle").hidden=activeMode!=="code"||runnerVisible;
  document.body.classList.toggle("runner-open",runnerVisible);
  syncRunnerModality();
  $("rerunCode").disabled=runnerBusy||!runnerLastSnapshot;
  input.disabled=!chatAvailable;
	input.placeholder=!providerKey?"Load a provider key in Settings":(!modelsReady?"Checking suggested models":"No model selected");
	if(chatAvailable)input.placeholder=modeCopy[activeMode].placeholder;
  $("modeKicker").textContent=modeCopy[activeMode].kicker;
  $("openingTitle").textContent=modeCopy[activeMode].title;
  if(tokens&&!trialReady){
	$("openingSub").textContent="Open Settings and activate the invitation file supplied by your beta inviter.";
  }else if(tokens&&!trialAvailable){
	$("openingSub").textContent="The current free test allowance is exhausted. The exact reset time is shown in Settings.";
  }else if(!providerKey){
    $("openingSub").textContent="Choose a provider and model, then load a key in Settings to begin.";
  }else if(!chatAvailable){
    $("openingSub").textContent="Choose a model ID available to your "+providerLabel()+" account.";
  }else if(activeMode==="code"){
    $("openingSub").textContent=localHistoryDescription()+" Code mode can reason about text you provide, but it cannot access your files or terminal yet.";
  }else{
    $("openingSub").textContent=localHistoryDescription()+" Requests pass through this host to "+providerLabel()+" using your account.";
  }

  $("walletBlock").hidden=!tokens||!status.wallet;
  if(status.wallet){
    $("walletOnHand").textContent=status.wallet.on_hand;
    $("walletUnit").textContent="on hand · buys "+plural(status.wallet.on_hand,"request");
    $("walletSpent").textContent=status.wallet.spent;
  }

  var rows=[];
  rows.push(["Provider",providerLabel()]);
  rows.push(["API key",providerKey?"loaded in this tab":"not loaded"]);
  rows.push(["Request path","browser → Osanwë host → provider"]);
  rows.push(["Server history",status.retained]);

  var list=$("connList");list.textContent="";
  rows.forEach(function(r){
    var d=document.createElement("div");
    var k=document.createElement("span");k.textContent=r[0];
    var v=document.createElement("b");v.textContent=r[1];
    d.appendChild(k);d.appendChild(v);list.appendChild(d);
  });

  $("connAside").textContent="Provider destinations are fixed in the server registry. Arbitrary URLs are blocked so this site cannot become an open proxy.";

  if(catalogModels.length)renderModelCards();

  refresh();
}

function formatUTC(value){
	var parsed=new Date(value);
	return Number.isNaN(parsed.getTime())?"—":parsed.toISOString().replace(".000Z","Z");
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
    // One element per pair rather than two spans and a <br>. These are label
    // and value, not a run of text that happens to wrap, and the row lets the
    // label column size itself to the longest label instead of a guessed width.
    var row=document.createElement("div");
    var k=document.createElement("span");k.className="k";k.textContent=p[0];
    var v=document.createElement("b");v.textContent=p[1];
    row.appendChild(k);row.appendChild(v);f.appendChild(row);
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
    panel.scrollTop=0;
    panel.focus({preventScroll:true});
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
var followingConversation=true,conversationScrollFrame=0;
function scrollConversation(force){
  if(force)followingConversation=true;
  if(!followingConversation)return;
  if(conversationScrollFrame)cancelAnimationFrame(conversationScrollFrame);
  conversationScrollFrame=requestAnimationFrame(function(){
    conversationScrollFrame=0;
    if(!followingConversation&&!force)return;
    thread.scrollTop=thread.scrollHeight;
  });
}
thread.addEventListener("scroll",function(){
  followingConversation=isNearConversationEnd(thread);
},{passive:true});
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
  var parts=parseCodeFences(text),previewBundle=buildPreviewBundle(parts);
  var previewRoot=parts.find(function(part){return part.kind==="code"&&part.runnerLanguage==="html"});
  parts.forEach(function(part){
    if(part.kind==="text"){
      var span=document.createElement("span");span.className="assistant-text";span.textContent=part.content;body.appendChild(span);return;
    }
    var block=document.createElement("section");block.className="generated-code";
    var head=document.createElement("div");head.className="generated-code-head";
    var label=document.createElement("span");label.textContent=part.language||"code";head.appendChild(label);
    if(withRunner&&part.runnerLanguage){
      var useBundle=previewBundle&&(part===previewRoot||part.runnerLanguage==="javascript");
      var runnable=useBundle?previewBundle:{language:part.runnerLanguage,code:part.content};
      var loadButton=document.createElement("button");loadButton.type="button";
      loadButton.textContent=runnable.language==="html"?"Run preview":"Run code";
      loadButton.addEventListener("click",function(){loadRunnerCode(runnable.language,runnable.code,true,loadButton)});
      head.appendChild(loadButton);
    }
    var pre=document.createElement("pre"),code=document.createElement("code");
    code.textContent=part.content;pre.appendChild(code);block.appendChild(head);block.appendChild(pre);body.appendChild(block);
  });
  return selectRunnableCode(parts);
}

function refresh(){
	var hasCredential=Boolean(providerKey);
	var activeModel=catalogModels.some(function(item){return item.id===model.value});
  send.hidden=busy;stop.hidden=!busy;
  stop.disabled=!busy||stopping;
  send.disabled=busy||!hasCredential||!modelsReady||!activeModel||!input.value.trim();
  rail.setAttribute("aria-busy",String(busy));
  if(broken){state.textContent="Seal broken";state.classList.add("warn");return}
  state.classList.remove("warn");
	state.textContent=busy?(stopping?"Stopping":(requestPhase==="connecting"?"Connecting":"Generating")):(hasCredential?(modelsReady&&activeModel?(lastTTFT===null?"Ready":"Ready · "+lastTTFT+" ms TTFT"):(modelsReady?"Model needed":"Checking models")):"Key needed");
}

function fail(body,message){
  broken=true;seal.classList.add("broken");
  body.parentElement.className="turn err";
  body.textContent=message;
  refresh();scrollConversation();
}

function submit(){
  var text=input.value.trim();
  if(!text||busy||!status||!providerKey||!modelsReady||
	!catalogModels.some(function(item){return item.id===model.value}))return;

  setEmpty(false);
  var requestConversation=conversation;
  var requestMode=activeMode;
  appendTurn(requestConversation,"user",text);
  persistConversation(requestConversation);
  turn("you","You").textContent=text;
  input.value="";autosize();scrollConversation(true);

  broken=false;seal.classList.remove("broken","pressing");
  void seal.offsetWidth;seal.classList.add("pressing");

  var reply=appendTurn(requestConversation,"assistant","",{status:"streaming"});
  var body=turn("reply",modeCopy[requestMode].assistant);
  var caret=document.createElement("span");caret.className="caret";
  body.appendChild(caret);
  scrollConversation(true);
  var request={controller:new AbortController(),conversation:requestConversation,done:null,startedAt:performance.now(),firstTextAt:null};
  activeRequest=request;
	busy=true;stopping=false;requestPhase="connecting";refresh();
  request.done=sendMessages({
    model:model.value,
    system:modeCopy[requestMode].system,
    messages:toRequestMessages(requestConversation)
  },{
    signal:request.controller.signal,
    apiKey:providerKey,
    provider:providerId,
    mode:requestMode
  }).then(function(resp){
    requestPhase="streaming";refresh();
    return stream(resp,body,caret,reply,requestConversation,requestMode,request);
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
	if(activeRequest===request){busy=false;stopping=false;requestPhase="idle";activeRequest=null;refresh();input.focus()}
	render();
  });
}

// stream reads server-sent events and appends only the text deltas.
//
// Everything else on the wire -- message_start, usage records, ping -- carries
// no words. Appending them would put JSON in the middle of a sentence.
function stream(resp,body,caret,reply,requestConversation,requestMode,request){
  return readProviderTextStream(resp.body,function(text){
	if(request.firstTextAt===null){request.firstTextAt=performance.now();lastTTFT=Math.max(0,Math.round(request.firstTextAt-request.startedAt))}
	reply.content+=text;
	caret.remove();
	body.appendChild(document.createTextNode(text));
	body.appendChild(caret);
	scrollConversation();
  }).then(function(){
	reply.status="complete";
	caret.remove();
	if(requestMode==="code"){
	  var runnable=renderAssistantContent(body,reply.content,true);
	  if(runnable)loadRunnerCode(runnable.language,runnable.code,true,null,false);
	}
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
  if(name!=="editor"&&name!=="results")name="editor";
  $("runnerEditorPanel").hidden=name!=="editor";$("runnerResults").hidden=name!=="results";
  document.querySelectorAll("[data-runner-view]").forEach(function(button){
    var selected=button.dataset.runnerView===name;
    button.setAttribute("aria-selected",String(selected));button.tabIndex=selected?0:-1;
  });
}

function setRunnerExecutionBusy(value){
  runnerBusy=value;
  $("runCode").disabled=value;
  $("runnerEditor").readOnly=value;
  $("runnerLanguage").disabled=value;
  $("clearRunner").disabled=value;
  $("codeRunner").setAttribute("aria-busy",String(value));
}

function visibleFocusTarget(node){
  return Boolean(node&&node.isConnected&&!node.disabled&&!node.closest("[inert]")&&node.getClientRects().length);
}

function syncRunnerModality(){
  var visible=activeMode==="code"&&runnerOpen;
  var runner=$("codeRunner"),maximized=runner.classList.contains("is-maximized");
  var drawer=window.matchMedia("(max-width:70rem)").matches;
  var modal=visible&&(maximized||drawer);
  if(modal){runner.setAttribute("role","dialog");runner.setAttribute("aria-modal","true")}
  else{runner.removeAttribute("role");runner.removeAttribute("aria-modal")}
  $("codeContext").inert=modal;
  document.querySelector(".conversation-surface").inert=modal;
  $("runnerResizer").inert=modal;
  document.querySelector(".chrome").inert=modal;
  if(modal&&!runner.contains(document.activeElement))$("closeCodeRunner").focus();
  runnerModal=modal;
}

function resetRunnerFrame(reason){
  runnerChannel="";runnerHadError=false;runnerActiveSnapshot=null;pendingRunnerRun=null;setRunnerExecutionBusy(false);
  if(runnerStartupTimer)window.clearTimeout(runnerStartupTimer);runnerStartupTimer=null;
  runnerFrame.onload=null;
  runnerFrame.src=PREFIX+"assets/runner.html?idle="+encodeURIComponent(String(Date.now()));
  if(reason)$("runnerStatus").textContent=reason;
  $("rerunCode").disabled=!runnerLastSnapshot;
}

function setRunnerOpen(open,opener){
  if(open&&opener)runnerReturnFocus=opener;
  runnerOpen=open;
  if(!open){
    resetRunnerFrame("Preview stopped. Loaded code remains in the editor.");
    $("codeRunner").classList.remove("is-maximized");
    $("expandCodeRunner").textContent="↗";
    $("expandCodeRunner").setAttribute("aria-label","Maximize live preview");
  }
  render();
  if(open)$("runnerEditor").focus();
  else{
    var returnTarget=runnerReturnFocus;runnerReturnFocus=null;
    if(!visibleFocusTarget(returnTarget))returnTarget=$("codePreviewToggle");
    if(!visibleFocusTarget(returnTarget))returnTarget=$("codeTab");
    if(visibleFocusTarget(returnTarget))returnTarget.focus();
  }
}

function loadRunnerCode(language,code,runNow,opener,moveFocus){
  $("runnerLanguage").value=language;$("runnerEditor").value=code;
  runnerLines=[];$("runnerResults").textContent="No output yet.";
  $("runnerStatus").classList.remove("warn");$("runnerStatus").textContent=runNow?"Loading generated code into the sandbox…":"Loaded. Nothing runs until you choose Run in preview.";
  runnerOpen=true;runnerReturnFocus=opener||document.activeElement;render();
  if(runNow)startRunnerRun(language,code,moveFocus!==false);else $("runnerEditor").focus();
}

function addRunnerLine(kind,text){
  var prefix={log:"LOG",info:"INFO",warn:"WARN",error:"ERROR",test:"TEST",preview:"PREVIEW",complete:"DONE"}[kind]||"OUTPUT";
  runnerLines.push(prefix+"  "+String(text).slice(0,4000));
  if(runnerLines.length>200)runnerLines=runnerLines.slice(-200);
  $("runnerResults").textContent=runnerLines.join("\n")||"No output yet.";
}

function showRunnerNetworkState(available){
  var badge=$("runnerNetworkState");
  if(available===true){
    badge.textContent="Network off";badge.title="Generated code cannot make network requests";return;
  }
  if(available===false){
    badge.textContent="HTML locked";badge.title="Interactive HTML requires Chromium 152 or newer";return;
  }
  badge.textContent="Checking";badge.title="Checking the browser's preview boundary";
}

function requestRunnerCapabilities(){
  if(runnerFrame.contentWindow)runnerFrame.contentWindow.postMessage({type:"osanwe-runner-capabilities"},"*");
}

function startRunnerRun(language,code,moveFocus){
  if(!code.trim()){
    $("runnerStatus").textContent="Add code before running.";$("runnerStatus").classList.add("warn");$("runnerEditor").focus();return;
  }
  runnerLastSnapshot={language:language,code:code};runnerActiveSnapshot=runnerLastSnapshot;
  runnerChannel=crypto.randomUUID();runnerLines=[];runnerHadError=false;setRunnerExecutionBusy(true);
  $("runnerResults").textContent="Waiting for output…";$("runnerStatus").textContent="Running in the isolated sandbox…";
  $("runnerStatus").classList.remove("warn");$("rerunCode").disabled=true;
  if(language==="javascript"){showRunnerView("results");if(moveFocus!==false)$("resultsTab").focus()}
  else if(moveFocus!==false)$("closeCodeRunner").focus();
  $("previewAddress").textContent=language==="html"?"osanwe://local-preview/index.html":"osanwe://local-preview/console";
  pendingRunnerRun={type:"osanwe-run",channel:runnerChannel,language:language,code:code};
  var expectedChannel=runnerChannel;
  runnerStartupTimer=window.setTimeout(function(){
    if(!pendingRunnerRun||pendingRunnerRun.channel!==expectedChannel)return;
    pendingRunnerRun=null;runnerActiveSnapshot=null;setRunnerExecutionBusy(false);$("rerunCode").disabled=false;
    $("runnerStatus").textContent="The sandbox did not start. Try running again.";$("runnerStatus").classList.add("warn");
  },1500);
  runnerFrame.src=PREFIX+"assets/runner.html?run="+encodeURIComponent(expectedChannel);
}

function runEditorCode(){
  startRunnerRun($("runnerLanguage").value,$("runnerEditor").value,true);
}

function rerunLastSnapshot(){
  if(!runnerLastSnapshot){
    $("runnerStatus").textContent="Nothing has run yet. Choose Run in preview first.";$("runnerStatus").classList.add("warn");return;
  }
  startRunnerRun(runnerLastSnapshot.language,runnerLastSnapshot.code,true);
}

window.addEventListener("message",function(event){
  var message=event.data;
  if(event.source!==runnerFrame.contentWindow||!message)return;
  if(message.type==="osanwe-runner-ready"){
    if(typeof message.networkIsolation==="boolean")showRunnerNetworkState(message.networkIsolation);
    if(!pendingRunnerRun||message.channel!==pendingRunnerRun.channel)return;
    if(runnerStartupTimer)window.clearTimeout(runnerStartupTimer);runnerStartupTimer=null;
    runnerFrame.contentWindow.postMessage(pendingRunnerRun,"*");pendingRunnerRun=null;return;
  }
  if(message.type==="osanwe-runner-control"&&message.channel===runnerChannel&&message.action==="escape"){
    handleRunnerEscape();return;
  }
  if(message.type!=="osanwe-runner"||message.channel!==runnerChannel)return;
  if(["log","info","warn","error","test","preview","complete"].indexOf(message.kind)<0)return;
  addRunnerLine(message.kind,message.text||"");
  if(message.kind==="error"){
    runnerHadError=true;$("runnerStatus").textContent=message.text||"The run failed.";$("runnerStatus").classList.add("warn");
  }
  if(message.kind==="preview")$("runnerStatus").textContent=message.text||"Preview ready.";
  if(message.kind==="complete"){
    var completedLanguage=runnerActiveSnapshot?runnerActiveSnapshot.language:"";
    runnerActiveSnapshot=null;setRunnerExecutionBusy(false);$("rerunCode").disabled=false;
    var failed=Number.isSafeInteger(message.failed)?message.failed:0;
    var tests=Number.isSafeInteger(message.testCount)?message.testCount:0;
    if(message.timedOut){$("runnerStatus").textContent="Stopped at the 2.5 second limit.";$("runnerStatus").classList.add("warn")}
    else if(runnerHadError){$("runnerStatus").textContent="Run completed with errors.";$("runnerStatus").classList.add("warn")}
    else if(failed){$("runnerStatus").textContent=message.text||"One or more tests failed.";$("runnerStatus").classList.add("warn")}
    else if(tests){$("runnerStatus").textContent=message.text||"All declared tests passed."}
    else if(completedLanguage==="javascript"){$("runnerStatus").textContent="Run completed. No tests were declared."}
  }
});
runnerFrame.addEventListener("load",requestRunnerCapabilities);
window.setTimeout(requestRunnerCapabilities,0);

$("openCodeRunner").addEventListener("click",function(){setRunnerOpen(true,this)});
$("codePreviewToggle").addEventListener("click",function(){setRunnerOpen(true,this)});
$("closeCodeRunner").addEventListener("click",function(){setRunnerOpen(false)});
$("runCode").addEventListener("click",runEditorCode);
$("rerunCode").addEventListener("click",rerunLastSnapshot);
$("expandCodeRunner").addEventListener("click",function(){
  var maximized=$("codeRunner").classList.toggle("is-maximized");
  this.textContent=maximized?"↙":"↗";
  this.setAttribute("aria-label",maximized?"Restore live preview":"Maximize live preview");
  this.title=this.getAttribute("aria-label");
  syncRunnerModality();
});
$("clearRunner").addEventListener("click",function(){
  $("runnerEditor").value="";runnerLines=[];runnerLastSnapshot=null;$("runnerResults").textContent="No output yet.";$("runnerStatus").classList.remove("warn");
  resetRunnerFrame("Cleared. Nothing has run.");$("runnerEditor").focus();
});
$("runnerEditor").addEventListener("input",function(){
  if(!runnerBusy&&runnerLastSnapshot&&(this.value!==runnerLastSnapshot.code||$("runnerLanguage").value!==runnerLastSnapshot.language)){
    $("runnerStatus").textContent="Code changed. Run it to update the display; Reload keeps the last snapshot.";
  }
});
$("runnerLanguage").addEventListener("change",function(){
  if(!runnerBusy&&runnerLastSnapshot)$("runnerStatus").textContent="Language changed. Run it to update the display; Reload keeps the last snapshot.";
});
document.querySelectorAll("[data-runner-view]").forEach(function(button){button.addEventListener("click",function(){showRunnerView(button.dataset.runnerView)})});

function setRunnerWidth(value){
  var percent=Math.max(35,Math.min(72,Math.round(value)));
  $("codeRunner").style.setProperty("--runner-width",percent+"%");
  $("runnerResizer").setAttribute("aria-valuenow",String(percent));
  $("runnerResizer").setAttribute("aria-valuetext","Preview width "+percent+" percent");
}
$("runnerResizer").addEventListener("pointerdown",function(event){
  var handle=this;handle.setPointerCapture(event.pointerId);handle.classList.add("is-dragging");
  var move=function(next){setRunnerWidth((window.innerWidth-next.clientX)/window.innerWidth*100)};
  var done=function(){handle.classList.remove("is-dragging");handle.removeEventListener("pointermove",move);handle.removeEventListener("pointerup",done);handle.removeEventListener("pointercancel",done)};
  handle.addEventListener("pointermove",move);handle.addEventListener("pointerup",done);handle.addEventListener("pointercancel",done);
});
$("runnerResizer").addEventListener("keydown",function(event){
  var current=Number(this.getAttribute("aria-valuenow"))||38;
  if(event.key==="ArrowLeft")current+=2;
  else if(event.key==="ArrowRight")current-=2;
  else if(event.key==="Home")current=35;
  else if(event.key==="End")current=72;
  else return;
  event.preventDefault();setRunnerWidth(current);
});
window.addEventListener("resize",syncRunnerModality);

// ---- wiring ---------------------------------------------------------
function validModelId(value){return /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,159}$/.test(value)}

function customCatalogEntry(id){
  return normalizeCatalog({data:[{
    id:id,type:"model",capabilities:{text:true,streaming:false,tools:false,images:false},
    limits:{max_request_bytes:65536,max_output_tokens:2048},
    osanwe:{provider_account:"your_provider_account",gateway_content_access:"prompt_and_answer_visible_in_transit",
      conversation_history:"not_intentionally_retained_by_osanwe",provider_retention:"see_provider_policy",
      provider_training:"see_provider_policy",provider_identity:providerLabel()}
  }]})[0];
}

function applyProviderModel(){
  var candidate=providerModel.value.trim();
  if(!validModelId(candidate)){
    $("providerKeyStatus").textContent="Enter a provider model ID using letters, numbers, dots, slashes, colons, underscores, or hyphens.";
    providerModel.focus();return false;
  }
  if(!catalogModels.some(function(item){return item.id===candidate}))catalogModels.push(customCatalogEntry(candidate));
  selectModel(candidate);providerModel.value=candidate;
  $("providerKeyStatus").textContent="Model set to "+candidate+" for "+providerLabel()+".";
  return true;
}

function connectProviderKey(){
  if(!status||!applyProviderModel())return;
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
  $("connectProviderKey").hidden=true;$("testProviderKey").hidden=false;$("forgetProviderKey").hidden=false;
  $("providerKeyStatus").textContent="Key loaded for "+providerLabel()+" in this tab. Test the connection before closing Settings. Reload or choose Forget key to clear it.";
  render();$("testProviderKey").focus();
}

function forgetProviderKey(){
  providerKey="";providerKeyInput.value="";providerKeyInput.disabled=false;providerConsent.disabled=false;
  $("connectProviderKey").hidden=false;$("testProviderKey").hidden=true;$("forgetProviderKey").hidden=true;
  $("providerKeyStatus").textContent="The tab released its reference to the provider key.";
  render();providerKeyInput.focus();
}

async function checkProviderConnection(){
  var button=$("testProviderKey"),message=$("providerKeyStatus");
  if(!providerKey){message.textContent="Load a provider key before testing the connection.";providerKeyInput.focus();return}
  if(!applyProviderModel())return;
  button.disabled=true;message.textContent="Testing "+providerLabel()+" with a bounded synthetic request…";
  try{
    var result=await testProviderConnection({provider:providerId,model:model.value,apiKey:providerKey});
    message.textContent="Connection verified. "+providerLabel()+" accepted "+result.model+".";
  }catch(error){
    var retry=error&&error.retryable?" You can try again.":"";
    message.textContent=(error&&error.message?error.message:"Connection test failed.")+retry;
  }finally{button.disabled=false}
}

async function activateSelectedInvite(){
	var chooser=$("inviteBookFile"),button=$("activateTrialAccess"),message=$("trialAccessStatus");
	var file=chooser.files&&chooser.files[0];
	if(!file){message.textContent="Choose the invitation JSON file supplied by your beta inviter.";chooser.focus();return}
	if(file.size<1||file.size>64*1024){message.textContent="The invitation must be a non-empty JSON file no larger than 64 KiB.";return}
	button.disabled=true;chooser.disabled=true;message.textContent="Validating locally…";
	try{
		var contents=await file.text();
		await activateInviteBook(contents);
		chooser.value="";
		await load();
		message.textContent="Free test access is active. The invitation is now held in this device's private wallet.";
	}catch(error){
		message.textContent=error&&error.message?error.message:"Free test activation failed.";
	}finally{
		button.disabled=false;chooser.disabled=false;
	}
}

$("connectProviderKey").addEventListener("click",connectProviderKey);
$("testProviderKey").addEventListener("click",function(){checkProviderConnection()});
$("forgetProviderKey").addEventListener("click",forgetProviderKey);
$("useProviderModel").addEventListener("click",applyProviderModel);
providerModel.addEventListener("keydown",function(e){if(e.key==="Enter"){e.preventDefault();applyProviderModel()}});
providerSelect.addEventListener("change",function(){
  var next=providerSelect.value;
  if(!providers.some(function(item){return item.id===next})||next===providerId)return;
  runTransition(async function(){
    await stopActiveRequest();
    providerKey="";providerKeyInput.value="";providerKeyInput.disabled=false;providerConsent.disabled=false;
    $("connectProviderKey").hidden=false;$("testProviderKey").hidden=true;$("forgetProviderKey").hidden=true;
    providerId=next;preferredModel="";
    try{localStorage.setItem("osanwe-provider",providerId);localStorage.removeItem("osanwe-model")}catch(e){}
    if(status)status.selected_provider=providerLabel();
    await loadModels();syncProviderControls();
    $("providerKeyStatus").textContent="Provider changed to "+providerLabel()+". Load that provider's key for this tab.";
    render();providerKeyInput.focus();
  }).catch(storageFailed);
});
$("activateTrialAccess").addEventListener("click",function(){activateSelectedInvite()});
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
function handleRunnerEscape(){
  if(activeMode!=="code"||!runnerOpen)return false;
  if($("codeRunner").classList.contains("is-maximized")){$("expandCodeRunner").click();return true}
  if(window.matchMedia("(max-width:70rem)").matches){setRunnerOpen(false);return true}
  return false;
}
document.addEventListener("keydown",function(e){
  if(e.key==="Escape"&&panel.classList.contains("open")){openPanel(false);return}
  if(e.key==="Escape"&&!$("settingsDialog").open&&handleRunnerEscape()){e.preventDefault();return}
  keepDialogFocus(e);
});

$("codeRunner").addEventListener("keydown",function(e){
  if(!runnerModal||e.key!=="Tab")return;
  var candidates=Array.from(this.querySelectorAll("button:not(:disabled),select:not(:disabled),textarea:not(:disabled),iframe,[tabindex]:not([tabindex='-1'])"))
    .filter(visibleFocusTarget);
  if(!candidates.length)return;
  var first=candidates[0],last=candidates[candidates.length-1];
  if(e.shiftKey&&document.activeElement===first){e.preventDefault();last.focus()}
  else if(!e.shiftKey&&document.activeElement===last){e.preventDefault();first.focus()}
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
      if(activeMode==="code")runnerOpen=window.matchMedia("(min-width:70.01rem)").matches;
      else if(runnerOpen){runnerOpen=false;resetRunnerFrame("Preview stopped. Loaded code remains in the editor.")}
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

// Re-reading the catalog is free and needs no token, so the control is always
// live rather than gated on an error state.
$("reloadCatalog").addEventListener("click",function(){
  $("catalogState").textContent="Refreshing provider suggestions…";
  loadModels(true);
});

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
function closeModelMenu(){
  modelMenu.hidden=true;modelTrigger.setAttribute("aria-expanded","false");
  modelChoices.hidden=true;modelChoiceToggle.setAttribute("aria-expanded","false");
  $("modelAdvanced").hidden=true;modelAdvancedToggle.setAttribute("aria-expanded","false");
}

function syncModelPicker(){
  var selected=model.value||"No model";
  $("modelTriggerValue").textContent=selected;
  $("modelMenuValue").textContent=selected;
  modelTrigger.disabled=model.disabled;
  modelChoices.textContent="";
  catalogModels.forEach(function(item){
    var choice=document.createElement("button");
    choice.type="button";choice.className="model-choice";choice.setAttribute("role","option");
    choice.setAttribute("aria-selected",String(item.id===model.value));choice.textContent=item.id;
    choice.addEventListener("click",function(){selectModel(item.id);closeModelMenu();modelTrigger.focus()});
    modelChoices.appendChild(choice);
  });
  if(providerModel&&model.value)providerModel.value=model.value;
}

function selectModel(id){
  if(!catalogModels.some(function(item){return item.id===id}))return;
  model.value=id;conversation.model=id;rememberModel(id);persistConversation();
  syncModelPicker();renderModelCards();render();
}

function loadModels(force){
  var request=++catalogRequest;
  return fetchModels(providerId,globalThis.fetch,Boolean(force))
    .then(function(cat){
      if(request!==catalogRequest)return;
	  catalogModels=normalizeCatalog(cat);
	  if(preferredModel&&validModelId(preferredModel)&&!catalogModels.some(function(item){return item.id===preferredModel}))catalogModels.push(customCatalogEntry(preferredModel));
	  modelsReady=true;
      if(!catalogModels.length){
        $("catalogState").textContent="No suggested models are available for this provider. Enter a model ID in Settings.";
		model.textContent="";model.disabled=true;conversation.model="";syncModelPicker();closeModelMenu();render();renderModelCards();
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
	  syncModelPicker();render();renderModelCards();
    })
    .catch(function(){
	  if(request!==catalogRequest)return;
	  modelsReady=true;catalogModels=[];model.textContent="";model.disabled=true;conversation.model="";syncModelPicker();closeModelMenu();render();renderModelCards();
      $("catalogState").textContent="The provider suggestions are unavailable. Try again.";
    });
}

function renderModelCards(){
  var body=$("catalogBody");body.textContent="";
  $("catalogState").textContent=catalogModels.length
    ? providerLabel()+" · "+plural(catalogModels.length,"suggested model")
    : "No suggested models are currently listed.";

  catalogModels.forEach(function(item){
    var row=catalogRow(item,retentionMode);
    var selected=item.id===model.value;
    var tr=document.createElement("tr");
    tr.classList.toggle("is-selected",selected);

    // The model name doubles as the control that selects it. A separate button
    // per row would put six of them in a column that is already dense, and the
    // row is the thing a reader is pointing at anyway.
    var name=document.createElement("td");
    var choose=document.createElement("button");
    choose.type="button";choose.className="choose-model";
    choose.setAttribute("aria-pressed",String(selected));
    choose.textContent=row.id;
    choose.addEventListener("click",function(){selectModel(item.id)});
    name.appendChild(choose);
    var tags=document.createElement("div");tags.className="row-tags";
    row.tags.forEach(function(t){
      var tag=document.createElement("span");tag.className="row-tag";tag.textContent=t;tags.appendChild(tag);
    });
    name.appendChild(tags);
    tr.appendChild(name);

    [row.availability,row.request,row.ours,row.theirs,row.expiry].forEach(function(text){
      var td=document.createElement("td");td.textContent=text;tr.appendChild(td);
    });
    body.appendChild(tr);
  });
}

function rememberModel(id){
  preferredModel=id;
  try{localStorage.setItem("osanwe-model",id)}catch(e){}
}

model.addEventListener("change",function(){
  conversation.model=model.value;providerModel.value=model.value;rememberModel(model.value);persistConversation();syncModelPicker();renderModelCards();
});
modelTrigger.addEventListener("click",function(){
  var open=modelMenu.hidden;
  if(open){modelMenu.hidden=false;modelTrigger.setAttribute("aria-expanded","true");modelChoiceToggle.focus()}
  else closeModelMenu();
});
modelChoiceToggle.addEventListener("click",function(){
  var open=modelChoices.hidden;modelChoices.hidden=!open;modelChoiceToggle.setAttribute("aria-expanded",String(open));
  if(open){var first=modelChoices.querySelector(".model-choice");if(first)first.focus()}
});
modelAdvancedToggle.addEventListener("click",function(){
  var detail=$("modelAdvanced"),open=detail.hidden;detail.hidden=!open;modelAdvancedToggle.setAttribute("aria-expanded",String(open));
});
document.addEventListener("click",function(event){if(!modelMenu.hidden&&!modelPicker.contains(event.target))closeModelMenu()});
document.addEventListener("keydown",function(event){
  if(event.key==="Escape"&&!modelMenu.hidden){event.preventDefault();closeModelMenu();modelTrigger.focus()}
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
  syncModelPicker();
  scrollConversation(true);refresh();
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
syncModelPicker();
load().then(function(){return loadModels()});
autosize();refresh();
})();
