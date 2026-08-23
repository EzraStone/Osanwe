import { loadModels as fetchModels, loadStatus as fetchStatus, sendMessages } from "./api.js";
import { appendTurn, createConversation, toRequestMessages } from "./conversation.js";
import { anthropicTextDelta, SSEParser } from "./sse.js";

(function(){
"use strict";
var PREFIX="/_osanwe/";

var $=function(id){return document.getElementById(id)};
var thread=$("thread"),rail=$("rail"),opening=$("opening"),byok=$("byokNotice"),
    input=$("input"),send=$("send"),seal=$("seal"),state=$("state"),model=$("model"),
    panel=$("panel"),veil=$("veil"),chatView=$("chatView"),devView=$("devView");

var status=null,busy=false,broken=false,inFlight=null,
    conversation=createConversation({model:model.value});

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
  facts.forEach(function(p){
    var k=document.createElement("span");k.className="k";k.textContent=p[0];
    var v=document.createElement("b");v.textContent=p[1];
    f.appendChild(k);f.appendChild(v);f.appendChild(document.createElement("br"));
  });
}

function openPanel(o){
  if(o)fillPanel();
  panel.classList.toggle("open",o);veil.classList.toggle("open",o);
  if(o)$("closePanel").focus();else seal.focus();
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
      busy=false;refresh();return;
    }
    reply.status="error";
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
document.addEventListener("keydown",function(e){if(e.key==="Escape")openPanel(false)});

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
    });
    chatView.hidden=want!=="chat";devView.hidden=want!=="dev";
    if(want==="dev")load();else input.focus();
  });
});

document.querySelectorAll("[data-snip]").forEach(function(btn){
  btn.addEventListener("click",function(){
    document.querySelectorAll("[data-snip]").forEach(function(b){
      b.setAttribute("aria-selected",String(b===btn));
    });
    showSnippet(btn.dataset.snip);
  });
});

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
      if(!cat||!cat.data||!cat.data.length)return;   // no routing: keep the default
      var current=model.value;
      model.textContent="";
      cat.data.forEach(function(m){
        var o=document.createElement("option");
        o.value=m.id;o.textContent=m.id;
        model.appendChild(o);
      });
      // Keep the selection if it survived, otherwise take the first.
      if(cat.data.some(function(m){return m.id===current}))model.value=current;
    })
    .catch(function(){ /* single-provider gateway; the default stands */ });
}

model.addEventListener("change",function(){conversation.model=model.value});

// ---- appearance -----------------------------------------------------
// Three states rather than two, because "follow the system" is a real
// preference and not the absence of one: a machine that switches itself at
// dusk should be able to take this with it.
//
// The choice is the only thing this page stores. It says nothing about what
// was asked, of whom, or when -- and a window that forgot which way round it
// was every time you opened it would be its own small annoyance.
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
load().then(loadModels);
// The client is local, so polling it is nearly free, and a relay that failed
// over should not sit behind a stale label until the page is reloaded.
setInterval(function(){if(!busy)load()},10000);
autosize();refresh();
})();
