import test from 'node:test';
import assert from 'node:assert/strict';

class FixtureNode {
  constructor(tag, namespaceURI='http://www.w3.org/1999/xhtml') {
    this.tagName=tag.toUpperCase();this.namespaceURI=namespaceURI;this.children=[];this.attributes={};this.listeners={};this.dataset={};this.style={};this.className='';this.value='';this._textContent='';this.disabled=false;this.hidden=false;this.files=[];
  }
  get textContent(){return this._textContent || this.children.map(child=>child.textContent).join('');}
  set textContent(value){this._textContent=String(value);this.children=[];}
  append(...nodes){this.children.push(...nodes);}
  replaceChildren(...nodes){this.children=[...nodes];this._textContent='';}
  addEventListener(type,fn){this.listeners[type]=fn;}
  setAttribute(name,value){this.attributes[name]=String(value);}
  getAttribute(name){return this.attributes[name] ?? null;}
  get scrollHeight(){return 0;}
  focus(){}
  click(){}
  cloneNode(deep=false){const copy=new FixtureNode(this.tagName,this.namespaceURI);Object.assign(copy,{...this,children:[],listeners:{},attributes:{...this.attributes},dataset:{...this.dataset},style:{...this.style}});if(deep)copy.children=this.children.map(child=>child.cloneNode(true));return copy;}
  dispatch(type,event={}){const listener=this.listeners[type];if(!listener)return;return listener({target:this,preventDefault(){},...event});}
  querySelector(selector){return this.querySelectorAll(selector)[0] || null;}
  querySelectorAll(selector){
    const matches=node=>selector.startsWith('.')?String(node.className).split(/\s+/).includes(selector.slice(1)):selector.startsWith('[')?node.getAttribute(selector.slice(1,-1).split('=')[0])===selector.slice(1,-1).split('=')[1]?.replace(/^['"]|['"]$/g,''):selector.toLowerCase().split(' ').at(-1)===node.tagName.toLowerCase();
    const out=[];const walk=node=>{for(const child of node.children){if(matches(child))out.push(child);walk(child);}};walk(this);return out;
  }
  get innerText(){return this.textContent;}
}
class FixtureDocument {
  constructor(){this.documentElement=new FixtureNode('html');}
  createElement(tag){return new FixtureNode(tag);}
  createElementNS(namespace,tag){return new FixtureNode(tag,namespace);}
  createTextNode(text){const node=new FixtureNode('#text');node.textContent=String(text);return node;}
}

const deferred=()=>{let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no;});return {promise,resolve,reject};};
function json(data){return new Response(JSON.stringify(data),{status:200,headers:{'Content-Type':'application/json'}});}
function sse(data){const text=`data: ${JSON.stringify(data)}\n\ndata: [DONE]\n\n`;return new Response(new ReadableStream({start(controller){controller.enqueue(new TextEncoder().encode(text));controller.close();}}),{status:200,headers:{'Content-Type':'text/event-stream'}});}
function makeHarness({personas=[],history=()=>({messages:[],session:{persona_selected:false}}),chat=()=>sse({type:'done'})}={}){
  globalThis.document=new FixtureDocument();globalThis.Node=FixtureNode;
  const requests=[],notices=[];let historyHandler=history,chatHandler=chat;
  globalThis.fetch=async(url,options={})=>{
    const body=options.body?JSON.parse(options.body):{};
    if(String(url).endsWith('/action')){
      requests.push({type:'history',body});const result=await historyHandler(body.args.id);return json({data:result});
    }
    if(String(url).endsWith('/chat')){requests.push({type:'chat',body});return chatHandler(body,options.signal);}
    if(String(url).endsWith('/chat/stop'))return json({});
    throw new Error(`Unexpected request: ${url}`);
  };
  const ctx={chatSessionId:'',state:{personas},navigate(page){this.page=page;},notify(message,error){notices.push({message,error});},handleError(error){notices.push({message:error.message,error:true});}};
  return {ctx,requests,notices,setHistory(handler){historyHandler=handler;},setChat(handler){chatHandler=handler;}};
}
function findByLabel(root,label){return root.querySelectorAll('select').find(node=>node.getAttribute('aria-label')===label);}
async function submit(root,text='hello'){
  const input=root.querySelector('textarea');input.value=text;
  await root.querySelector('form').dispatch('submit');
}
async function createHarness(options){
  const h=makeHarness(options);const {createChat}=await import('../static/chat.js');const page=createChat(h.ctx);const root=page.render();return {...h,page,root};
}

