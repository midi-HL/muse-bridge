import {request,setCSRF,el,icon,button,APIError} from './core.js';
import {t,setLanguage,language} from './i18n.js';
import {createPages} from './pages.js';
import {createChat} from './chat.js';

const navGroups=[
  {label:'workspace',items:[['overview','overview'],['chat','chat'],['sessions','sessions']]},
  {label:'manage',items:[['accounts','accounts'],['platforms','platforms'],['nodes','nodes'],['stickers','stickers'],['asr','asr'],['keys','keys']]},
  {label:'system',items:[['logs','logs'],['settings','settings'],['personas','personas'],['schedules','schedules'],['commands','commands']]}
];
let appState={auth:null,schema:{settings:[],operations:[]},state:{settings:{},accounts:[],nodes:[],personas:[],stickers:[],api_keys:[],sessions:[],schedules:[],stats:{},logs:[],capabilities:[]}};
let root, shell, content, title, subtitle, toastHost, nav, mobileMenu, ctx, pages, chat;
let refreshTimer, currentRequest=0;
const prefersDark=()=>{try{return localStorage.getItem('muse.theme')==='dark' || (!localStorage.getItem('muse.theme')&&matchMedia('(prefers-color-scheme: dark)').matches);}catch{return false;}};
function setTheme(theme){const value=theme==='system'?(matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light'):theme;document.documentElement.dataset.theme=value;document.querySelector('meta[name="theme-color"]')?.setAttribute('content',value==='dark'?'#111318':'#f6f7fb');try{localStorage.setItem('muse.theme',theme);}catch{};}
function notify(message,danger=false){if(!toastHost)return;const toast=el('div',{class:`toast ${danger?'danger':''}`,role:danger?'alert':'status'},icon(danger?'alert':'check'),el('span',{},message));toastHost.append(toast);setTimeout(()=>toast.remove(),5000);}
function handleError(error){if(error?.status===401){showAuth();return;}notify(error?.message || t('failed'),true);}
async function refresh(keepPage=true){const ticket=++currentRequest;try {const [schema,state]=await Promise.all([request('/schema'),request('/state')]);if(ticket!==currentRequest)return;appState.schema=schema;appState.state=state;ctx.schema=schema;ctx.state=state;renderContent();}catch(error){if(ticket===currentRequest)handleError(error);throw error;}}
function navItems(){return navGroups.flatMap(group=>group.items);}
function titleFor(page){return t(page);}
function renderNavigation(){nav.replaceChildren(...navGroups.map(group=>el('div',{class:'nav-group'},el('div',{class:'nav-group-label'},t(group.label)),...group.items.map(([page,iconName])=>el('button',{type:'button',class:`nav-item ${ctx.page===page?'active':''}`,onClick:()=>navigate(page)},icon(iconName),el('span',{},t(page)),ctx.page===page?el('span',{class:'nav-current','aria-hidden':'true'}):null)))));}
function navigate(page){if(!navItems().some(([key])=>key===page))page='overview';if(ctx.page===page){mobileMenu?.classList.remove('open');return;}if(chat && ctx.page==='chat' && chat.isSending?.()){notify(t('cancelGeneration'),true);return;}ctx.page=page;pages.resetFilters();renderShell();mobileMenu?.classList.remove('open');}
function renderShell(){title.textContent=titleFor(ctx.page);subtitle.textContent=t(`${ctx.page}Desc`);renderNavigation();renderContent();}
function renderContent(){if(!content)return;content.replaceChildren(ctx.page==='chat'?chat.render():pages.render(ctx.page));}
function refreshPagePeriodically(){ if(ctx.page==='logs'||ctx.page==='overview'||ctx.page==='sessions') refresh().catch(()=>{}); chat?.refreshHistory?.(); }
function openChat(session){chat?.chooseSession(session);}
function makeShell(){
  root.replaceChildren();
  shell=el('div',{class:'app-shell'});
  const sidebar=el('aside',{class:'sidebar',id:'sidebar'},el('div',{class:'sidebar-brand'},el('span',{class:'brand-mark'},'m'),el('div',{},el('strong',{},t('brand')),el('small',{},'Muse Bridge'))),el('div',{class:'sidebar-status'},icon('shield'),el('span',{},t('protected'))),nav=el('nav',{class:'navigation','aria-label':t('workspace')}),el('div',{class:'sidebar-foot'},el('span',{},t('localFirst')),button(t('logout'),logout,'quiet','logout')));
  mobileMenu=el('div',{class:'mobile-overlay',onClick:()=>mobileMenu.classList.remove('open')});
  const main=el('main',{class:'main',id:'main'},el('header',{class:'topbar'},el('div',{class:'topbar-left'},button(t('menu'),()=>mobileMenu.classList.toggle('open'),'icon-button','menu'),el('div',{},title=el('h1',{}),subtitle=el('p',{}))),el('div',{class:'topbar-actions'},button(language==='zh'?'EN':'中文',()=>{setLanguage(language==='zh'?'en':'zh');makeShell();},'quiet'),button(t('theme'),()=>{const dark=document.documentElement.dataset.theme==='dark';setTheme(dark?'light':'dark');},'icon-button',prefersDark()?'sun':'moon'),button(t('refresh'),()=>refresh().catch(()=>{}),'icon-button','refresh'))),content=el('section',{class:'content'}));
  toastHost=el('div',{class:'toast-host','aria-live':'polite'});
  shell.append(mobileMenu,sidebar,main,toastHost);root.append(shell);renderShell();
}
async function logout(){try{await request('/auth/logout',{body:{}});}catch{}showAuth();}
function authForm(mode){
  const setup=mode==='setup', box=el('div',{class:'auth-card'},el('div',{class:'auth-logo'},el('span',{class:'brand-mark'},'m')),el('p',{class:'eyebrow'},t('brand')),el('h1',{},setup?t('setupTitle'):t('authTitle')),el('p',{class:'auth-subtitle'},setup?t('setupSubtitle'):t('authSubtitle')));
  const password=el('input',{type:'password',autocomplete:setup?'new-password':'current-password',required:true,minLength:setup?12:1});
  const setupToken=setup&&appState.auth?.setup_token_required?el('input',{type:'password',autocomplete:'off',required:true}):null;
  const repeat=setup?el('input',{type:'password',autocomplete:'new-password',required:true,minLength:12}):null;
  const otp=el('input',{type:'text',inputMode:'numeric',autocomplete:'one-time-code',placeholder:t('otp'),maxLength:8});
  const error=el('div',{class:'form-error',role:'alert'});
  const submit=button(setup?t('setup'):t('login'),null,'primary','arrow');submit.type='submit';
  const form=el('form',{class:'auth-form',onSubmit:async event=>{event.preventDefault();error.textContent='';if(setup&&(password.value.length<12||password.value!==repeat.value)){error.textContent=password.value.length<12?t('passwordMin'):t('mismatch');return;}submit.disabled=true;try{const result=await request(`/auth/${setup?'setup':'login'}`,{body:{password:password.value,...(setup?{password_repeat:repeat.value,...(setupToken?{setup_token:setupToken.value}:{})}:{current_password:password.value}),...(otp.value?{otp:otp.value}:{})}});if(result.csrf_token)setCSRF(result.csrf_token);appState.auth=result;await loadApp();}catch(e){error.textContent=e.message || t('failed');}finally{submit.disabled=false;}}},el('label',{},t('password'),password),setup?el('label',{},t('passwordRepeat'),repeat):null,setupToken?el('label',{},'Setup token',setupToken):null,el('label',{},t('otp'),otp),error,submit);
  box.append(form,el('div',{class:'auth-foot'},el('span',{},t('localFirst')),button(language==='zh'?'EN':'中文',()=>{setLanguage(language==='zh'?'en':'zh');showAuth();},'quiet')));return el('main',{class:'auth-page'},box);
}
function showAuth(){clearInterval(refreshTimer);if(!root)return;const mode=appState.auth?.setup_required?'setup':'login';root.replaceChildren(authForm(mode));}
async function loadApp(){try{await refresh(false);makeShell();clearInterval(refreshTimer);refreshTimer=setInterval(refreshPagePeriodically,10000);}catch(error){handleError(error);}}
async function boot(){setTheme(localStorage.getItem('muse.theme') || 'system');root=document.getElementById('app');ctx={page:'overview',schema:appState.schema,state:appState.state,autoRefresh:true,chatSessionId:'',navigate,refresh,renderContent,notify,handleError,openChat};pages=createPages(ctx);chat=createChat(ctx);try{const auth=await request('/auth/status');appState.auth=auth;if(auth.csrf_token)setCSRF(auth.csrf_token);if(!auth.authenticated){showAuth();return;}await loadApp();}catch(error){showAuth();notify(error.message,true);}}
boot();
