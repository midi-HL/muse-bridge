import {el,icon,button,chatStream,action,stopChat,readFile,download,APIError} from './core.js';
import {t,date} from './i18n.js';
import {card,note,dialog,confirmAction} from './ui.js';
export function createChat(ctx) {
  let sessionId=ctx.chatSessionId || '', model='muse-spark', messages=[], attachments=[], controller=null, sending=false, sessionTitle='', personaId='';
  let sessionGeneration=0, personaSelectionGeneration=0, personaDirty=false;
  const id=()=>globalThis.crypto?.randomUUID?.() || `web-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  const textOf=message=>message.text || message.content || '';
  function userMessage(text, files){return {role:'user',text,attachments:files?.map(file=>({filename:file.filename,mime:file.mime})) || [],time:Date.now()};}
  function bubble(message,index) {
    const role=message.role || 'assistant', isUser=role==='user';
    return el('article',{class:`message ${isUser?'message-user':'message-assistant'}`,dataset:{index}},el('div',{class:'message-meta'},el('span',{class:'message-avatar'},isUser?'你':'m'),el('strong',{},isUser?t('user'):role==='system'?t('systemMessage'):t('assistant')),message.time?el('time',{},date(message.time)):null),el('div',{class:'message-body'},message.text || (message.pending?t('thinking'):''),message.attachments?.length?el('div',{class:'attachment-list'},message.attachments.map(a=>el('span',{class:'attachment-chip'},icon('file'),a.filename))):null),message.attachment?attachmentView(message.attachment):null);
  }
  function attachmentView(attachment) { const label=attachment.filename || t('generatedFile');const link=attachment.id?el('a',{class:'file-result',href:`/admin/api/files/${encodeURIComponent(attachment.id)}`,download:label},icon('download'),label):el('span',{class:'file-result'},icon('file'),label); return link; }
  let streamList, input, sendButton, stopButton, modelSelect, personaSelect, attachInput, attachmentPreview, status;
  function renderMessages(){streamList.replaceChildren(...messages.map(bubble));streamList.scrollTop=streamList.scrollHeight;}
  let historyRequest=0;
  const activePersonas=()=>((ctx.state?.personas || []).filter(persona=>persona.enabled!==false));
  function syncPersonaOptions(){
    if(!personaSelect)return;
    const options=[el('option',{value:'',text:t('personaDefault')}),el('option',{value:'_none',text:t('personaNone')}),...activePersonas().map(persona=>el('option',{value:String(persona.id),text:persona.name || String(persona.id)}))];
    const selectedExists=personaId===''||personaId==='_none'||activePersonas().some(persona=>String(persona.id)===personaId);
    if(!selectedExists)options.push(el('option',{value:personaId,text:`${t('personaUnavailable')}: ${personaId}`,disabled:true}));
    personaSelect.replaceChildren(...options);personaSelect.value=personaId;
  }
  function setPersona(value){personaId=value;syncPersonaOptions();}
  function selectPersona(value){personaId=value;personaSelectionGeneration++;personaDirty=true;syncPersonaOptions();if(status)status.textContent='';}
  async function loadHistory(idValue, quiet=false) {
    if(!idValue)return;
    const requestId=++historyRequest, generation=sessionGeneration, personaGeneration=personaSelectionGeneration;
    try {
      const data=await action('sessions.history',{id:idValue});
      if(requestId!==historyRequest||generation!==sessionGeneration||idValue!==sessionId||sending)return;
      const incoming=(data?.messages || []).map(m=>({...m,text:textOf(m),time:m.time || m.created_at}));
      const key=m=>`${m.role||''}\u0000${m.text||''}`;
      if(incoming.length!==messages.length||incoming.some((m,i)=>key(m)!==key(messages[i]))){messages=incoming;renderMessages();}
      if(!personaDirty&&personaGeneration===personaSelectionGeneration){
        if(data?.session?.persona_selected===true)setPersona(data.session.persona_id ? String(data.session.persona_id) : '_none');
        else if(data?.session?.persona_selected===false)setPersona('');
      }
    } catch(error){if(!quiet&&generation===sessionGeneration&&idValue===sessionId)ctx.notify(error.message,true);}
  }
  function refreshHistory(){if(sessionId&&!sending)return loadHistory(sessionId,true);}
  function switchSession(sessionIdValue,title=''){
    sessionGeneration++;historyRequest++;personaSelectionGeneration++;personaDirty=false;sessionId=sessionIdValue || '';sessionTitle=title;personaId='';messages=[];syncPersonaOptions();ctx.chatSessionId=sessionId;renderMessagesIfReady();
  }
  function renderMessagesIfReady(){if(streamList)renderMessages();}
  async function chooseSession(session) {
    if(sending){ctx.notify(t('cancelGeneration'),true);return;}
    switchSession(session?.id,session?.title || '');ctx.navigate('chat');await loadHistory(sessionId);
  }
  function newChat(){
    if(sending){ctx.notify(t('cancelGeneration'),true);return;}
    sessionGeneration++;historyRequest++;personaSelectionGeneration++;personaDirty=false;sessionId=id();sessionTitle='';personaId='';syncPersonaOptions();ctx.chatSessionId=sessionId;messages=[];renderMessagesIfReady();input?.focus();
  }
  function setSending(value) { sending=value;sendButton.disabled=value;stopButton.hidden=!value;input.disabled=value;attachInput.disabled=value;modelSelect.disabled=value;personaSelect.disabled=value; }
  async function stop(){if(!sending)return;stopButton.disabled=true;status.textContent=t('stopping');try{await stopChat(sessionId);}catch(error){ctx.notify(error.message,true);}finally{controller?.abort();controller=null;setSending(false);status.textContent=t('chatStopped');}}
  async function send(){
    const prompt=input.value.trim();if(!prompt && !attachments.length)return;if(sending)return;
    if(personaId && personaId!=='_none'&&!activePersonas().some(persona=>String(persona.id)===personaId)){
      const message=`${t('personaUnavailable')}: ${personaId}`;ctx.notify(message,true);status.textContent=message;return;
    }
    if(!sessionId){sessionId=id();ctx.chatSessionId=sessionId;}else if(ctx.chatSessionId && ctx.chatSessionId!==sessionId){switchSession(ctx.chatSessionId);}
    const generation=sessionGeneration, requestSession=sessionId, selectedPersonaGeneration=personaSelectionGeneration;
    historyRequest++;
    const files=attachments.splice(0);input.value='';attachmentPreview.replaceChildren();messages.push(userMessage(prompt,files));const assistant={role:'assistant',text:'',pending:true,time:Date.now()};messages.push(assistant);renderMessages();setSending(true);status.textContent=t('thinking');controller=new AbortController();
    const payload={session_id:requestSession,prompt,model,attachments:files,...(personaId?{persona_id:personaId}:{})};
    try { await chatStream(payload,event=>{
      if(generation!==sessionGeneration)return;
      if(event.session_id && event.session_id!==sessionId){sessionId=event.session_id;ctx.chatSessionId=sessionId;}
      if(event.type==='error'){
        if(event.error?.code==='cancelled')throw new DOMException(t('chatStopped'),'AbortError');
        throw new Error(event.text || t('chatFailed'));
      }
      if(event.type==='delta'){assistant.text+=(event.text || '');assistant.pending=false;renderMessages();}else if(event.type==='queued'||event.type==='status'){status.textContent=event.text || t('thinking');}else if(event.type==='attachment'&&event.attachment){assistant.attachment=event.attachment;renderMessages();}else if(event.type==='done'){assistant.pending=false;if(selectedPersonaGeneration===personaSelectionGeneration)personaDirty=false;status.textContent=t('success');renderMessages();}
    },controller.signal); if(generation===sessionGeneration&&assistant.pending){assistant.pending=false;status.textContent=t('streamInterrupted');} }
    catch(error){
      if(generation!==sessionGeneration)return;
      assistant.pending=false;
      if(error.name==='AbortError'){
        status.textContent=t('chatStopped');
        assistant.text=assistant.text || t('chatStopped');
      }else{
        status.textContent=t('chatFailed');
        assistant.text=assistant.text || `${t('chatFailed')}: ${error.message}`;
        ctx.handleError(error);
      }
      renderMessages();
    }
    finally {if(generation===sessionGeneration){controller=null;setSending(false);if(status.textContent===t('thinking'))status.textContent='';}}
  }
  async function attach(){const chosen=[...attachInput.files];if(!chosen.length)return;try{const loaded=[];for(const file of chosen){if(file.size>20*1024*1024)throw new Error(t('fileTooLarge'));loaded.push(await readFile(file));}if(attachments.reduce((n,a)=>n+a.data_base64.length,0)+loaded.reduce((n,a)=>n+a.data_base64.length,0)>54*1024*1024)throw new Error(t('attachmentsTooLarge'));attachments.push(...loaded);attachmentPreview.replaceChildren(...attachments.map((a,i)=>el('span',{class:'attachment-chip'},icon('file'),a.filename,button('',()=>{attachments.splice(i,1);attach();},'icon-button','close'))));}catch(error){ctx.notify(error.message,true);}finally{attachInput.value='';}}
  function exportChat(){if(!messages.length){ctx.notify(t('noSessionHistory'),true);return;}download(JSON.stringify({session_id:sessionId,messages},null,2),`${sessionId || 'muse-session'}.json`,'application/json');}
  function build(){
    streamList=el('div',{class:'message-list',role:'log','aria-live':'polite'});
    input=el('textarea',{rows:1,placeholder:t('composerPlaceholder'),'aria-label':t('composerPlaceholder'),onKeyDown:event=>{if(event.key==='Enter'&&!event.shiftKey){event.preventDefault();send();}},onInput:event=>{event.target.style.height='auto';event.target.style.height=Math.min(event.target.scrollHeight,180)+'px';}});
    sendButton=button(t('send'),send,'primary','send');stopButton=button(t('stop'),stop,'danger','stop');stopButton.hidden=true;
    modelSelect=el('select',{value:model,'aria-label':t('model'),onChange:e=>model=e.target.value},['muse-spark','muse-image','muse-video','gpt-4o-mini'].map(v=>el('option',{value:v,text:v})));
    personaSelect=el('select',{'aria-label':t('persona'),onChange:e=>selectPersona(e.target.value)});syncPersonaOptions();
    attachInput=el('input',{type:'file',multiple:true,accept:'image/*,audio/*,video/*,.pdf,.txt,.md,.json,.csv',class:'file-input',onChange:attach});attachmentPreview=el('div',{class:'attachment-preview'});status=el('span',{class:'composer-status','aria-live':'polite'});
    const composer=el('form',{class:'composer',onSubmit:e=>{e.preventDefault();return send();}},attachmentPreview,el('div',{class:'composer-input'},input),el('div',{class:'composer-tools'},el('label',{class:'icon-button attach-button','title':t('attach')},icon('attach'),attachInput),modelSelect,personaSelect,el('span',{class:'grow'}),status,stopButton,sendButton),el('small',{class:'composer-hint persona-hint'},t('personaWarning')),el('small',{class:'composer-hint'},t('sendHint')));
    const intro=el('div',{class:'chat-intro'},el('span',{class:'brand-mark'},'m'),el('h2',{},t('chatWelcome')),el('p',{},t('chatWelcomeBody')));
    if(messages.length)intro.hidden=true;
    return el('div',{class:'chat-layout'},el('div',{class:'chat-topbar'},el('div',{},el('span',{class:'eyebrow'},t('chat')),el('h1',{},sessionTitle || t('newChat'))),el('div',{class:'top-actions'},modelSelect.cloneNode(true),button(t('newChat'),newChat,'secondary','plus'),button(t('export'),exportChat,'quiet','download'))),intro,streamList,composer);
  }
  const page={render(){const node=build();streamList=node.querySelector('.message-list');const clonedModel=node.querySelector('.chat-topbar select');if(clonedModel){clonedModel.value=model;clonedModel.addEventListener('change',e=>model=e.target.value);}renderMessages();if(ctx.chatSessionId && ctx.chatSessionId!==sessionId){switchSession(ctx.chatSessionId);loadHistory(ctx.chatSessionId);}return node;},newChat,chooseSession,stop,refreshHistory,isSending:()=>sending};
  return page;
}