test('chat has a single working model selector and labelled icon-only actions',async()=>{
  const h=await createHarness({});
  assert.equal(h.root.querySelectorAll('select').filter(n=>n.getAttribute('aria-label')==='模型').length,1);
  const {button}=await import('../static/core.js');
  const refresh=button('刷新',()=>{},'icon-button','refresh');
  assert.equal(refresh.getAttribute('aria-label'),'刷新');
  assert.equal(refresh.textContent,'');
});

test('the first conversation hides the empty state and exposes its messages',async()=>{
  const h=await createHarness({});
  assert.equal(h.root.querySelector('.chat-intro').hidden,false);
  await submit(h.root,'first message');
  assert.equal(h.root.querySelector('.chat-intro').hidden,true);
  assert.ok(h.root.querySelectorAll('.message-body').length>=2);
  h.page.newChat();
  assert.equal(h.root.querySelector('.chat-intro').hidden,false);
});

test('createChat sends persona only when explicitly selected',async()=>{
  const h=await createHarness({personas:[{id:'helpful',name:'Helpful'},{id:'off',name:'Disabled',enabled:false}]});
  const select=findByLabel(h.root,'人设');
  assert.ok(select);
  assert.deepEqual(select.children.map(option=>option.value),['','_none','helpful']);

  select.value='helpful';select.dispatch('change');await submit(h.root);
  assert.equal(h.requests.filter(r=>r.type==='chat')[0].body.persona_id,'helpful');

  h.page.newChat();select.value='_none';select.dispatch('change');await submit(h.root);
  assert.equal(h.requests.filter(r=>r.type==='chat')[1].body.persona_id,'_none');

  h.page.newChat();assert.equal(select.value,'');await submit(h.root);
  const defaultPayload=h.requests.filter(r=>r.type==='chat')[2].body;
  assert.equal(Object.hasOwn(defaultPayload,'persona_id'),false);
});

test('createChat refuses a persona disabled after it was selected',async()=>{
  const h=await createHarness({personas:[{id:'p1',name:'Preset'}]});
  const select=findByLabel(h.root,'人设');select.value='p1';select.dispatch('change');
  h.ctx.state.personas[0].enabled=false;
  await submit(h.root);
  assert.equal(h.requests.filter(r=>r.type==='chat').length,0);
  assert.equal(h.notices.length,1);
  assert.match(h.notices[0].message,/不存在或已停用/);
});

test('late history from an earlier session cannot replace selected session or its persona pin',async()=>{
  const pending=new Map();
  const h=await createHarness({history:id=>{const wait=deferred();pending.set(id,wait);return wait.promise;}});
  const first=h.page.chooseSession({id:'first',title:'First'});
  const second=h.page.chooseSession({id:'second',title:'Second'});
  pending.get('second').resolve({messages:[{role:'assistant',text:'second history'}],session:{persona_selected:true,persona_id:'p1'}});
  await second;
  pending.get('first').resolve({messages:[{role:'assistant',text:'stale first history'}],session:{persona_selected:true,persona_id:'old'}});
  await first;
  assert.equal(h.ctx.chatSessionId,'second');
  assert.deepEqual(h.root.querySelectorAll('.message-body').map(node=>node.textContent),['second history']);
  assert.equal(findByLabel(h.root,'人设').value,'p1');
});

test('history responses do not replace a persona selected before or after the request while chat is hidden',async()=>{
  const h=await createHarness({history:async()=>({messages:[],session:{persona_selected:true,persona_id:'p1'}})});
  await h.page.chooseSession({id:'existing'});
  const select=findByLabel(h.root,'人设');select.value='p2';select.dispatch('change');

  const pending=deferred();h.setHistory(()=>pending.promise);
  const refresh=h.page.refreshHistory();
  select.value='_none';select.dispatch('change');
  h.ctx.page='sessions';
  pending.resolve({messages:[],session:{persona_selected:true,persona_id:'p1'}});await refresh;
  assert.equal(select.value,'_none');

  const later=deferred();h.setHistory(()=>later.promise);
  const hiddenRefresh=h.page.refreshHistory();
  select.value='p2';select.dispatch('change');
  later.resolve({messages:[],session:{persona_selected:true,persona_id:'p1'}});await hiddenRefresh;
  assert.equal(select.value,'p2');
});

