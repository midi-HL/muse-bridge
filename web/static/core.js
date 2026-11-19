import {t} from './i18n.js';
export const API = '/admin/api';
let csrf = '';
export function setCSRF(value) { csrf = value || ''; }
export class APIError extends Error { constructor(message, status = 0, fields = {}) { super(message); this.status = status; this.fields = fields; } }
export async function request(path, options = {}) {
  const {body, signal, method = body === undefined ? 'GET' : 'POST'} = options;
  let response;
  try { response = await fetch(API + path, {method, credentials:'same-origin', cache:'no-store', signal, headers:{Accept:'application/json', ...(body === undefined ? {} : {'Content-Type':'application/json'}), ...(method === 'GET' ? {} : {'X-CSRF-Token':csrf})}, ...(body === undefined ? {} : {body:JSON.stringify(body)})}); }
  catch (error) { if (error.name === 'AbortError') throw error; throw new APIError(t('networkError')); }
  let data;
  try { data = await response.json(); } catch { throw new APIError(`${t('failed')} (${response.status})`, response.status); }
  if (!response.ok || data.error || data.ok === false) throw new APIError(data.error?.message || `${t('failed')} (${response.status})`, response.status, data.error?.fields || {});
  if (data.csrf_token) setCSRF(data.csrf_token);
  return data;
}
export async function action(name, args = {}) { const result = await request('/action', {body:{action:name,args}}); return result.data; }
export async function stopChat(sessionId) { return request('/chat/stop', {body:{session_id:sessionId}}); }
export async function chatStream(body, onEvent, signal) {
  const response = await fetch(API + '/chat', {method:'POST',credentials:'same-origin',cache:'no-store',signal,headers:{'Content-Type':'application/json','X-CSRF-Token':csrf,Accept:'text/event-stream'},body:JSON.stringify(body)});
  if (!response.ok) { let result; try { result=await response.json(); } catch {} if(response.status===401)throw new APIError(t('sessionExpired'),401); throw new APIError(result?.error?.message || `${t('failed')} (${response.status})`,response.status,result?.error?.fields || {}); }
  if (!response.body) throw new APIError(t('streamInterrupted'));
  const reader=response.body.getReader(),decoder=new TextDecoder(),parser=new SSEDecoder(onEvent);
  try { while(true){const {done,value}=await reader.read();if(done)break;parser.push(decoder.decode(value,{stream:true}));} parser.push(decoder.decode(),true); }
  catch(error){if(error.name==='AbortError')throw error; if(error instanceof SyntaxError)throw new APIError(t('streamInterrupted')); throw error;}
  finally {reader.releaseLock();}
}
export function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key,value] of Object.entries(props)) {
    if (value === undefined || value === null || value === false) continue;
    if (key === 'class') node.className = value;
    else if (key === 'text') node.textContent = value;
    else if (key === 'dataset') Object.assign(node.dataset, value);
    else if (key.startsWith('on') && typeof value === 'function') node.addEventListener(key.slice(2).toLowerCase(), value);
    else if (key in node && !key.startsWith('aria')) node[key] = value;
    else node.setAttribute(key, value === true ? '' : String(value));
  }
  for (const child of children.flat(Infinity)) if (child !== undefined && child !== null && child !== false) node.append(child instanceof Node ? child : document.createTextNode(String(child)));
  return node;
}
export const paths = {
  overview:'M3 3h7v7H3z M14 3h7v7h-7z M3 14h7v7H3z M14 14h7v7h-7z',chat:'M21 11.5a8.5 8.5 0 0 1-8.5 8.5H5l-4 3V11.5a10 10 0 0 1 20 0Z M7 9h8 M7 13h5',accounts:'M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2 M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8 M17 4a4 4 0 0 1 0 7 M22 21v-2a4 4 0 0 0-3-3.87',platforms:'M8 12l13-9-4 18-6-6-4 3 1-6-5-3 18-6',nodes:'M12 8V3 M5 21v-5h14v5 M12 12v9 M8 8h8v4H8z',stickers:'M21 13V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h8l8-8Z M13 21v-6a2 2 0 0 1 2-2h6 M7 8h.01 M15 8h.01 M7 12c2 2 4 2 6 0',asr:'M9 5a3 3 0 0 1 6 0v7a3 3 0 0 1-6 0V5Z M5 10v2a7 7 0 0 0 14 0v-2 M12 19v3 M8 22h8',keys:'M15 7a5 5 0 1 1-2 4l-9 9H1v-3l9-9a5 5 0 0 1 5-6 M16 6h.01',sessions:'M3 3h18v14H7l-4 4V3Z M7 7h10 M7 11h6',logs:'M5 3h14v18H5z M8 7h8 M8 11h8 M8 15h5',settings:'M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8 M9 3h6l1 3 3 1 2 5-2 5-3 1-1 3H9l-1-3-3-1-2-5 2-5 3-1z',personas:'M12 3a4 4 0 1 0 0 8 4 4 0 0 0 0-8 M5 21v-3a7 7 0 0 1 14 0v3 M17 4l3-1 1 3',schedules:'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18 M12 7v5l4 2',commands:'M4 6l5 6-5 6 M12 18h8',plus:'M12 5v14 M5 12h14',search:'M10.5 3a7.5 7.5 0 1 0 0 15 7.5 7.5 0 0 0 0-15 M16 16l5 5',refresh:'M20 8a8 8 0 0 0-14-3L3 8 M3 3v5h5 M4 16a8 8 0 0 0 14 3l3-3 M21 21v-5h-5',arrow:'M5 12h14 M13 6l6 6-6 6',send:'M22 2 9 15 M22 2l-7 20-6-7-7-6 20-7Z',stop:'M6 6h12v12H6z',close:'M6 6l12 12 M18 6 6 18',check:'M4 12l5 5L20 6',alert:'M12 3 2 21h20L12 3Z M12 9v4 M12 17h.01',file:'M14 2H5v20h14V7l-5-5Z M14 2v6h5',download:'M12 3v12 M7 10l5 5 5-5 M4 17v4h16v-4',moon:'M20 15A9 9 0 0 1 9 4a9 9 0 1 0 11 11Z',sun:'M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8 M12 1v3 M12 20v3 M1 12h3 M20 12h3 M4 4l2 2 M18 18l2 2 M4 20l2-2 M18 6l2-2',menu:'M4 6h16 M4 12h16 M4 18h16',logout:'M9 3H3v18h6 M8 12h13 M16 7l5 5-5 5',chevron:'M9 5l7 7-7 7',upload:'M12 16V3 M7 8l5-5 5 5 M4 17v4h16v-4',attach:'m21 11-8 8a6 6 0 0 1-8-8l9-9a4 4 0 0 1 6 6L10 18a2 2 0 0 1-3-3l9-9',copy:'M9 9h12v12H9z M15 9V3H3v12h6',shield:'M12 2 3 6v6c0 5 9 10 9 10s9-5 9-10V6l-9-4Z M8 12l3 3 5-6'
};
Object.assign(paths,{
  chat:'M21 11.5a8.5 8.5 0 0 1-8.5 8.5H4l-2 2v-9.5A8.5 8.5 0 0 1 10.5 4H12 M7 10h10 M7 14h6',
  platforms:'m21 3-7 18-4-7-7-4L21 3Zm0 0L10 14',
  nodes:'M12 8V4 M6 16v-4h12v4 M12 12V8 M4 16h4v4H4z M10 2h4v6h-4z M16 16h4v4h-4z',
  keys:'M15.5 3a5.5 5.5 0 1 1-3.7 9.6L5 19.4H2V16l7.9-7.9A5.5 5.5 0 0 1 15.5 3Z M16 7h.01',
  personas:'M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2 M13 7a4 4 0 1 1-8 0 4 4 0 0 1 8 0 M20 8v6 M17 11h6',
  settings:'m9.5 3-.6 2.4-2.1 1.2-2.4-.7L2 10.1 3.8 12l-.1 2.4L2 16.3l2.4 4.1 2.5-.7 2 1.2.6 2.4h4.8l.6-2.4 2-1.2 2.5.7 2.4-4.1-1.8-1.9V12l1.8-1.9-2.4-4.2-2.4.7-2.1-1.2L14.3 3h-4.8Z M15.5 13a3.5 3.5 0 1 1-7 0 3.5 3.5 0 0 1 7 0',
  send:'m21 3-6 18-4-8-8-4 18-6ZM11 13 21 3',
  logout:'M9 4H4v16h5 M10 12h11 M17 8l4 4-4 4'
});
export function icon(name, className = '') { const svg = document.createElementNS('http://www.w3.org/2000/svg','svg'); for (const [key,value] of Object.entries({viewBox:'0 0 24 24',fill:'none',stroke:'currentColor','stroke-width':'1.7','stroke-linecap':'round','stroke-linejoin':'round','aria-hidden':'true',class:`icon ${className}`})) svg.setAttribute(key,value); const p = document.createElementNS(svg.namespaceURI,'path'); p.setAttribute('d', paths[name] || paths.commands); svg.append(p); return svg; }
export function button(label, onClick, kind = 'secondary', iconName) { const compact=kind.split(' ').includes('icon-button');return el('button',{type:'button',class:`button ${kind}`,onClick,...(compact?{'aria-label':label,title:label}:{})}, iconName ? icon(iconName) : null, compact?null:label); }
export function badge(value) { const state = String(value ?? 'unknown'); const color = /^(ok|healthy|live|enabled|success|passed|supported|available|ready)$/.test(state) ? 'success' : /^(error|invalid|failed|unavailable)$/.test(state) ? 'danger' : /^(pending|pending_vm|warning|cooldown|unknown)$/.test(state) ? 'warning' : 'neutral'; return el('span',{class:`badge ${color}`},el('span',{class:'status-dot'}),t(state)); }
export function safeURL(value) { try { const u = new URL(value, location.origin); return u.origin === location.origin && /^https?:$/.test(u.protocol) ? u.href : null; } catch { return null; } }
const secretPattern = /(?:password|passphrase|cookie|token|secret|authorization|api[_-]?key|access[_-]?key|hatch_gw|hatch_sess|hatch_vml|data_base64|blob|^key$)/i;
export function redact(value, key = '') { if (secretPattern.test(key)) return typeof value === 'object' && value?.configured !== undefined ? {configured:!!value.configured,masked:value.configured ? (typeof value.masked === 'string' && value.masked ? value.masked : '••••••••') : ''} : value ? '••••••••' : ''; if (Array.isArray(value)) return value.map(v => redact(v)); if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value).map(([k,v])=>[k,redact(v,k)])); return value; }
export function redactText(value) { return String(value).replace(/(\b(?:hatch_gw|hatch_sess|hatch_vml|cookie|token|api[_-]?key|password|secret|authorization)\b\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;]+)/gi,'$1••••••••').replace(/(\bBearer\s+)[A-Za-z0-9._~+\/-]+=*/gi,'$1••••••••'); }
export function pretty(value) { if (value === undefined || value === null) return '—'; return typeof value === 'string' ? redactText(value) : JSON.stringify(redact(value), null, 2); }
export function download(content, filename = 'muse-export.json', mime = 'application/json') { const blob = new Blob([typeof content === 'string' ? content : JSON.stringify(content,null,2)], {type:mime}); const url = URL.createObjectURL(blob); const a = el('a',{href:url,download:filename}); a.click(); setTimeout(()=>URL.revokeObjectURL(url),1000); }
export async function readFile(file, maxBytes = 20 * 1024 * 1024) { if (file.size > maxBytes) throw new Error(t('fileTooLarge')); const buffer = new Uint8Array(await file.arrayBuffer()); let binary = ''; for (let i=0;i<buffer.length;i+=8192) binary += String.fromCharCode(...buffer.subarray(i,i+8192)); return {filename:file.name,mime:file.type || 'application/octet-stream',data_base64:btoa(binary)}; }
export function coerceField(field, value) {
  if (field.type === 'boolean' || field.type === 'bool') return !!value;
  if (value === '' || value == null) { if (field.required) throw new Error(t('required')); return field.type === 'number' || field.type === 'integer' || field.type === 'int' ? undefined : ''; }
  if (['number','integer','int','float'].includes(field.type)) { const n = Number(value); if (!Number.isFinite(n) || (['integer','int'].includes(field.type) && !Number.isInteger(n))) throw new Error(t('invalidNumber')); if (field.min !== undefined && n < field.min) throw new Error(`${t('minValue')} ${field.min}`); if (field.max !== undefined && n > field.max) throw new Error(`${t('maxValue')} ${field.max}`); return n; }
  if (['object','json','array','string[]'].includes(field.type)) { try { const parsed = JSON.parse(value); if (['array','string[]'].includes(field.type) && !Array.isArray(parsed)) throw new Error(); if (field.type === 'object' && (!parsed || Array.isArray(parsed) || typeof parsed !== 'object')) throw new Error(); return parsed; } catch { throw new Error(t('invalidJSON')); } }
  if (field.type === 'url') { try { const url = new URL(value); if (!['http:','https:','socks5:','socks5h:'].includes(url.protocol)) throw new Error(); } catch { throw new Error(t('invalidURL')); } }
  if (field.pattern && !new RegExp(field.pattern).test(value)) throw new Error(t('formatError'));
  return value;
}
// Streaming decoder accepts arbitrary UTF-8/chunk boundaries, CRLF and multiline SSE.
export class SSEDecoder {
  constructor(onEvent) { this.buffer=''; this.data=[]; this.onEvent=onEvent; }
  push(text, final = false) {
    this.buffer += text;
    let index;
    while ((index=this.buffer.indexOf('\n')) >= 0) { this.line(this.buffer.slice(0,index).replace(/\r$/, '')); this.buffer=this.buffer.slice(index+1); }
    if (final) { if (this.buffer) this.line(this.buffer.replace(/\r$/, '')); this.buffer=''; this.line(''); }
  }
  line(line) { if (!line) { if (this.data.length) { const text=this.data.join('\n'); this.data=[]; if (text === '[DONE]') this.onEvent({type:'done'}); else this.onEvent(JSON.parse(text)); } } else if (line.startsWith('data:')) this.data.push(line.slice(5).replace(/^ /,'')); }
}

