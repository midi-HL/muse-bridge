import {el,icon,button,chatStream,action,stopChat,readFile,download,APIError} from './core.js';
import {t,date} from './i18n.js';
import {card,note,dialog,confirmAction} from './ui.js';
export function createChat(ctx) {
  let sessionId=ctx.chatSessionId || '', model='muse-spark', messages=[], attachments=[], controller=null, sending=false, sessionTitle='';
  const id=()=>globalThis.crypto?.randomUUID?.() || `web-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  const textOf=message=>message.text || message.content || '';
  function userMessage(text, files){return {role:'user',text,attachments:files?.map(file=>({filename:file.filename,mime:file.mime})) || [],time:Date.now()};}
  function bubble(message,index) {
    const role=message.role || 'assistant', isUser=role==='user';
    return el('article',{class:`message ${isUser?'message-user':'message-assistant'}`,dataset:{index}},el('div',{class:'message-meta'},el('span',{class:'message-avatar'},isUser?'你':'m'),el('strong',{},isUser?t('user'):role==='system'?t('systemMessage'):t('assistant')),message.time?el('time',{},date(message.time)):null),el('div',{class:'message-body'},message.text || (message.pending?t('thinking'):''),message.attachments?.length?el('div',{class:'attachment-list'},message.attachments.map(a=>el('span',{class:'attachment-chip'},icon('file'),a.filename))):null),message.attachment?attachmentView(message.attachment):null);
  }
  function attachmentView(attachment) { const label=attachment.filename || t('generatedFile');const link=attachment.id?el('a',{class:'file-result',href:`/admin/api/files/${encodeURIComponent(attachment.id)}`,download:label},icon('download'),label):el('span',{class:'file-result'},icon('file'),label); return link; }
  let streamList, input, sendButton, stopButton, modelSelect, attachInput, attachmentPreview, status;
  function renderMessages(){streamList.replaceChildren(...messages.map(bubble));streamList.scrollTop=streamList.scrollHeight;}
  async function loadHistory(idValue) { if (!idValue)return; try { const data=await action('sessions.history',{id:idValue}); messages=(data?.messages || []).map(m=>({...m,text:textOf(m),time:m.time || m.created_at}));sessionId=idValue;renderMessages(); } catch(error){ctx.notify(error.message,true); } }
  async function chooseSession(session) { if(sending){ctx.notify(t('cancelGeneration'),true);return;} sessionId=session?.id || '';sessionTitle=session?.title || '';messages=[];ctx.chatSessionId=sessionId;ctx.navigate('chat');await loadHistory(sessionId); }
  function newChat(){if(sending){ctx.notify(t('cancelGeneration'),true);return;}sessionId=id();sessionTitle='';ctx.chatSessionId=sessionId;messages=[];renderMessages();input?.focus();}
  function setSending(value) { sending=value;sendButton.disabled=value;stopButton.hidden=!value;input.disabled=value;attachInput.disabled=value;modelSelect.disabled=value; }
  async function stop(){if(!sending)return;stopButton.disabled=true;status.textContent=t('stopping');try{await stopChat(sessionId);}catch(error){ctx.notify(error.message,true);}finally{controller?.abort();controller=null;setSending(false);status.textContent=t('chatStopped');}}
  async function send(){const prompt=input.value.trim();if(!prompt && !attachments.length)return;if(sending)return;if(!sessionId)newChat();const files=attachments.splice(0);input.value='';attachmentPreview.replaceChildren();messages.push(userMessage(prompt,files));const assistant={role:'assistant',text:'',pending:true,time:Date.now()};messages.push(assistant);renderMessages();setSending(true);status.textContent=t('thinking');controller=new AbortController();
    try { await chatStream({session_id:sessionId,prompt,model,attachments:files},event=>{if(event.session_id && event.session_id!==sessionId){sessionId=event.session_id;ctx.chatSessionId=sessionId;}if(event.type==='delta'){assistant.text+=(event.text || '');assistant.pending=false;renderMessages();}else if(event.type==='status'){status.textContent=event.text || t('thinking');}else if(event.type==='attachment'&&event.attachment){assistant.attachment=event.attachment;renderMessages();}else if(event.type==='error'){throw new Error(event.text || t('chatFailed'));}else if(event.type==='done'){assistant.pending=false;status.textContent=t('success');renderMessages();}},controller.signal); if(assistant.pending){assistant.pending=false;status.textContent=t('streamInterrupted');} }
    catch(error){if(error.name==='AbortError'){assistant.pending=false;assistant.text=assistant.text || t('chatStopped');}else{assistant.pending=false;assistant.text=assistant.text || `${t('chatFailed')}: ${error.message}`;ctx.handleError(error);}renderMessages();}
    finally {controller=null;setSending(false);if(status.textContent===t('thinking'))status.textContent='';}
  }
  async function attach(){const chosen=[...attachInput.files];if(!chosen.length)return;try{const loaded=[];for(const file of chosen){if(file.size>20*1024*1024)throw new Error(t('fileTooLarge'));loaded.push(await readFile(file));}if(attachments.reduce((n,a)=>n+a.data_base64.length,0)+loaded.reduce((n,a)=>n+a.data_base64.length,0)>54*1024*1024)throw new Error(t('attachmentsTooLarge'));attachments.push(...loaded);attachmentPreview.replaceChildren(...attachments.map((a,i)=>el('span',{class:'attachment-chip'},icon('file'),a.filename,button('',()=>{attachments.splice(i,1);attach();},'icon-button','close'))));}catch(error){ctx.notify(error.message,true);}finally{attachInput.value='';}}
  function exportChat(){if(!messages.length){ctx.notify(t('noSessionHistory'),true);return;}download(JSON.stringify({session_id:sessionId,messages},null,2),`${sessionId || 'muse-session'}.json`,'application/json');}
  function build(){
    streamList=el('div',{class:'message-list',role:'log','aria-live':'polite'});
    input=el('textarea',{rows:1,placeholder:t('composerPlaceholder'),'aria-label':t('composerPlaceholder'),onKeyDown:event=>{if(event.key==='Enter'&&!event.shiftKey){event.preventDefault();send();}},onInput:event=>{event.target.style.height='auto';event.target.style.height=Math.min(event.target.scrollHeight,180)+'px';}});
    sendButton=button(t('send'),send,'primary','send');stopButton=button(t('stop'),stop,'danger','stop');stopButton.hidden=true;
    modelSelect=el('select',{value:model,'aria-label':t('model'),onChange:e=>model=e.target.value},['muse-spark','muse-image','muse-video','gpt-4o-mini'].map(v=>el('option',{value:v,text:v})));
    attachInput=el('input',{type:'file',multiple:true,accept:'image/*,audio/*,video/*,.pdf,.txt,.md,.json,.csv',class:'file-input',onChange:attach});attachmentPreview=el('div',{class:'attachment-preview'});status=el('span',{class:'composer-status','aria-live':'polite'});
    const composer=el('form',{class:'composer',onSubmit:e=>{e.preventDefault();send();}},attachmentPreview,el('div',{class:'composer-input'},input),el('div',{class:'composer-tools'},el('label',{class:'icon-button attach-button','title':t('attach')},icon('attach'),attachInput),modelSelect,el('span',{class:'grow'}),status,stopButton,sendButton),el('small',{class:'composer-hint'},t('sendHint')));
    const intro=el('div',{class:'chat-intro'},el('span',{class:'brand-mark'},'m'),el('h2',{},t('chatWelcome')),el('p',{},t('chatWelcomeBody')));
    if(messages.length)intro.hidden=true;
    return el('div',{class:'chat-layout'},el('div',{class:'chat-topbar'},el('div',{},el('span',{class:'eyebrow'},t('chat')),el('h1',{},sessionTitle || t('newChat'))),el('div',{class:'top-actions'},modelSelect.cloneNode(true),button(t('newChat'),newChat,'secondary','plus'),button(t('export'),exportChat,'quiet','download'))),intro,streamList,composer);
  }
  const page={render(){const node=build();streamList=node.querySelector('.message-list');const clonedModel=node.querySelector('.chat-topbar select');if(clonedModel){clonedModel.value=model;clonedModel.addEventListener('change',e=>model=e.target.value);}renderMessages();if(ctx.chatSessionId && ctx.chatSessionId!==sessionId)loadHistory(ctx.chatSessionId);return node;},newChat,chooseSession,stop,isSending:()=>sending};
  return page;
}