test('successful send clears persona dirty state but failed send leaves it editable',async()=>{
  let fail=true;
  const h=await createHarness({personas:[{id:'p1',name:'One'},{id:'p2',name:'Two'}],chat:()=>sse(fail?{type:'error',text:'failed'}:{type:'done'})});
  const select=findByLabel(h.root,'人设');select.value='p2';select.dispatch('change');
  await submit(h.root);
  assert.equal(select.value,'p2');
  const failedPayload=h.requests.find(request=>request.type==='chat').body;
  assert.equal(failedPayload.persona_id,'p2');

  fail=false;select.value='p1';select.dispatch('change');await submit(h.root,'retry');
  assert.equal(select.disabled,false);
  h.setHistory(async()=>({messages:[],session:{persona_selected:true,persona_id:'p1'}}));
  await h.page.refreshHistory();
  assert.equal(select.value,'p1');
});

test('history pin with empty persona id selects no preset',async()=>{
  const h=await createHarness({history:async()=>({messages:[],session:{persona_selected:true,persona_id:''}})});
  await h.page.chooseSession({id:'pinned-none'});
  assert.equal(findByLabel(h.root,'人设').value,'_none');
});

test('persona selector is locked while generation is active',async()=>{
  const response=deferred();
  const h=await createHarness({personas:[{id:'p1',name:'Preset'}],chat:()=>response.promise});
  const sending=submit(h.root);
  assert.equal(findByLabel(h.root,'人设').disabled,true);
  response.resolve(sse({type:'done'}));await sending;
  assert.equal(findByLabel(h.root,'人设').disabled,false);
});

const queueStatus='当前服务繁忙，正在排队，请稍候。';
for(const {name,terminal,expectedStatus,expectedMessage,notice} of [
  {name:'error',terminal:{type:'error',error:{code:'timeout',message:'Request timed out'},text:'Request timed out'},expectedStatus:'生成失败',expectedMessage:'生成失败: Request timed out',notice:'Request timed out'},
  {name:'server cancellation',terminal:{type:'error',error:{code:'cancelled',message:'Request cancelled'},text:'Request cancelled'},expectedStatus:'已停止生成',expectedMessage:'已停止生成'},
  {name:'stream abort',terminal:new DOMException('The operation was aborted','AbortError'),expectedStatus:'已停止生成',expectedMessage:'已停止生成'},
]){
  test(`createChat replaces queue status after ${name}`,async()=>{
    const queued=deferred(),finish=deferred(),encoder=new TextEncoder();
    const frame=event=>encoder.encode(`data: ${JSON.stringify(event)}\n\n`);
    let sentStatus=false;
    const response=new Response(new ReadableStream({
      async pull(controller){
        if(!sentStatus){
          sentStatus=true;
          controller.enqueue(frame({type:'status',text:queueStatus}));
          return;
        }
        // The next read starts after the component has processed the status event.
        queued.resolve();
        await finish.promise;
        if(terminal instanceof Error){controller.error(terminal);return;}
        controller.enqueue(frame(terminal));
        controller.close();
      },
    },{highWaterMark:0}),{headers:{'Content-Type':'text/event-stream'}});
    const h=await createHarness({chat:()=>response});
    const status=h.root.querySelector('.composer-status');
    const sending=submit(h.root);
    await queued.promise;
    assert.equal(status.textContent,queueStatus);
    assert.equal(h.root.querySelector('textarea').disabled,true);

    finish.resolve();
    await sending;
    assert.equal(status.textContent,expectedStatus);
    assert.equal(h.root.querySelector('textarea').disabled,false);
    assert.equal(h.root.querySelectorAll('.message-body').at(-1).textContent,expectedMessage);
    assert.deepEqual(h.notices,notice?[{message:notice,error:true}]:[]);
  });
}

test('history refresh started before send cannot overwrite the sent transcript',async()=>{
  const pending=deferred();
  const h=await createHarness({history:()=>pending.promise});
  const session=h.page.chooseSession({id:'active'});
  const initial=h.requests.find(r=>r.type==='history');
  pending.resolve({messages:[],session:{persona_selected:false}});await session;
  assert.equal(initial.body.args.id,'active');

  const refreshReply=deferred();
  h.setHistory(()=>refreshReply.promise);
  h.page.refreshHistory();
  await submit(h.root,'sent after refresh');
  refreshReply.resolve({messages:[{role:'assistant',text:'stale refresh'}],session:{persona_selected:true,persona_id:'p1'}});
  await Promise.resolve();await Promise.resolve();
  assert.deepEqual(h.root.querySelectorAll('.message-body').map(node=>node.textContent),['sent after refresh','']);
  assert.equal(findByLabel(h.root,'人设').value,'');
});
