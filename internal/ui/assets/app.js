import { loadModels as fetchModels, loadStatus as fetchStatus, sendMessages } from "./api.js";
import { appendTurn, conversationTitle, createConversation, exportConversation, toRequestMessages } from "./conversation.js";
import { humanize, modelFacts, normalizeCatalog } from "./models.js";
import { anthropicTextDelta, SSEParser } from "./sse.js";
import { conversationStore } from "./storage.js";

(function(){
"use strict";
var PREFIX="/_osanwe/";

var $=function(id){return document.getElementById(id)};
var thread=$("thread"),rail=$("rail"),opening=$("opening"),byok=$("byokNotice"),
    input=$("input"),send=$("send"),seal=$("seal"),state=$("state"),model=$("model"),
    panel=$("panel"),veil=$("veil"),chatView=$("chatView"),modelsView=$("modelsView"),devView=$("devView");

var status=null,busy=false,broken=false,inFlight=null,dialogOpener=null,catalogModels=[],preferredModel="",
    retentionMode="ephemeral",store=conversationStore("ephemeral"),
    conversation=createConversation({model:model.value});
try{preferredModel=localStorage.getItem("osanwe-model")||""}catch(e){}
try{if(localStorage.getItem("osanwe-retention")==="device")retentionMode="device"}catch(e){}
if(retentionMode==="device"){
  try{store=conversationStore("device")}catch(e){retentionMode="ephemeral";store=conversationStore("ephemeral")}
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

  $("endpointText").textContent="http://"+status.endpoint;
  $("copyEndpoint").dataset.copy="http://"+status.endpoint;
  $("upstreamText").textContent=status.upstream;
  showSnippet(currentSnip);

  // Chat only works when the client can pay. On a bring-your-own-key setup
  // there is no key in this process to send with, which is the point.
  byok.hidden=tokens;
  input.disabled=!tokens;
  input.placeholder=tokens?"Ask anything":"Not available on your own key";
  $("openingSub").textContent=tokens
    ? "Nothing here is kept, and nothing leaves this device unsealed."
    : "Osanwë is carrying your tools' traffic. This window is not one of them.";

  $("walletBlock").hidden=!tokens||!status.wallet;
  if(status.wallet){
    $("walletOnHand").textContent=status.wallet.on_hand;
    $("walletUnit").textContent="on hand · buys "+plural(status.wallet.on_hand,"request");
    $("walletSpent").textContent=status.wallet.spent;
  }

  var rows=[];
  if(status.relay){
    rows.push(["Relay",status.relay.nickname||status.relay.address]);
    rows.push(["Key",status.relay.key_matched?"matched what was published":"unverified"]);
    if(status.relay.since_seconds)rows.push(["In use for",humanAge(status.relay.since_seconds)]);
  }else{
    rows.push(["Relay","none chosen yet"]);
  }
  if(status.directory){
    rows.push(["Directory",plural(status.directory.signed_by,"authority")+" signed the list in force"]);
    rows.push(["Relays known",String(status.directory.relays_known)]);
  }
  rows.push(["Paying with",status.paying]);
  rows.push(["Retained",status.retained]);

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
  var lines=[];
  if(status&&status.relay){
    lines.push(["They were sealed on this machine, ","before anything touched the network."]);
    lines.push(["They passed through a relay that could not open them — ",
      "it forwarded a sealed thing and knows only that it did."]);
  }
  if(status&&status.paying==="tokens"){
    lines.push(["The model answered without being told who asked. ",
      "Your address stopped at the relay, and no account of yours was used."]);
    lines.push(["The gateway could read your words. ",
      "Attested execution is not built, and this client cannot verify that the relay and gateway have independent operators."]);
  }else{
    lines.push(["","Your own account was used, so the provider still knows who asked. Only your address was hidden."]);
  }
  lines.push(["","Your writing style is still your own. No network can disguise that, and this one does not claim to."]);

  lines.forEach(function(l){
    var li=document.createElement("li");
    if(l[0])li.appendChild(document.createTextNode(l[0]));
    var s=document.createElement("span");s.textContent=l[1];li.appendChild(s);
    told.appendChild(li);
  });

  var f=$("facts");f.textContent="";
  var facts=[];
  if(status&&status.relay)facts.push(["relay",status.relay.nickname||status.relay.address]);
  if(status&&status.relay)facts.push(["verified",status.relay.key_matched?"key matched the published one":"unverified"]);
  if(status&&status.directory)facts.push(["signed by",status.directory.signed_by+" authorities"]);
  if(status)facts.push(["paying",status.paying]);
  if(status)facts.push(["retained",status.retained]);
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

function turn(kind,label){
  var d=document.createElement("div");d.className="turn "+kind;
  var h=document.createElement("div");h.className="who";h.textContent=label;
  var b=document.createElement("div");b.className="body";
  d.appendChild(h);d.appendChild(b);rail.appendChild(d);return b;
}

function refresh(){
  var tokens=status&&status.paying==="tokens";
  send.disabled=busy||!tokens||!input.value.trim();
  rail.setAttribute("aria-busy",String(busy));
  if(broken){state.textContent="Seal broken";state.classList.add("warn");return}
  state.classList.remove("warn");
  state.textContent=busy?"Answering":(tokens?"Sealed":"Carrying your tools");
}

function fail(body,message){
  broken=true;seal.classList.add("broken");
  body.parentElement.className="turn err";
  body.textContent=message;
  refresh();scroll();
}

function submit(){
  var text=input.value.trim();
  if(!text||busy||!status||status.paying!=="tokens")return;

  setEmpty(false);
  appendTurn(conversation,"user",text);
  persistConversation();
  turn("you","You").textContent=text;
  input.value="";autosize();scroll();

  broken=false;seal.classList.remove("broken","pressing");
  void seal.offsetWidth;seal.classList.add("pressing");

  var reply=appendTurn(conversation,"assistant","",{status:"streaming"});
  var body=turn("reply","Osanwë");
  var caret=document.createElement("span");caret.className="caret";
  body.appendChild(caret);
  busy=true;refresh();

  inFlight=new AbortController();
  sendMessages({
    model:model.value,
    messages:toRequestMessages(conversation)
  },{signal:inFlight.signal}).then(function(resp){
    return stream(resp,body,caret,reply);
  }).catch(function(e){
    if(e.name==="AbortError"){
      reply.status="stopped";caret.remove();
      if(!reply.content)body.textContent="Stopped";
      persistConversation();
      busy=false;refresh();return;
    }
    reply.status="error";
    persistConversation();
    caret.remove();
    fail(body,e.message||String(e));
  }).then(function(){
    busy=false;inFlight=null;refresh();
    // Spending a token changes the wallet, so the numbers behind Connect are
    // stale the moment a request finishes.
    load();
  });
}

// stream reads server-sent events and appends only the text deltas.
//
// Everything else on the wire -- message_start, usage records, ping -- carries
// no words. Appending them would put JSON in the middle of a sentence.
function stream(resp,body,caret,reply){
  var reader=resp.body.getReader(),dec=new TextDecoder(),parser=new SSEParser();
  function consume(payloads){
    payloads.forEach(function(payload){
      var delta=anthropicTextDelta(payload);
      if(!delta.text)return;
      reply.content+=delta.text;
      caret.remove();
      body.appendChild(document.createTextNode(delta.text));
      body.appendChild(caret);
      scroll();
    });
  }
  function pump(){
    return reader.read().then(function(r){
      if(r.done){
        consume(parser.push(dec.decode()));
        consume(parser.finish());
        reply.status="complete";
        persistConversation();
        caret.remove();return;
      }
      consume(parser.push(dec.decode(r.value,{stream:true})));
      return pump();
    });
  }
  return pump();
}

// ---- snippets -------------------------------------------------------
var currentSnip="shell";
function snippets(endpoint){
  var url="http://"+endpoint;
  return {
    shell:{code:'# any tool that reads the standard variable\n'+
      'export ANTHROPIC_BASE_URL='+url+'\n'+
      'export ANTHROPIC_API_KEY=osanwe   # discarded locally',
      note:"Most tools read ANTHROPIC_BASE_URL and need nothing else changed."},
    python:{code:'from anthropic import Anthropic\n\n'+
      'client = Anthropic(\n    base_url="'+url+'",\n'+
      '    api_key="osanwe",   # required by the SDK\n)',
      note:"The SDK insists on a key. When you are paying with tokens that string is stripped before the request leaves this machine."},
    node:{code:'import Anthropic from "@anthropic-ai/sdk";\n\n'+
      'const client = new Anthropic({\n'+
      '  baseURL: "'+url+'",\n  apiKey: "osanwe",\n});',
      note:"Streaming works unchanged. Nothing on the path buffers, so tokens arrive as they are produced."},
    curl:{code:'curl '+url+'/v1/messages \\\n'+
      '  -H "content-type: application/json" \\\n'+
      '  -d \'{"model":"claude-sonnet-5","max_tokens":1024,\n'+
      '       "messages":[{"role":"user","content":"…"}]}\'',
      note:"No auth header at all when tokens are in use: one is bought and attached for you, per request."}
  };
}
function showSnippet(name){
  currentSnip=name;
  var s=snippets(status?status.endpoint:"127.0.0.1:8080")[name];
  $("snippet").textContent=s.code;$("snipNote").textContent=s.note;
}

// ---- wiring ---------------------------------------------------------
input.addEventListener("input",function(){autosize();refresh()});
input.addEventListener("keydown",function(e){
  if(e.key==="Enter"&&!e.shiftKey){e.preventDefault();submit()}
});
send.addEventListener("click",submit);
seal.addEventListener("click",function(){openPanel(!panel.classList.contains("open"))});
$("closePanel").addEventListener("click",function(){openPanel(false)});
veil.addEventListener("click",function(){openPanel(false)});
document.addEventListener("keydown",function(e){
  if(e.key==="Escape"&&panel.classList.contains("open"))openPanel(false);
  keepDialogFocus(e);
});

$("newBtn").addEventListener("click",function(){
  if(inFlight)inFlight.abort();
  conversation=createConversation({model:model.value});
  rail.querySelectorAll(".turn").forEach(function(n){n.remove()});
  broken=false;seal.classList.remove("broken");
  setEmpty(true);input.focus();refresh();
});

document.querySelectorAll("[data-view]").forEach(function(btn){
  btn.addEventListener("click",function(){
    var want=btn.dataset.view;
    document.querySelectorAll("[data-view]").forEach(function(b){
      b.setAttribute("aria-selected",String(b===btn));
      b.tabIndex=b===btn?0:-1;
    });
    chatView.hidden=want!=="chat";modelsView.hidden=want!=="models";devView.hidden=want!=="dev";
    if(want==="dev")load();else if(want==="chat")input.focus();
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
    var tabs=Array.from(list.querySelectorAll("[role='tab']"));
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
  var btn=this,done=function(){
    btn.textContent="Copied";btn.classList.add("done");
    setTimeout(function(){btn.textContent="Copy";btn.classList.remove("done")},1400);
  };
  if(navigator.clipboard&&navigator.clipboard.writeText){
    navigator.clipboard.writeText(btn.dataset.copy).then(done,done);
  }else{done()}
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
  return fetchModels()
    .then(function(cat){
      catalogModels=normalizeCatalog(cat);
      if(!catalogModels.length){
        $("catalogState").textContent="The connected endpoint did not report a model catalog.";
        return;
      }
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
      renderModelCards();
    })
    .catch(function(){
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
    choose.textContent=selected?"Using in Chat":"Use in Chat";
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
}

function setRetention(mode){
  if(mode!=="device"){
    retentionMode="ephemeral";store=conversationStore("ephemeral");
    try{localStorage.setItem("osanwe-retention","ephemeral")}catch(e){}
    $("settingsStatus").textContent="This conversation is now ephemeral. Previously saved history remains until deleted.";
    retentionLabel();return Promise.resolve();
  }
  try{store=conversationStore("device")}catch(e){return storageFailed(e)}
  return store.put(conversation).then(function(){
    retentionMode="device";
    try{localStorage.setItem("osanwe-retention","device")}catch(e){}
    $("settingsStatus").textContent="This conversation is saved only in this browser profile.";
    retentionLabel();
  }).catch(storageFailed);
}

function storageFailed(error){
  retentionMode="ephemeral";store=conversationStore("ephemeral");retentionLabel();
  $("settingsStatus").textContent="Device-only history is unavailable. This conversation remains ephemeral.";
  return Promise.reject(error);
}

function persistConversation(){
  if(retentionMode!=="device")return;
  store.put(conversation).catch(function(error){storageFailed(error).catch(function(){})});
}

function renderConversation(){
  rail.querySelectorAll(".turn").forEach(function(node){node.remove()});
  conversation.turns.forEach(function(item){
    var kind=item.role==="user"?"you":(item.status==="error"?"err":"reply");
    var label=item.role==="user"?"You":"Osanwë";
    var body=turn(kind,label);
    body.textContent=item.content||(item.status==="stopped"?"Stopped":"Incomplete answer");
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
        if(inFlight)inFlight.abort();conversation=record;renderConversation();
        $("settingsDialog").close();$("chatTab").click();
      });
      list.appendChild(button);
    });
  }).catch(function(error){storageFailed(error).catch(function(){})});
}

function openSettings(){
  retentionLabel();$("settingsStatus").textContent="";refreshHistory();$("settingsDialog").showModal();
}
$("settingsBtn").addEventListener("click",openSettings);
$("retentionState").addEventListener("click",openSettings);
$("closeSettingsBtn").addEventListener("click",function(){$("settingsDialog").close()});
document.querySelectorAll("input[name='retention']").forEach(function(input){
  input.addEventListener("change",function(){setRetention(input.value).catch(function(){})});
});
$("deleteHistoryBtn").addEventListener("click",function(){
  var saved;
  try{saved=conversationStore("device")}catch(e){storageFailed(e).catch(function(){});return}
  saved.clear().then(function(){
    retentionMode="ephemeral";store=conversationStore("ephemeral");
    try{localStorage.setItem("osanwe-retention","ephemeral")}catch(e){}
    $("settingsStatus").textContent="All conversations saved by Osanwë in this browser were deleted.";retentionLabel();refreshHistory();
  }).catch(function(e){storageFailed(e).catch(function(){})});
});
$("deleteConversationBtn").addEventListener("click",function(){
  if(!window.confirm("Delete this conversation from this page and device-only history? This cannot delete provider copies or exported files."))return;
  if(inFlight)inFlight.abort();
  var deletedID=conversation.id,saved;
  try{saved=conversationStore("device")}catch(e){saved=null}
  var remove=saved?saved.delete(deletedID):Promise.resolve();
  remove.then(function(){
    conversation=createConversation({model:model.value});renderConversation();refreshHistory();
    $("settingsStatus").textContent="The current conversation was deleted from this page and Osanwë device-only history.";
  }).catch(function(error){storageFailed(error).catch(function(){})});
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
// Three states rather than two, because "follow the system" is a real
// preference and not the absence of one: a machine that switches itself at
// dusk should be able to take this with it.
//
// Theme and model identifiers are the only values kept in localStorage. They
// say nothing about what was asked, or when; conversation text never belongs
// in this small synchronous settings store.
(function(){
  var btn=$("themeBtn"),modes=["auto","light","dark"],
      labels={auto:"Auto",light:"Light",dark:"Dark"},mode="auto";
  try{ mode=localStorage.getItem("osanwe-theme")||"auto" }catch(e){}
  if(modes.indexOf(mode)<0)mode="auto";

  function apply(){
    var root=document.documentElement;
    if(mode==="auto"){
      root.removeAttribute("data-theme");
      root.style.colorScheme="light dark";
    }else{
      root.setAttribute("data-theme",mode);
      // Keeps scrollbars and form controls on the same side as the page.
      root.style.colorScheme=mode;
    }
    btn.textContent=labels[mode];
  }
  btn.addEventListener("click",function(){
    mode=modes[(modes.indexOf(mode)+1)%modes.length];
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
setInterval(function(){if(!busy)load()},10000);
autosize();refresh();
})();
