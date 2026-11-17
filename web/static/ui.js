import {el,icon,button,coerceField,pretty,badge} from './core.js';
import {t,schemaText} from './i18n.js';
let idCounter=0;
export function empty(title, description, actionButton, iconName='overview') { return el('div',{class:'empty'},el('span',{class:'empty-icon'},icon(iconName)),el('h3',{},title),el('p',{},description),actionButton); }
export function card(title, body, subtitle, actions) { return el('section',{class:'card'},title ? el('div',{class:'card-head'},el('div',{},el('h2',{},title),subtitle?el('p',{},subtitle):null),actions) : null,el('div',{class:'card-body'},body)); }
export function note(text, kind='info') { return el('div',{class:`notice ${kind}`,role:kind==='danger'?'alert':'note'},icon(kind==='danger'?'alert':'shield'),el('span',{},text)); }
export function resultView(result) { if (!result) return null; return el('div',{class:`result ${result.error?'result-error':''}`,'aria-live':'polite'},el('div',{class:'result-heading'},icon(result.pending?'refresh':result.error?'alert':'check'),el('strong',{},result.pending?t('running'):result.error?t('failed'):t('success'))),result.data !== undefined ? el('pre',{},pretty(result.data)) : null); }
export function fieldControl(field,value) {
  const key=field.key || field.name, id=`field-${++idCounter}`;
  const label=schemaText(field), description=schemaText({...field,key:'',name:''},'description');
  const secret=field.secret || field.type==='password';
  const error=el('span',{class:'field-error',id:`${id}-error`});
  let input;
  const current=value ?? field.default ?? '';
  if (field.type==='boolean' || field.type==='bool') input=el('input',{type:'checkbox',checked:!!current,class:'switch',id,name:key});
  else if (field.options?.length) { input=el('select',{id,name:key},field.options.map(o=>{const val=typeof o==='object'?o.value:o; const label=typeof o==='object'?schemaText(o):t(String(o)); return el('option',{value:val,text:label});})); input.value=String(current); }
  else if (['text','textarea','object','json','array','string[]'].includes(field.type) || field.multiline || String(key).endsWith('prompt')) input=el('textarea',{id,name:key,rows:field.rows || 4,value:typeof current==='object'?JSON.stringify(current,null,2):String(current),spellcheck:!secret});
  else input=el('input',{id,name:key,type:secret?'password':['number','integer','int','float'].includes(field.type)?'number':field.type==='url'?'url':'text',value:secret?'':typeof current==='object'?'':String(current),min:field.min,max:field.max,step:['integer','int'].includes(field.type)?1:'any',autocomplete:secret?'new-password':'off',placeholder:secret?t('keepSecret'):field.placeholder || ''});
  input.required=!!field.required;
  input.setAttribute('aria-describedby',`${id}-error${description?` ${id}-hint`:''}`);
  const controls=[input];
  let clear;
  if (secret) {
    controls.push(el('div',{class:'secret-state'},badge(current?.configured?'enabled':'disabled'),el('span',{},current?.configured?'••••••••':t('notConfigured'))));
    if (current?.configured) { clear=el('input',{type:'checkbox'}); controls.push(el('label',{class:'check-row'},clear,t('secretClear'))); }
    controls.push(el('small',{class:'muted'},t('secretHint')));
  }
  const root=el('div',{class:`field ${input.type==='checkbox'?'boolean-field':''}`},el('label',{htmlFor:id},label,field.required?el('span',{class:'required'},' *'):null),...controls,description?el('small',{id:`${id}-hint`,class:'muted'},description):null,error);
  return {root,input,key,field,read(){ error.textContent=''; input.removeAttribute('aria-invalid'); if (secret && !input.value && !clear?.checked) return undefined; try { return coerceField(field,input.type==='checkbox'?input.checked:input.value); } catch(e) { error.textContent=e.message; input.setAttribute('aria-invalid','true'); throw e; } },setError(message){error.textContent=String(message);input.setAttribute('aria-invalid','true');},willClear(){return !!clear?.checked;}};
}
export function form(fields, values, onSubmit, submitLabel=t('save'), options={}) {
  const controls=fields.map(f=>fieldControl(f,values?.[f.key || f.name]));
  const error=el('div',{class:'form-error',role:'alert'}), submit=button(submitLabel,null,'primary',options.icon || 'check');submit.type='submit';
  const progress=el('span',{class:'muted','aria-live':'polite'});
  let pending=false;
  const root=el('form',{class:`form ${options.class || ''}`,onSubmit:async event=>{
    event.preventDefault();if(pending)return;
    const args={};let invalid=false;error.replaceChildren();
    for(const control of controls) try {const v=control.read();if(v!==undefined)args[control.key]=v;} catch {invalid=true;}
    if(invalid){controls.find(c=>c.input.getAttribute('aria-invalid'))?.input.focus();return;}
    if(controls.some(c=>c.willClear()) && !await confirmAction(t('secretClear'),t('secretClearConfirm')))return;
    pending=true;submit.disabled=true;progress.textContent=t('running');
    try { await onSubmit(args,root); progress.textContent=t('success'); }
    catch(e){error.textContent=e.message || t('failed');for(const [key,message] of Object.entries(e.fields || {}))controls.find(c=>c.key===key)?.setError(message);progress.textContent='';}
    finally {pending=false;submit.disabled=false;}
  }},el('div',{class:options.columns?'form-grid':''},controls.map(c=>c.root)),error,el('div',{class:'form-actions'},submit,progress));
  root.controls=controls;
  return root;
}
export function dialog(title, content, options={}) {
  const previous=document.activeElement;
  const element=el('dialog',{class:`dialog ${options.wide?'wide':''}`,'aria-labelledby':`dialog-title-${++idCounter}`});
  const heading=el('h2',{id:`dialog-title-${idCounter}`},title);
  let closeButton;
  const close=()=>{element.close();element.remove();options.onClose?.();if(previous?.isConnected)previous.focus();};
  closeButton=button(t('close'),close,'icon-button','close'); closeButton.setAttribute('aria-label',t('close'));
  element.append(el('div',{class:'dialog-head'},heading,closeButton),el('div',{class:'dialog-body'},typeof content==='function'?content(close):content));
  element.addEventListener('cancel',event=>{event.preventDefault();close();});
  element.addEventListener('click',event=>{if(event.target===element){const r=element.getBoundingClientRect();if(event.clientX<r.left||event.clientX>r.right||event.clientY<r.top||event.clientY>r.bottom)close();}});
  document.body.append(element);element.showModal();return {element,close};
}
export function confirmAction(title, description) { return new Promise(resolve=>{ let settled=false; const done=value=>{if(settled)return;settled=true;resolve(value);};dialog(t('dangerTitle'),close=>el('div',{},note(description || t('dangerBody'),'warning'),el('p',{class:'confirm-target'},title),el('div',{class:'form-actions'},button(t('cancel'),close),button(t('confirm'),()=>{done(true);close();},'danger'))),{onClose:()=>done(false)});}); }
export function table(columns,rows) { return el('div',{class:'table-wrap'},el('table',{},el('thead',{},el('tr',{},columns.map(c=>el('th',{scope:'col'},c.label)))),el('tbody',{},rows.map(row=>el('tr',{},columns.map(c=>el('td',{},c.render?c.render(row):row[c.key] ?? '—'))))))); }
export function searchBox(onInput, placeholder=t('searchPlaceholder'), value='') { const input=el('input',{type:'search',value,placeholder,'aria-label':t('search'),onInput:event=>onInput(event.target.value)}); return el('div',{class:'search'},icon('search'),input); }
export function toolbar(...children) { return el('div',{class:'toolbar'},...children); }
export function rowActions(...children) { return el('div',{class:'row-actions'},...children); }
export function stat(label,value,sub,iconName) { return el('div',{class:'stat-card'},el('div',{class:'stat-top'},el('span',{},label),icon(iconName || 'overview')),el('strong',{class:'stat-value'},value ?? '—'),el('small',{class:'muted'},sub || '')); }
