import {el,icon,button,badge,action,pretty,redact,safeURL,download,readFile} from './core.js';
import {t,date,schemaText,language} from './i18n.js';
import {empty,card,note,resultView,form,dialog,confirmAction,table,searchBox,toolbar,rowActions,stat} from './ui.js';
const f=(key,label,type='string',extra={})=>({key,label:t(label),type,...extra});
const nameOf=row=>row.name || row.label || row.id || '—';
const text=value=>value==null?'—':typeof value==='object'?pretty(value):String(value);
const options=values=>values.map(([value,label])=>({value,label}));
const groups={general:['通用','General'],limits:['限流与排队','Limits and queue'],alerts:['告警','Alerts'],media:['媒体处理','Media processing'],platforms:['平台连接','Platforms'],accounts:['账号池','Account pool'],nodes:['代理节点','Proxy nodes'],asr:['语音识别','Speech recognition'],stickers:['贴纸','Stickers'],personas:['人设','Personas'],security:['安全','Security']};
function groupLabel(group){return groups[group]?.[language==='en'?1:0] || group;}
export function createPages(ctx) {
  const results=new Map();
  let search='',filter='all',selectedGroup='all';
  const pageResults=()=>[...results.entries()].filter(([key])=>key.startsWith(ctx.page+':')).reverse().map(([key,result])=>resultView(result));
  async function perform(name,args={},opts={}) {
    const key=`${ctx.page}:${opts.key || name+':'+(args.id || '')}`, page=ctx.page;
    if(results.get(key)?.pending)return;
    results.set(key,{pending:true});ctx.renderContent();
    try {
      const data=await action(name,args);
      if(opts.download)download(data?.content ?? data?.blob ?? data,data?.filename || opts.filename,data?.mime);
      results.set(key,{data:opts.download?{filename:data?.filename || opts.filename}:redact(data ?? {ok:true})});
      await ctx.refresh(false);
      if(data?.requires_restart)ctx.notify(language==='zh'?'备份已导入，重启服务后生效。':'Backup imported. Restart the service to apply it.');
      if(ctx.page===page)ctx.renderContent();
      return data;
    } catch(error){if(name==='accounts.probe')await ctx.refresh(false).catch(()=>{});results.set(key,{error:true,data:error.message});if(ctx.page===page)ctx.renderContent();ctx.handleError(error);throw error;}
  }
  const run=(name,args,opts)=>()=>perform(name,args,opts).catch(()=>{});
  async function danger(name,args,title,opts){if(await confirmAction(title))await perform(name,args,opts).catch(()=>{});}
  function edit(title,fields,values,name,transform=value=>value) {dialog(title,close=>form(fields,values,async args=>{await perform(name,transform(args));close();}),{wide:fields.length>5});}
  function listPage(items,columns,emptyText,primary,extra) {
    const body=el('div');
    const draw=()=>{const q=search.toLowerCase();const rows=items.filter(row=>(!q || JSON.stringify(redact(row)).toLowerCase().includes(q)) && (filter==='all' || row.status===filter || (filter==='enabled'&&row.enabled)||(filter==='disabled'&&!row.enabled)));
      body.replaceChildren(rows.length?table(columns,rows):empty(items.length?t('noMatches'):t('noData'),items.length?t('searchPlaceholder'):emptyText,items.length?button(t('clearFilter'),()=>{search='';filter='all';ctx.renderContent();}):primary));};
    draw();
    return el('div',{class:'page-stack'},toolbar(searchBox(value=>{search=value;draw();}),extra,primary),body,...pageResults());
  }
  function settingsGroups(wanted) {
    const fields=(ctx.schema.settings || []).filter(field=>!wanted || wanted.includes(field.group));
    if(!fields.length)return note(t('noSchema'),'warning');
    return fields.reduce((out,field)=>{if(!out.includes(field.group))out.push(field.group);return out;},[]).map(group=>card(groupLabel(group),form(fields.filter(field=>field.group===group),ctx.state.settings || {},async (values,root)=>{
      await action('settings.update',{values});
      root.controls.filter(c=>c.field.secret).forEach(c=>{c.input.value='';});
      await ctx.refresh(false);
    },t('saveSettings'),{columns:true}),null));
  }
  function accountForm(account) {edit(t('edit')+' · '+nameOf(account),[f('label','label'),f('note','note','textarea'),f('enabled','enabled','boolean'),f('node_id','node','select',{options:options([['',t('autoNode')],...(ctx.state.nodes || []).map(n=>[n.id,n.name])])}),f('pool','pool','select',{options:options([['shared',t('shared')],['chat',t('chat')],['api','API']])})],{enabled:true,pool:'shared',...account},'accounts.update',args=>({id:account.id,...args}));}
  function accountImport(migrate=false) {
    dialog(migrate?t('migration'):t('accountImport'),close=>{
      const input=el('input',{type:'file',accept:'.json,.txt,.yaml,.yml',class:'file-input'});
      const importForm=form([f('text','importText','textarea',{required:true,rows:10})],{},async args=>{await perform('accounts.import',args);importForm.reset();close();},t('importSubmit'));
      input.addEventListener('change',async()=>{try{const file=input.files[0];if(!file)return;if(file.size>10*1024*1024)throw new Error(t('fileTooLarge'));importForm.controls[0].input.value=await file.text();}catch(e){ctx.notify(e.message,true);}});
      return el('div',{},note(t('importHint')),el('label',{class:'file-picker'},icon('upload'),t('chooseFile'),input),importForm);
    },{wide:true});
  }
  function nodeForm(node={}) {edit(node.id?t('edit'):t('nodeAdd'),[f('name','name','string',{required:true}),f('url','nodeURL','string',{required:!node.id,description:t('nodeURLHint'),placeholder:node.id?t('keepSecret'):'socks5://127.0.0.1:1080'}),f('enabled','enabled','boolean')],{enabled:true,...node,url:''},'nodes.save',args=>({...(node.id?{id:node.id}:{}),...args,...(!args.url&&node.id?{url:undefined}:{})}));}
  function keyCreate() {dialog(t('keyCreate'),close=>form([f('name','keyName','string',{required:true}),f('rpm','rpm','integer',{min:0,default:60}),f('daily','daily','integer',{min:0,default:1000})],{},async args=>{
    const data=await action('keys.create',args);close();await ctx.refresh(false);
    if(!data?.key)throw new Error(t('failed'));
    let fullKey=data.key;
    dialog(t('keyOnce'),closeKey=>{
      const output=el('textarea',{class:'one-time-key',readOnly:true,value:fullKey,rows:3});
      return el('div',{},note(t('keyOnceHint'),'warning'),output,el('p',{class:'muted'},t('keySafety')),el('div',{class:'form-actions'},button(t('copy'),async()=>{try{await navigator.clipboard.writeText(fullKey);ctx.notify(t('copied'));}catch{output.focus();output.select();}},'primary','copy'),button(t('keySaved'),closeKey)));
    },{onClose:()=>{fullKey='';data.key='';}});
  },t('keyCreate')));}
  function exportAction(name,args,filename){return perform(name,args,{download:true,filename});}
  function backupPanel() {
    const exportForm=form([f('passphrase','passphrase','password',{required:true})],{},async(args,root)=>{await exportAction('backup.export',args,'muse-backup.enc');root.reset();},t('backupExport'),{icon:'download'});
    const importForm=form([f('passphrase','passphrase','password',{required:true}),f('blob','backupPaste','textarea',{required:true,rows:5})],{},async(args,root)=>{if(await confirmAction(t('backupImport'),t('backupHint'))){await perform('backup.import',args);root.reset();}},t('backupImport'),{icon:'upload'});
    const file=el('input',{type:'file',accept:'.enc,.json,.txt',onChange:async event=>{try{const file=event.target.files[0];if(!file)return;if(file.size>20*1024*1024)throw new Error(t('fileTooLarge'));importForm.controls.find(c=>c.key==='blob').input.value=await file.text();}catch(error){ctx.notify(error.message,true);}}});
    return card(t('backup'),el('div',{},note(t('backupHint')),el('div',{class:'two-col'},exportForm,el('div',{},el('label',{class:'file-picker'},icon('upload'),t('backupFile'),file),importForm))));
  }
  function scheduleForm(schedule={}) {edit(schedule.id?t('edit'):t('scheduleAdd'),[f('name','name','string',{required:true}),f('enabled','enabled','boolean'),f('platform','platform','select',{options:['telegram','web']}),f('target','target','string',{required:true,description:t('targetHint')}),f('prompt','prompt','textarea',{required:true}),f('cron','cron','string',{required:true,description:t('cronHint'),pattern:'^\\S+\\s+\\S+\\s+\\S+\\s+\\S+\\s+\\S+$'}),f('timezone','timezone','string',{required:true})],{enabled:true,platform:'telegram',cron:'0 9 * * *',timezone:'Asia/Kuala_Lumpur',...schedule},'schedules.save',args=>({...args,...(schedule.id?{id:schedule.id}:{})}));}
  const views={
    overview(){
      const s=ctx.state, accounts=s.accounts || [], stats=s.stats || {}, healthy=accounts.filter(a=>a.enabled!==false&&['ok','available'].includes(a.status)).length;
      const protocol=stats.protocol_status ?? stats.protocol?.status ?? s.protocol?.status ?? 'unknown';
      const health=accounts.length?el('div',{class:'health-list'},accounts.slice(0,8).map(a=>el('div',{class:'health-row'},el('span',{class:'avatar small'},String(nameOf(a)).slice(0,1)),el('div',{class:'grow'},el('strong',{},nameOf(a)),el('small',{class:'muted block'},date(a.last_probe_at || a.last_probe))),badge(a.status)))):empty(t('quickStart'),t('quickStartBody'),button(t('accountImport'),()=>accountImport(),'primary','plus'),'accounts');
      return el('div',{},el('div',{class:'stats-grid'},stat(t('healthyAccounts'),`${healthy} / ${accounts.length}`,t('accountHealth'),'accounts'),stat(t('messagesToday'),stats.messages_today ?? stats.today_messages,t('messagesToday'),'chat'),stat(t('errorRate'),stats.error_rate==null?'—':`${(stats.error_rate*100).toFixed(1)}%`,t('recent'),'logs'),stat(t('activeSessions'),stats.active_sessions ?? (s.sessions || []).length,t('sessions'),'sessions')),
        el('div',{class:'two-col overview-grid'},card(t('accountHealth'),health,null,button(t('accounts'),()=>ctx.navigate('accounts'),'quiet','arrow')),card(t('overviewStatus'),el('div',{class:'status-list'},el('div',{},el('span',{},t('protocol')),badge(protocol)),el('div',{},el('span',{},t('queue')),el('strong',{},stats.queue_size ?? stats.queued ?? '—')),el('div',{},el('span',{},t('lastProbe')),el('span',{},date(stats.protocol_checked_at || stats.protocol?.checked_at))),button(t('protocolProbe'),run('protocol.probe'),'secondary','refresh')),null)),
        card(t('quickActions'),el('div',{class:'quick-grid'},[['chat','chat'],['accountImport','accounts'],['platforms','platforms'],['commands','commands']].map(([label,page])=>button(t(label),()=>page==='accounts'?accountImport():ctx.navigate(page),'quick-action',page)))),
        (s.capabilities || []).length?card(t('capabilities'),el('div',{class:'capability-grid'},s.capabilities.map(c=>el('div',{class:'capability'},el('div',{class:'split'},el('strong',{},schemaText(c,'feature') || c.name),badge(c.status)),el('p',{class:'muted'},schemaText(c,'detail'))))),t('capabilityNote')):null,...pageResults());
    },
    accounts(){const s=ctx.state;const select=el('select',{'aria-label':t('status'),onChange:e=>{filter=e.target.value;ctx.renderContent();}},['all','available','ok','unknown','pending_vm','invalid','error','disabled'].map(v=>el('option',{value:v,text:t(v)})));select.value=filter;
      return el('div',{},listPage(s.accounts || [],[
        {label:t('account'),render:a=>el('div',{},el('strong',{},nameOf(a)),el('small',{class:'muted block'},a.note || a.id))},
        {label:t('status'),render:a=>{const state=a.enabled===false?'disabled':(a.status || 'unknown');const b=badge(state);const detailKey=a.enabled===false?'':['workspaceNotRunning','upstreamAuthFailed','probeNetworkFailed','probeProtocolFailed','upstreamRateLimited','accountProbeFailed'].includes(a.last_error)?a.last_error:(a.last_error==='account probe failed'?'probeAgainNeeded':'');b.title=detailKey?t(detailKey):'';return el('div',{},b,detailKey?el('small',{class:'muted block'},t(detailKey)):null);}},
        {label:t('credentials'),render:a=>el('div',{},el('code',{},'••••••••'),el('small',{class:'muted block'},a.configured===false?t('notConfigured'):t('configured')))},
        {label:t('node'),render:a=>text(s.nodes?.find(n=>n.id===a.node_id)?.name || (a.node_id || (language==='zh'?'默认连接':'Default connection')))},
        {label:t('quota'),render:a=>text(a.quota ?? a.quota_cache)},
        {label:t('lastProbe'),render:a=>el('div',{},date(a.last_probe_at || a.last_probe),a.cooldown_until?el('small',{class:'muted block'},t('cooldown')+': '+date(a.cooldown_until)):null)},
        {label:t('actions'),render:a=>rowActions(button(t('probe'),run('accounts.probe',{id:a.id}),'quiet'),button(t('edit'),()=>accountForm(a),'quiet'),button(a.enabled===false?t('enable'):t('disable'),run('accounts.update',{id:a.id,enabled:a.enabled===false}),'quiet'),button(t('delete'),()=>danger('accounts.delete',{id:a.id},nameOf(a)),'quiet danger-text'))}
      ],t('emptyAccounts'),button(t('accountImport'),()=>accountImport(),'primary','plus'),el('div',{class:'row-actions'},select,button(t('migration'),()=>accountImport(true),'secondary','upload'))),...settingsGroups(['accounts']));},
    platforms(){return el('div',{},...settingsGroups(['platforms']),...pageResults());},
    nodes(){return el('div',{},listPage(ctx.state.nodes || [],[
      {label:t('name'),render:n=>el('div',{},el('strong',{},n.name),el('small',{class:'muted block'},n.url_masked || n.masked_url || (n.url?maskProxy(n.url):'—')))},
      {label:t('status'),render:n=>badge(n.enabled===false?'disabled':n.status || (n.error?'error':'unknown'))},
      {label:t('latency'),render:n=>el('div',{},n.latency_ms==null?'—':`${n.latency_ms} ms`,n.error?el('small',{class:'danger-text block'},n.error):null)},
      {label:t('testedAt'),render:n=>date(n.tested_at)},
      {label:t('nodeBindings'),render:n=>(ctx.state.accounts || []).filter(a=>a.node_id===n.id).length},
      {label:t('actions'),render:n=>rowActions(button(t('test'),run('nodes.test',{id:n.id}),'quiet'),button(t('edit'),()=>nodeForm(n),'quiet'),button(t('delete'),()=>danger('nodes.delete',{id:n.id},n.name),'quiet danger-text'))}
    ],t('emptyNodes'),button(t('nodeAdd'),()=>nodeForm(),'primary','plus')),...settingsGroups(['nodes']));},
    stickers(){const items=ctx.state.stickers || [];const importButton=button(t('stickerImport'),()=>edit(t('stickerImport'),[f('url','stickerURL','url',{required:true})],{},'stickers.import'),'primary','plus');const content=el('div',{class:'sticker-grid'});
      const draw=()=>{const matched=items.filter(s=>JSON.stringify(redact(s)).toLowerCase().includes(search.toLowerCase()));content.replaceChildren(...matched.map(s=>{const url=safeURL(s.preview_url || (s.file_id?'/admin/api/files/'+encodeURIComponent(s.file_id):''));const image=url?el('img',{src:url,alt:s.description || s.emoji || t('stickers'),loading:'lazy',onError:event=>{event.target.replaceWith(el('span',{class:'muted'},t('noPreview')));}}):el('span',{class:'muted'},t('noPreview'));return el('article',{class:'sticker-card'},el('div',{class:'sticker-preview'},image),el('div',{class:'sticker-body'},el('div',{class:'split'},el('strong',{},s.emoji || s.pack || s.pack_name || s.id),el('small',{class:'muted'},s.id)),el('p',{},s.description || '—'),rowActions(button(t('edit'),()=>edit(t('description'),[f('description','description','textarea',{required:true})],s,'stickers.update',a=>({id:s.id,...a})),'quiet'),button(t('delete'),()=>danger('stickers.delete',{id:s.id},s.emoji || s.id),'quiet danger-text'))));}));if(!matched.length)content.append(empty(items.length?t('noMatches'):t('noData'),t('emptyStickers'),importButton,'stickers'));};draw();return el('div',{},toolbar(searchBox(v=>{search=v;draw();}),importButton),content,...pageResults(),...settingsGroups(['stickers']));},
    asr(){
      const testResult=el('div');const file=el('input',{type:'file',accept:'audio/*,.wav,.mp3,.ogg,.opus,.m4a,.flac',required:true});let pending=false;
      const testButton=button(t('transcribe'),async()=>{if(pending)return;if(!file.files.length){file.reportValidity();return;}pending=true;testButton.disabled=true;testResult.replaceChildren(resultView({pending:true}));try{const attachment=await readFile(file.files[0]);const data=await action('asr.test',{data_base64:attachment.data_base64,filename:attachment.filename});results.set('asr:test',{data});testResult.replaceChildren(asrResult(data));}catch(error){results.set('asr:test',{error:true,data:error.message});testResult.replaceChildren(resultView({error:true,data:error.message}));ctx.handleError(error);}finally{pending=false;testButton.disabled=false;}},'primary','asr');
      if(results.has('asr:test')){const r=results.get('asr:test');testResult.append(r.error?resultView(r):asrResult(r.data));}
      const dl=form([f('url','modelURL','url',{required:true}),f('sha256','sha256','string',{pattern:'^$|^[a-fA-F0-9]{64}$'})],{},args=>perform('asr.download',args),t('download'),{icon:'download'});
      return el('div',{},...settingsGroups(['asr']),el('div',{class:'two-col'},card(t('asrModel'),el('div',{},dl,note(t('asrManualHint')))),card(t('asrTest'),el('div',{},note(t('asrHint')),el('label',{class:'file-picker'},icon('upload'),t('audioFile'),file),testButton,testResult))),...[...results.entries()].filter(([k])=>k.startsWith('asr:')&&k!=='asr:test').map(([,r])=>resultView(r)));},
    keys(){return listPage(ctx.state.api_keys || [],[
      {label:t('name'),render:k=>el('div',{},el('strong',{},k.name),el('small',{class:'muted block'},date(k.created_at)))},
      {label:t('keys'),render:k=>el('code',{},k.masked || k.prefix ? `${k.masked || k.prefix}${k.masked?'':'••••••••'}`:'••••••••')},
      {label:t('status'),render:k=>badge(k.revoked || k.revoked_at?'revoked':'enabled')},
      {label:t('usage'),render:k=>text(k.usage ?? k.requests ?? k.requests_today ?? k.used_today)},
      {label:t('rpm'),key:'rpm'},{label:t('daily'),key:'daily'},
      {label:t('actions'),render:k=>rowActions(button(t('edit'),()=>edit(t('edit'),[f('rpm','rpm','integer',{min:0}),f('daily','daily','integer',{min:0})],k,'keys.update',a=>({id:k.id,...a})),'quiet'),!k.revoked&&!k.revoked_at?button(t('revoke'),()=>danger('keys.revoke',{id:k.id},k.name),'quiet danger-text'):null)}
    ],t('emptyKeys'),button(t('keyCreate'),keyCreate,'primary','plus'));},
    sessions(){return listPage(ctx.state.sessions || [],[
      {label:t('session'),render:s=>el('div',{},el('strong',{},s.title || s.name || s.id),el('small',{class:'muted block'},s.platform || 'web'))},
      {label:t('owner'),render:s=>s.owner || s.user_id || s.chat_id || s.target || '—'},
      {label:t('account'),render:s=>ctx.state.accounts?.find(a=>a.id===s.account_id)?.label || s.account_id || '—'},
      {label:t('lastActive'),render:s=>date(s.last_active_at || s.last_active || s.updated_at)},
      {label:t('actions'),render:s=>rowActions(button(t('continueChat'),()=>ctx.openChat(s),'quiet'),button(t('export'),()=>exportAction('sessions.export',{id:s.id},'muse-session.json').catch(()=>{}),'quiet'),button(t('reset'),()=>danger('sessions.reset',{id:s.id},s.title || s.id),'quiet danger-text'))}
    ],t('emptySessions'),button(t('newChat'),()=>ctx.navigate('chat'),'primary','plus'));},
    logs(){const entries=ctx.state.logs || [];const list=el('div',{class:'log-list',role:'log','aria-label':t('logs')});const draw=()=>{const rows=entries.filter(l=>(filter==='all'||l.level===filter)&&JSON.stringify(l).toLowerCase().includes(search.toLowerCase()));list.replaceChildren(...rows.map(l=>el('div',{class:'log-row'},el('time',{},date(l.time || l.timestamp || l.created_at)),badge(l.level || 'info'),el('pre',{},l.message || l.msg || pretty(l)))));if(!rows.length)list.append(empty(t('noData'),t('emptyLogs'),button(t('refresh'),()=>ctx.refresh()),'logs'));if(ctx.autoRefresh)list.scrollTop=list.scrollHeight;};draw();const level=el('select',{'aria-label':t('level'),onChange:e=>{filter=e.target.value;draw();}},['all','debug','info','warn','error'].map(v=>el('option',{value:v,text:t(v)})));level.value=filter;return el('div',{},toolbar(searchBox(v=>{search=v;draw();}),level,el('label',{class:'check-row'},el('input',{type:'checkbox',checked:ctx.autoRefresh,onChange:e=>{ctx.autoRefresh=e.target.checked;}}),t('autoRefresh'))),list);},
    settings(){const available=[...new Set((ctx.schema.settings || []).map(f=>f.group))];const select=el('select',{'aria-label':t('section'),onChange:e=>{selectedGroup=e.target.value;ctx.renderContent();}},el('option',{value:'all'},t('allSettings')),available.map(g=>el('option',{value:g},groupLabel(g))));select.value=selectedGroup;return el('div',{},toolbar(select,button(t('commands'),()=>ctx.navigate('commands'),'secondary','commands')),...settingsGroups(selectedGroup==='all'?null:[selectedGroup]),backupPanel(),...pageResults());},
    personas(){const add=()=>edit(t('personaAdd'),[f('name','name','string',{required:true}),f('prompt','prompt','textarea',{required:true,rows:8})],{},'personas.save');return el('div',{},note(t('personaWarning'),'warning'),listPage(ctx.state.personas || [],[{label:t('name'),key:'name'},{label:t('prompt'),render:p=>el('div',{class:'clamp-text'},p.prompt)},{label:t('actions'),render:p=>rowActions(button(t('edit'),()=>edit(t('edit'),[f('name','name','string',{required:true}),f('prompt','prompt','textarea',{required:true,rows:8})],p,'personas.save',a=>({id:p.id,...a})),'quiet'),button(t('delete'),()=>danger('personas.delete',{id:p.id},p.name),'quiet danger-text'))}],t('emptyPersonas'),button(t('personaAdd'),add,'primary','plus')),...settingsGroups(['personas']));},
    schedules(){return listPage(ctx.state.schedules || [],[
      {label:t('name'),render:s=>el('div',{},el('strong',{},s.name),el('small',{class:'muted block'},s.platform+' · '+s.target))},
      {label:t('status'),render:s=>badge(s.enabled?'enabled':'disabled')},
      {label:t('cron'),render:s=>el('div',{},el('code',{},s.cron),el('small',{class:'muted block'},s.timezone))},
      {label:t('nextRun'),render:s=>date(s.next_run_at || s.next_run)},
      {label:t('lastRun'),render:s=>el('div',{},date(s.last_run_at || s.last_run),s.error?el('small',{class:'danger-text'},s.error):null)},
      {label:t('actions'),render:s=>rowActions(button(t('edit'),()=>scheduleForm(s),'quiet'),button(t('delete'),()=>danger('schedules.delete',{id:s.id},s.name),'quiet danger-text'))}
    ],t('emptySchedules'),button(t('scheduleAdd'),()=>scheduleForm(),'primary','plus'));},
    commands(){const operations=ctx.schema.operations || [];const grid=el('div',{class:'command-grid'});
      const draw=()=>{const q=search.toLowerCase();grid.replaceChildren(...operations.filter(o=>[o.name,schemaText(o,'title'),o.group].join(' ').toLowerCase().includes(q)).map(o=>el('article',{class:'command-card'},el('div',{class:'split'},icon(o.group || 'commands'),o.dangerous?badge('warning'):null),el('h3',{},schemaText(o,'title') || o.name),el('code',{class:'muted'},o.name),el('p',{class:'muted'},schemaText({...o,key:'',name:''},'description')),button(t('run'),()=>openOperation(o),'secondary','arrow'))));if(!grid.childElementCount)grid.append(empty(t('noData'),operations.length?t('noMatches'):t('noOperations'),button(t('refresh'),()=>ctx.refresh()),'commands'));};draw();return el('div',{},toolbar(searchBox(v=>{search=v;draw();},t('operationSearch'))),grid,...pageResults());}
  };
  function openOperation(operation){
    const fields=(operation.fields || []).map(field=>({...field,key:field.key || field.name}));
    const inputs=fields.length?fields:[f('_args','rawJSON','object',{default:{},required:true})];
    dialog(schemaText(operation,'title') || operation.name,close=>form(inputs,{},async args=>{
      if(operation.dangerous&&!await confirmAction(schemaText(operation,'title') || operation.name))return;
      const actual=fields.length?args:args._args;
      if(operation.name==='keys.create'){
        const data=await action(operation.name,actual);close();await ctx.refresh(false);
        let key=data?.key || '';dialog(t('keyOnce'),done=>el('div',{},note(t('keyOnceHint'),'warning'),el('textarea',{readOnly:true,value:key,rows:3}),button(t('copy'),()=>navigator.clipboard.writeText(key).catch(()=>ctx.notify(t('failed'),true)),'primary','copy'),button(t('keySaved'),done)),{onClose:()=>{key='';if(data)data.key='';}});
      }else if(operation.name==='auth.totp.setup'){
        const data=await action(operation.name,actual);close();let secret=data?.secret || '';dialog(schemaText(operation,'title'),done=>el('div',{},el('p',{},language==='zh'?'将此密钥添加到验证器，然后在操作中心运行启用验证操作。':'Add this secret to your authenticator, then run the enable verification operation.'),el('textarea',{readOnly:true,value:secret,rows:2}),button(t('copy'),()=>navigator.clipboard.writeText(secret).catch(()=>ctx.notify(t('failed'),true)),'primary','copy'),button(t('close'),done)),{onClose:()=>{secret='';if(data)data.secret='';}});
      }else{await perform(operation.name,actual,{download:['backup.export','sessions.export'].includes(operation.name)});close();}
    },t('run')),{wide:inputs.length>5});
  }
  return {render(page){const view=views[page]?.() || views.overview();view.classList.add('page-stack');return view;},resetFilters(){search='';filter='all';},clear(){results.clear();search='';filter='all';},perform};
}
function maskProxy(value){try{const u=new URL(value);if(u.username)u.username='••••';if(u.password)u.password='••••';return u.toString();}catch{return '••••••••';}}
function asrResult(data){return el('div',{class:'asr-result'},el('h3',{},t('transcription')),el('pre',{class:'transcript'},data?.text || '—'),el('div',{class:'mini-stats'},stat(t('elapsed'),data?.duration_ms==null?'—':`${data.duration_ms} ms`),stat(t('audioDuration'),data?.audio_duration_ms==null?'—':`${data.audio_duration_ms} ms`),stat(t('rtf'),data?.rtf==null?'—':Number(data.rtf).toFixed(3))));}
