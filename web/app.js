'use strict';
const $ = id => document.getElementById(id);
// The panel's CSP allows no style attributes in markup (style-src 'self'). Rendered HTML carries
// data-css instead, and this applies it through the CSSOM, which the policy permits.
const applyCss = root => { if (!(root instanceof Element)) return; const nodes = root.matches('[data-css]') ? [root] : []; nodes.push(...root.querySelectorAll('[data-css]')); for (const el of nodes) { el.style.cssText = el.dataset.css; el.removeAttribute('data-css'); } };
new MutationObserver(records => { for (const record of records) for (const node of record.addedNodes) applyCss(node); }).observe(document.documentElement, {childList: true, subtree: true});
const loginScreen = $('login-screen');
const state = {csrf:'',users:[],status:null,control:null,metrics:null,audit:null,usage:null,nodes:null,deletingNode:null,diagnostics:null,info:null,usageMonth:'',statistics:null,statisticsNode:'main',statisticsPeriod:'24h',statisticsDay:'',updates:null,auditFilterAction:'',auditFilterUser:'',errors:{},view:'overview',editing:null,deleting:null,profileUser:null,profile:null,profileRequest:0,busy:false,loading:false,loadedAt:null,groups:null,editingGroup:null,selected:new Set(),rowMenu:null,nodeGroups:null};
const escapeHTML = value => String(value ?? '').replace(/[&<>"']/g, char => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[char]));
const number = value => new Intl.NumberFormat('ru-RU').format(value);
const bytes = value => { if (value == null) return '—'; const n=Number(value); if(n<1024)return `${number(n)} Б`; const i=Math.min(5,Math.floor(Math.log(n)/Math.log(1024))); return `${(n/1024**i).toLocaleString('ru-RU',{minimumFractionDigits:1,maximumFractionDigits:1})} ${['Б','КиБ','МиБ','ГиБ','ТиБ','ПиБ'][i]}`; };
const speed = value => value ? `${number(value)} Мбит/с` : 'Без лимита';
function localDateTime(value){if(!value)return '';const d=new Date(value);if(Number.isNaN(d.getTime()))return '';return new Date(d.getTime()-d.getTimezoneOffset()*60000).toISOString().slice(0,19);}
function expiryFromForm(form,user){const field=form.elements.expires;const previous=user?.policy.expires_at||'';if(field.disabled||field.value===localDateTime(previous))return previous;return field.value?new Date(field.value).toISOString():'';}
function quotaFromForm(form,user){const field=form.elements.quota;const previous=user?.policy.quota_bytes||0;if(field.disabled||field.value===String(previous/1073741824))return previous;return Math.round(Number(field.value)*1073741824);}
function trafficFor(user){const live=state.control?.users.find(u=>u.id===user.id);if(!state.control?.usage||!live)return null;return {used:live.quota_used_bytes,limit:live.quota_bytes};}
function trafficView() {
  const usage = state.usage;
  if (!usage) return '<section class="panel"><h2>Расход по пользователям</h2><p class="empty">Учёт расхода недоступен.</p></section>';
  const rows = usage.records.map(record => {
    const user = state.users.find(u => u.id === record.user_id);
    if (!user) return '';
    const live = metricsFor(user);
    const access = policyStatus(user);
    const quota = user.policy.quota_bytes;
    return `<tr>
      <td><span class="user-identity">${live?.sessions > 0 ? '<span class="user-online-dot" aria-hidden="true"></span>' : ''}<strong>${escapeHTML(user.username)}</strong></span>
        ${state.metrics ? `<small>${live?.sessions > 0 ? 'В сети' : 'Не в сети'}</small>` : ''}</td>
      <td class="${access[1]}">${access[0]}</td>
      <td>${user.locations ? `${user.locations.applied} из ${user.locations.total}` : '—'}</td>
      <td class="num">${bytes(record.charged_bytes)}${quota ? `<small>Остаток ${bytes(Math.max(0, quota-record.charged_bytes))} из ${bytes(quota)}</small>` : ''}</td>
    </tr>`;
  }).join('');
  return `<section class="panel"><div class="panel-head"><h2>Расход по пользователям</h2><a href="#users">Все пользователи →</a></div>
    <div class="table-wrap"><table class="overview-traffic-table"><thead><tr><th>Пользователь</th><th>Доступ</th><th>Локации</th><th class="num">Расход</th></tr></thead>
    <tbody>${rows || '<tr><td colspan="4" class="empty">В загруженных отчётах за этот месяц нет расхода по существующим пользователям.</td></tr>'}</tbody></table></div>
    ${usage.next_after ? '<button id="usage-more">Загрузить ещё</button>' : ''}
    <p class="hint">Учёт за ${escapeHTML(usage.month)} (UTC). Итоги за месяц включают расход удалённых пользователей.${hasNodes() ? ' При подключённых нодах общая квота недоступна.' : ''}</p></section>`;
}

function trafficChart() {
  const usage = state.usage;
  const history = [...(state.usageHistory || [])].sort((a,b) => a.month.localeCompare(b.month));
  const max = Math.max(1, ...history.flatMap(report => [report.totals.download_bytes, report.totals.upload_bytes]));
  const x = i => history.length === 1 ? 338 : 74 + i * 528 / (history.length - 1);
  const y = value => 182 - (value || 0) / max * 142;
  const line = key => history.map((report,i) => `${x(i)},${y(report.totals[key])}`).join(' ');
  const options = usage?.months.map(month => `<option value="${escapeHTML(month)}"${month===usage.month?' selected':''}>${escapeHTML(month)}</option>`).join('');
  const graph = history.length ? `<svg viewBox="0 0 620 222" role="img" aria-label="Загрузка и отдача по месяцам UTC">
    ${[0,1,2,3].map(i => `<line x1="74" x2="602" y1="${y(max*i/3)}" y2="${y(max*i/3)}" class="chart-grid"/><text x="0" y="${y(max*i/3)+4}">${bytes(max*i/3)}</text>`).join('')}
    ${history.length>1 ? `<polygon class="chart-area" points="74,182 ${line('download_bytes')} 602,182"/>` : ''}
    ${['download_bytes','upload_bytes'].map(key => `<g class="${key}"><polyline points="${line(key)}"/>${history.map((report,i) => `<circle cx="${x(i)}" cy="${y(report.totals[key])}" r="3.5"><title>${escapeHTML(report.month)} · ${bytes(report.totals[key])}</title></circle>`).join('')}</g>`).join('')}
    ${history.map((report,i) => `<text x="${x(i)}" y="208" text-anchor="${history.length===1?'middle':i===0?'start':i===history.length-1?'end':'middle'}">${escapeHTML(report.month)}</text>`).join('')}</svg>
    <div class="chart-legend"><span>Загрузка</span><span>Отдача</span></div>` : '<p class="empty">История трафика пока недоступна.</p>';
  return `<section class="panel traffic-chart"><div class="panel-head"><h2>Трафик по месяцам</h2>${options ? `<select id="usage-month" aria-label="Месяц расхода UTC">${options}</select>` : ''}</div>${graph}<p class="hint">Месячные итоги (UTC), не скорость в реальном времени.${history.length===1?' Накоплен один месяц: загрузка и отдача показаны точками.':''}</p></section>`;
}
async function usagePage(button){await run(button,async()=>{const page=await api('/usage?'+new URLSearchParams({month:state.usage.month,after:state.usage.next_after}));state.usage={...page,records:[...state.usage.records,...page.records]};render();});}
const messages = {group_required:'Выберите группу: она определяет, какие локации доступны пользователю.',group_not_empty:'В группе ещё есть участники. Переведите их в другую группу, потом удаляйте.',group_fixed:'Группу по умолчанию удалить нельзя: в неё попадают пользователи, когда ничего другого не задано.',group_not_found:'Группа не найдена. Обновите страницу.',invalid_group:'Название группы должно быть от 1 до 40 символов.',unknown_location:'Локация не найдена: возможно, нода удалена. Обновите страницу.',invalid_client_settings:"Проверьте названия, адреса, приложения и DNS. Нельзя больше 10 наборов или 8 публичных DNS.",client_settings_conflict:"Настройки изменены в другой сессии. Обновите страницу перед сохранением.",invalid_second_factor:"Неверный или уже использованный код. Дождитесь нового кода или используйте резервный.",factor_retry:"Настройки 2FA изменились. Обновите страницу и повторите действие.",quota_not_supported_with_nodes:'При подключённых нодах месячная квота не поддерживается: каждая нода считает трафик отдельно.',quota_set_remove_first:'Сначала уберите месячную квоту у пользователей',sync_prefix_missing:'Перезапустите панель: ей нужно создать адрес для нод.',node_not_found:'Нода уже удалена. Обновите страницу.',invalid_node_identity:'Название — до 64 печатных символов, страна — две латинские буквы.',invalid_audit_query:'Не удалось загрузить более ранние события.',invalid_server_identity:'Название — до 64 символов без управляющих; страна — две латинские буквы (NL, DE) или пусто.',invalid_credentials:'Неверный логин или пароль.',invalid_current_password:'Текущий пароль указан неверно.',weak_password:'Новый пароль должен содержать от 12 до 1024 символов.',password_unchanged:'Новый пароль совпадает с текущим.',password_change_unavailable:'Смена пароля недоступна в этом режиме запуска панели.',config_unavailable:'Не удалось прочитать настройки панели на сервере.',config_write_failed:'Не удалось сохранить новый пароль на сервере. Пароль не изменён.',too_many_attempts:'Слишком много попыток. Повторите через минуту.',login_busy:'Вход занят. Повторите через несколько секунд.',authentication_required:'Сессия завершилась. Войдите снова.',username_exists:'Такое имя уже занято.',invalid_user:'Проверьте имя и параметры пользователя.',revision_conflict_or_missing_user:'Пользователь изменён в другой сессии. Обновите список и откройте форму заново.',quota_or_expiry_not_supported_yet:'Этот endpoint ещё не поддерживает ограничение. Доступ не применён.',quota_not_supported_yet:'Месячная квота ещё не поддерживается. Доступ не применён.',endpoint_control_unavailable:'Endpoint недоступен. Сохранённые настройки не подтверждены.',enabled_applied_user_required:'Сначала включите пользователя и примените настройки.',csrf_required:'Сессия устарела. Обновите страницу.',invalid_protocol:'Выберите HTTP/2 или HTTP/3.',endpoint_expiry_not_supported:'Текущая версия endpoint ещё не поддерживает срок доступа.',user_expired:'Срок доступа истёк. Продлите его и примените изменения.',endpoint_quota_not_supported:'Текущий endpoint ещё не поддерживает месячную квоту.',user_quota_exhausted:'Месячная квота исчерпана. Увеличьте лимит или дождитесь нового месяца.',usage_month_unavailable:'Статистика этого месяца недоступна.',endpoint_remove_not_supported:'Текущий endpoint ещё не поддерживает удаление пользователей.',removed_but_reconciliation_required:'Доступ отозван на VPN-сервере, но запись в панели ещё не удалена. Обновите страницу и повторите удаление.',endpoint_acknowledgement_mismatch:'Endpoint не подтвердил результат операции. Обновите страницу и повторите.',revision_conflict:'Политика на VPN-сервере изменилась. Обновите страницу перед повторной операцией.'};
async function api(path, options={}) {
  let response;
  try { response=await fetch('/api/v1'+path,{...options,headers:{'Content-Type':'application/json','X-CSRF-Token':state.csrf,...options.headers},credentials:'same-origin',cache:'no-store'}); }
  catch { throw new Error('Нет соединения с панелью. Проверьте сеть и повторите действие.'); }
  const result = response.status===204 ? {} : await response.json();
  if(!response.ok){if(response.status===401 && path!=='/auth/login')showLogin();throw new Error((messages[result.error]&&result.users?messages[result.error]+': '+result.users.join(', ')+'.':messages[result.error]) || `Запрос не выполнен (${response.status}): ${result.error || 'ошибка сервера'}`);}
  return result;
}
function showLogin(){resetLiveTraffic();sharedSettings=null;sharedDirty=false;document.body.prepend(loginScreen);$('login-form').elements.password.value='';showLoginFactor(false);state.csrf='';state.users=[];state.profile=null;state.profileUser=null;state.metrics=null;state.control=null;state.audit=null;state.usage=null;state.diagnostics=null;state.info=null;state.usageMonth='';state.statistics=null;state.statisticsPeriod='24h';state.statisticsNode='main';state.statisticsDay='';state.status=null;state.loadedAt=null;for(const dialog of document.querySelectorAll('dialog'))dialog.close();$('profile-link').value='';setQR('profile-qr','');$('profile-result').hidden=true;$('content').replaceChildren();$('workspace').hidden=true;loginScreen.hidden=false;}
function showWorkspace(session){state.csrf=session.csrf_token;$('admin-name').textContent=session.username;loginScreen.remove();$('workspace').hidden=false;}
function notify(text){$('notice').textContent=text;$('notice').hidden=!text;}
function policyStatus(user){const live=state.control?.users.find(x=>x.id===user.id);if(!state.control)return ['Нет подтверждения','warn'];if(!live||live.revision!==user.desired_revision||user.desired_revision!==user.applied_revision||live.enabled!==user.policy.enabled||live.download_mbps!==user.policy.download_mbps||live.upload_mbps!==user.policy.upload_mbps||((live.enabled||state.status?.capabilities.durable_quotas)&&(live.quota_bytes||0)!==user.policy.quota_bytes)||((live.enabled||state.status?.capabilities.expiry)&&(live.expires_at||'')!==(user.policy.expires_at||'')))return ['Не применено','warn'];if(live.enabled&&live.quota_exhausted)return ['Квота исчерпана','warn'];if(live.enabled&&live.expired)return ['Срок истёк','warn'];return live.enabled?['Доступ разрешён','ok']:['Отключён','muted'];}
function metricsFor(user){if(!state.metrics)return null;const rows=state.metrics.clients.filter(x=>x.username===user.username);if(!rows.length)return null;return rows.reduce((sum,x)=>({sessions:sum.sessions+x.sessions,inbound:sum.inbound+x.inbound,outbound:sum.outbound+x.outbound,country:sum.country||x.country||""}),{sessions:0,inbound:0,outbound:0,country:""});}
function connectionsWord(n){const t=n%100,o=n%10;return t>10&&t<20?'транспортных соединений':o===1?'транспортное соединение':o>=2&&o<=4?'транспортных соединения':'транспортных соединений';}
function tokensWord(n){const t=n%100,o=n%10;return t>10&&t<20?'токенов':o===1?'токен':o>=2&&o<=4?'токена':'токенов';}
function ago(iso){const s=Math.max(0,(Date.now()-new Date(iso).getTime())/1000);if(s<90)return 'только что';const m=Math.round(s/60);if(m<60)return `${m} мин назад`;const h=Math.round(m/60);if(h<24)return `${h} ч назад`;const d=Math.round(h/24);return d<31?`${d} дн назад`:new Date(iso).toLocaleDateString('ru-RU');}
function liveIP(user){const row=state.metrics?.clients.find(x=>x.username===user.username&&x.sessions>0&&x.ip);return row?row.ip:'';}
function isOnline(user){const m=metricsFor(user);return !!(m&&m.sessions>0);}
function presenceCell(u,m){const seen=u.last_seen_at?`<small title="${escapeHTML(new Date(u.last_seen_at).toLocaleString('ru-RU'))}">`:'<small>';if(m&&m.sessions>0){const ip=liveIP(u)||u.last_ip;return `<span class="ok">В сети</span><small>${ip?escapeHTML(ip)+' · ':''}${number(m.sessions)} ${connectionsWord(m.sessions)}</small>`;}if(!state.metrics)return u.last_seen_at?`<span class="muted">Нет данных</span>${seen}Был(а) ${ago(u.last_seen_at)}${u.last_ip?' · '+escapeHTML(u.last_ip):''}</small>`:'<span class="muted">Нет данных</span>';if(!u.last_seen_at)return '<span class="muted">Не в сети</span><small>Ещё не подключался</small>';return `<span class="muted">Не в сети</span>${seen}Был(а) ${ago(u.last_seen_at)}${u.last_ip?' · '+escapeHTML(u.last_ip):''}</small>`;}
function userStatus(u){const t=trafficFor(u);if(t&&t.limit&&t.used>=t.limit)return{k:'exhausted',label:'Квота исчерпана',cls:'st-exhausted'};if(u.policy.expires_at&&new Date(u.policy.expires_at).getTime()<=Date.now())return{k:'expired',label:'Срок истёк',cls:'st-expired'};if(!u.policy.enabled)return{k:'disabled',label:'Отключён',cls:'st-off'};if(isOnline(u))return{k:'online',label:'В сети',cls:'st-online'};if(u.last_seen_at)return{k:'offline',label:'Не в сети',cls:'st-off'};return{k:'never',label:'Ни разу не подключался',cls:'st-never'};}function lastSeenCell(u,m){if(m&&m.sessions>0)return '<span class="ok">сейчас</span>';if(!u.last_seen_at)return '<span class="muted">—</span>';return `<span title="${escapeHTML(new Date(u.last_seen_at).toLocaleString('ru-RU'))}">${escapeHTML(ago(u.last_seen_at))}</span>`;}
function flagImage(country){if(!/^[A-Za-z]{2}$/.test(country||''))return '';const points=[...country.toUpperCase()].map(c=>(0x1F1E6+c.charCodeAt(0)-65).toString(16)).join('-');return `<img class="flag flag-emoji" src="/twemoji/${points}.svg" alt="" width="20" height="20" loading="lazy">`;}
function usageCell(u){const t=trafficFor(u),m=metricsFor(u),used=t?t.used:(m?m.inbound+m.outbound:null);if(used==null)return '<span class="muted">—</span>';const limit=t?t.limit:0,pct=limit?Math.min(100,Math.round(used/limit*100)):0,cls=limit?(pct>=100?'full':pct>=80?'warn':''):'';return `<div class="usage"><div class="usage-top"><span class="used">${bytes(used)}</span><span class="pct">${limit?pct+'%':'∞'}</span></div><div class="usage-bar"><i class="${cls}" data-css="width:${limit?pct:0}%"></i></div><div class="usage-limit">${limit?'из '+bytes(limit):'без лимита'}</div></div>`;}
function connectionCell(u,m){const live=!!(m&&m.sessions>0),ip=(live?liveIP(u):'')||u.last_ip,country=(m&&m.country)||'';const address=ip?`<span class="connection">${flagImage(country)}<span class="ip">${escapeHTML(ip)}</span></span>`:'';const note=live?`<small>${number(m.sessions)} ${connectionsWord(m.sessions)}</small>`:'';return address+note||'<span class="muted">—</span>';}
function bulkBarContent(){const groups=state.groups?.groups||[];return `<div class="bulk-bar"><span class="count">Выбрано ${state.selected.size}</span><span class="spacer"></span><label class="sr-only" for="bulk-group">Группа для перевода</label><select id="bulk-group" aria-label="Группа для перевода">${groups.map(g=>`<option value="${escapeHTML(g.id)}">${escapeHTML(g.name)}</option>`).join('')}</select><button data-bulk="move">Перевести</button><button data-bulk="block">Заблокировать</button><button data-bulk="unblock">Разблокировать</button><button class="danger" data-bulk="delete">Удалить</button><button class="ghost" data-bulk="clear">Снять выбор</button></div>`;}
function refreshBulkBar(){const holder=$('user-list');if(!holder)return;const bar=holder.querySelector('.bulk-bar');if(!state.selected.size){if(bar)bar.remove();}else if(bar)bar.outerHTML=bulkBarContent();else holder.insertAdjacentHTML('afterbegin',bulkBarContent());const all=$('select-all');if(all){const boxes=[...holder.querySelectorAll('[data-select]')];all.checked=boxes.length>0&&boxes.every(b=>b.checked);all.indeterminate=!all.checked&&boxes.some(b=>b.checked);}}
function openRowMenu(button,user){closeRowMenu();const pending=policyStatus(user)[0]==='Не применено';const menu=document.createElement('div');menu.className='row-menu';menu.setAttribute('role','menu');menu.dataset.user=user.id;menu.innerHTML=`<button role="menuitem" data-action="edit" data-id="${escapeHTML(user.id)}">Изменить</button><button role="menuitem" data-action="disconnect" data-id="${escapeHTML(user.id)}">Отключить соединения</button><button role="menuitem" data-menu-subscription="${escapeHTML(user.id)}">Скопировать ссылку подписки</button><button role="menuitem" data-action="apply" data-id="${escapeHTML(user.id)}"${pending?'':' disabled'}>Применить политику</button>`;document.body.appendChild(menu);const box=button.getBoundingClientRect();menu.style.top=Math.min(box.bottom+6,innerHeight-menu.offsetHeight-8)+'px';menu.style.left=Math.max(8,Math.min(box.right-menu.offsetWidth,innerWidth-menu.offsetWidth-8))+'px';button.setAttribute('aria-expanded','true');}
function closeRowMenu(){document.querySelectorAll('.row-menu').forEach(node=>node.remove());document.querySelectorAll('[data-menu][aria-expanded="true"]').forEach(node=>node.setAttribute('aria-expanded','false'));}
function userTable(users){const rows=users.map(u=>{const s=userStatus(u),m=metricsFor(u),pending=policyStatus(u)[0]==='Не применено';return `<tr${state.selected.has(u.id)?' class="is-selected"':''}><td class="check-col"><input type="checkbox" class="user-check" data-select="${escapeHTML(u.id)}"${state.selected.has(u.id)?' checked':''} aria-label="Выбрать ${escapeHTML(u.username)}"></td><td class="user-name">${escapeHTML(u.username)}${u.policy.expires_at?`<small>До ${escapeHTML(new Date(u.policy.expires_at).toLocaleString('ru-RU'))}</small>`:''}${clientLine(u)}</td><td data-label="Статус"><span class="status ${s.cls}"><span class="label">${s.label}</span></span>${pending?'<small class="pending">Не применено</small>':''}</td><td data-label="Когда был в сети">${lastSeenCell(u,m)}</td><td class="user-group-cell" data-label="Группа">${u.group?escapeHTML(u.group.name):'<span class="muted">—</span>'}</td><td class="num" data-label="Расход">${usageCell(u)}</td><td data-label="Подключение">${connectionCell(u,m)}</td><td><div class="row-actions"><button data-action="profile" data-id="${escapeHTML(u.id)}">Профиль</button><button class="more" data-menu="${escapeHTML(u.id)}" aria-haspopup="true" aria-expanded="false" aria-label="Ещё действия">⋯</button></div></td></tr>`;}).join('');return `${state.selected.size?bulkBarContent():''}<div class="table-wrap"><table class="user-table"><thead><tr><th class="check-col"><input type="checkbox" id="select-all" class="user-check" aria-label="Выбрать всех на странице"></th><th>Пользователь</th><th>Статус</th><th>Когда был в сети</th><th>Группа</th><th class="num">Расход</th><th>Подключение</th><th class="num">Действия</th></tr></thead><tbody>${rows||`<tr><td colspan="8" class="empty">${state.users.length?'Нет пользователей по выбранным условиям.':'Пользователей пока нет. Создайте первого — он появится в списке и получит доступ.'}</td></tr>`}</tbody></table></div>`;}
function kv(label,value,cls=''){return `<div class="kv"><span>${escapeHTML(label)}</span><span class="${cls}">${escapeHTML(value)}</span></div>`;}
function healthBlock(d){if(!d)return '<section class="panel"><h2>Работоспособность</h2><p class="empty">Диагностика недоступна. Нажмите «Обновить».</p></section>';const badge={ok:'Всё работает',warn:'Требует внимания',error:'Есть проблема'};return `<section class="panel"><div class="panel-head"><h2>Работоспособность</h2><span class="${esc(d.verdict)}">${badge[d.verdict]||'—'}</span></div><ul class="health">${d.checks.map(c=>`<li class="${esc(c.status)}"><strong>${escapeHTML(c.name)}</strong><span class="health-detail">${escapeHTML(c.detail)}</span></li>`).join('')}</ul></section>`;}
const esc = c => ({ok:'ok',warn:'warn',error:'error',muted:'muted'}[c]||c);
const liveTraffic = {previous:null, samples:[], rates:new Map(), current:null};
function resetLiveTraffic(){liveTraffic.previous=null;liveTraffic.samples=[];liveTraffic.rates.clear();liveTraffic.current=null;}
function sampleLiveTraffic(metrics) {
  const at = Date.parse(metrics?.observed_at) || Date.now();
  if (!metrics) {
    liveTraffic.previous=null;liveTraffic.current=null;liveTraffic.rates.clear();
    liveTraffic.samples.push({at,download:null,upload:null});
  } else {
    const counters=new Map();
    for(const row of metrics.clients){
      const old=counters.get(row.username)||{inbound:0,outbound:0};
      counters.set(row.username,{inbound:old.inbound+(row.inbound||0),outbound:old.outbound+(row.outbound||0)});
    }
    const previous=liveTraffic.previous;
    const seconds=previous?(at-previous.at)/1000:0;
    if(previous&&seconds>=0&&seconds<1)return;
    liveTraffic.rates.clear();
    let current=null;
    if(previous&&seconds>=1&&seconds<=45){
      current={download:0,upload:0};
      for(const [name,now] of counters){
        const old=previous.counters.get(name);
        if(!old)continue; // A newly observed account needs its own baseline.
        if(now.inbound<old.inbound||now.outbound<old.outbound){current=null;break;}
        const rate={download:(now.outbound-old.outbound)/seconds,upload:(now.inbound-old.inbound)/seconds};
        liveTraffic.rates.set(name,rate);current.download+=rate.download;current.upload+=rate.upload;
      }
    }
    if(!current)liveTraffic.rates.clear();
    liveTraffic.current=current;
    liveTraffic.previous={at,counters};
    liveTraffic.samples.push({at,download:current?.download??null,upload:current?.upload??null});
  }
  liveTraffic.samples=liveTraffic.samples.filter(p=>p.at>=at-15*60*1000).slice(-61);
}
const rateText=value=>value==null?'—':`${bytes(Math.round(value))}/с`;
function liveTrafficChart(){
  const samples=liveTraffic.samples,valid=samples.filter(p=>p.download!=null);
  const max=Math.max(1024,...valid.flatMap(p=>[p.download,p.upload]));
  const first=samples[0]?.at||Date.now(),last=samples.at(-1)?.at||first;
  const x=p=>last===first?338:74+(p.at-first)/(last-first)*528;
  const y=v=>182-v/max*142;
  const time=at=>new Date(at).toLocaleTimeString('ru-RU');
  const series=key=>{
    const segments=[];let points=[];
    for(const p of samples){if(p[key]==null){if(points.length)segments.push(points);points=[];}else points.push(p);}
    if(points.length)segments.push(points);
    return `<g class="${key==='download'?'download_bytes':'upload_bytes'}">${segments.map(points=>`<polyline points="${points.map(p=>`${x(p)},${y(p[key])}`).join(' ')}"/>${points.map(p=>`<circle cx="${x(p)}" cy="${y(p[key])}" r="3"><title>${time(p.at)} · ${rateText(p[key])}</title></circle>`).join('')}`).join('')}</g>`;
  };
  return `<section class="panel traffic-chart"><div class="panel-head"><h2>Трафик сейчас</h2><span class="hint">Главный сервер · 15 с</span></div>
    <div class="live-rates"><span>↓ Загрузка <strong>${rateText(liveTraffic.current?.download)}</strong></span><span>↑ Отдача <strong>${rateText(liveTraffic.current?.upload)}</strong></span></div>
    ${valid.length?`<svg viewBox="0 0 620 222" role="img" aria-label="Скорость VPN по текущим замерам">
      ${[0,1,2,3].map(i=>`<line x1="74" x2="602" y1="${y(max*i/3)}" y2="${y(max*i/3)}" class="chart-grid"/><text x="0" y="${y(max*i/3)+4}">${rateText(max*i/3)}</text>`).join('')}
      ${series('download')}${series('upload')}<text x="74" y="208">${time(first)}</text><text x="602" y="208" text-anchor="end">${time(last)}</text></svg>`:
      `<p class="empty">${state.metrics?'Собираем замеры. Скорость появится после следующего обновления.':'Метрики недоступны. Ожидаем соединения с сервером.'}</p>`}
    <div class="chart-legend"><span>Загрузка</span><span>Отдача</span></div><p class="hint">Средняя скорость между замерами. История до 15 минут собирается, пока открыта панель. После пропуска или сброса счётчиков нужен новый замер.</p></section>`;
}
function liveTrafficView(){
  return `<section class="panel"><div class="panel-head"><h2>Трафик пользователей сейчас</h2><a href="#users">Все пользователи →</a></div>
    <div class="table-wrap"><table class="live-traffic-table"><thead><tr><th>Пользователь</th><th>Подключение</th><th class="num">↓ Загрузка / ↑ Отдача</th><th class="num">Передано</th></tr></thead><tbody>${state.users.map(u=>{
      const m=metricsFor(u),rate=liveTraffic.rates.get(u.username);
      return `<tr><td><span class="user-identity">${m?.sessions>0?'<span class="user-online-dot" aria-hidden="true"></span>':''}<strong>${escapeHTML(u.username)}</strong></span></td><td>${presenceCell(u,m)}</td><td class="num">↓ ${rateText(rate?.download)}<small>↑ ${rateText(rate?.upload)}</small></td><td class="num">${m?bytes(m.inbound+m.outbound):'—'}</td></tr>`;
    }).join('')||'<tr><td colspan="4" class="empty">Пользователей пока нет.</td></tr>'}</tbody></table></div><p class="hint">Главный сервер. «Передано» — счётчики VPN с запуска движка, не месячный расход. Скорости — средние между текущими замерами.</p></section>`;
}
const statisticPeriods={'1h':'1ч','6h':'6ч','24h':'24ч','7d':'7д','30d':'30д'};
const chartNames={download:'Загрузка',upload:'Отдача',online:'Уникальные пользователи',cpu:'Загрузка CPU',memory:'Память',disk:'Диск'};
// Верх шкалы — ровное число в тех же единицах, что и подписи (КиБ/МиБ/ГиБ), а не 14,3 МиБ.
function niceFloat(value){const base=Math.pow(10,Math.floor(Math.log10(Math.max(1,value)))),meter=value/base;for(const step of [1,1.2,1.5,1.8,2,2.2,2.5,2.8,3,3.5,4,4.5,5,6,7,8,9,10])if(meter<=step)return step*base;return 10*base;}
function niceBytes(value){const unit=Math.pow(1024,Math.min(5,Math.max(0,Math.floor(Math.log(Math.max(1,value))/Math.log(1024)))));return niceFloat(value/unit)*unit;}
function statisticsChart(points,key,{percent=false,bars=false,second=null}={}){
  // Длинные периоды сервер отдаёт по ~360 интервалов: в столбцах это доли пикселя и «мелкий
  // график». Столбцы группируются так, чтобы каждый был не уже ~9 единиц (около 7 px).
  if(bars&&points.length>90){
    const groups=Math.max(1,Math.round(528/9));
    const size=Math.ceil(points.length/groups);
    const merged=[];
    for(let i=0;i<points.length;i+=size){
      const slice=points.slice(i,i+size);
      const row={at:Math.round((slice[0].at+slice.at(-1).at)/2),from:slice[0].at,to:slice.at(-1).at};
      const own=slice.filter(p=>p[key]!=null);
      row[key]=own.length?own.reduce((sum,p)=>sum+Number(p[key]),0):null;
      if(second)row[second]=slice.some(p=>p[second]!=null)?slice.reduce((sum,p)=>sum+(Number(p[second])||0),0):null;
      merged.push(row);
    }
    points=merged;
  }
  const available=points.filter(p=>p[key]!=null);
  if(!available.length)return '<p class="empty statistics-empty">История ещё не накоплена. Первые замеры появятся после начала сбора метрик.</p>';
  // Столбец потребления показывает интервал целиком: загрузку и отдачу вместе.
  const stack=p=>Number(p[key])+(second?Number(p[second])||0:0);
  const values=bars?available.map(stack):available.map(p=>Number(p[key]));
  const rawMax=Math.max(0,...values);
  if(bars&&rawMax<=0)return '<p class="empty statistics-empty">За выбранный период трафика нет: ни в одном интервале не было передачи.</p>';
  // Одиночный пик не должен сплющивать остальные столбцы, но и простой не должен его прятать:
  // 95-й процентиль считается по ненулевым интервалам, иначе тихие сутки давали шкалу в один байт
  // и все ненулевые столбцы уходили за верх как «выше шкалы».
  const ranked=bars?values.filter(v=>v>0).sort((a,b)=>a-b):[];
  const percentile=ranked.length?ranked[Math.min(ranked.length-1,Math.floor(ranked.length*.95))]:1;
  const max=percent?100:bars?niceBytes(percentile*1.08):Math.max(3,Math.ceil(rawMax/3)*3);
  const clipped=Boolean(bars&&rawMax>max);
  const first=points[0].at,last=points.at(-1).at;
  const x=p=>first===last?338:74+(p.at-first)/(last-first)*528;
  const y=v=>182-Math.min(Number(v),max)/max*142;
  const label=v=>percent?`${Math.round(v)}%`:bars?bytes(v):number(Math.round(v));
  const stamp=at=>new Date(at*1000).toLocaleString('ru-RU',{month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit'});
  // Столбец плотной истории — диапазон: подпись показывает его границы, а не одну минуту.
  const clock=at=>new Date(at*1000).toLocaleTimeString('ru-RU',{hour:'2-digit',minute:'2-digit'});
  const span=p=>p.from==null?stamp(p.at):`${clock(p.from)} – ${clock(p.to)}`;
  const name=chartNames[key]||key;
  const step=points.length>1?528/(points.length-1):528;
  // За длинные периоды точек сотни: столбец уже шага становился долей пикселя, два цвета сливались в муар.
  // Поэтому плотная история рисуется столбцами шириной в шаг (с нахлёстом от шва), редкая — с зазором.
  const dense=bars&&step<4;
  const columnWidth=bars?Math.min(18,Math.max(1,step-(dense?-.4:Math.max(.8,Math.min(2.4,step*.3))))):0;
  const rounded=columnWidth>=3?1:0;
  // Зона наведения шире шага и перекрывает соседние: нужный столбец ищется по близости центра, не по порядку в DOM.
  const hitWidth=Math.max(step,8);
  const hits=points.map(p=>{const rows=[span(p)];if(p[key]!=null)rows.push(`${name}~${label(p[key])}`);if(second&&p[second]!=null)rows.push(`${chartNames[second]||second}~${bytes(p[second])}`);return `<rect class="chart-hit" x="${x(p)-columnWidth/2-hitWidth/2}" y="40" width="${hitWidth}" height="142" data-tip="${escapeHTML(rows.join('|'))}"/>`;}).join('');
  const series=(field,cls)=>{
    let segments=[],segment=[];let previous=null;
    const nominal=(last-first)/Math.max(1,points.length-1);
    for(const p of points){if(p[field]==null||(previous&&p.at-previous>Math.max(150,nominal*2.5))){if(segment.length)segments.push(segment);segment=[];}if(p[field]!=null)segment.push(p);previous=p.at;}
    if(segment.length)segments.push(segment);
    // Точки на плотном графике сливались в сплошную полосу, поэтому рисуются только там, где различимы.
    const markers=step>=6;
    return `<g class="${cls}">${segments.map(ps=>`<polyline points="${ps.map(p=>`${x(p)},${y(p[field])}`).join(' ')}"/>${markers?ps.map(p=>`<circle cx="${x(p)}" cy="${y(p[field])}" r="2"/>`).join(''):''}`).join('')}</g>`;
  };
  const columns=bars?points.map(p=>{
    const down=p[key]==null?null:Number(p[key]);
    if(down==null)return '';
    const up=second?Number(p[second])||0:0,total=down+up,left=x(p)-columnWidth,bottom=y(down),top=y(total);
    // Выше шкалы: столбец светлый на всю высоту, точный пик — в бейдже над графиком.
    if(total>max)return `<g><rect class="bar-clipped" x="${left}" y="40" width="${columnWidth}" height="142"/><path class="chart-peak" d="M${left+columnWidth/2},37 l4,-5 -8,0 z"/></g>`;
    return `<g>${down>0?`<rect class="statistics-download" x="${left}" y="${bottom}" width="${columnWidth}" height="${182-bottom}" rx="${rounded}"/>`:''}${up>0?`<rect class="statistics-upload" x="${left}" y="${top}" width="${columnWidth}" height="${bottom-top}" rx="${rounded}"/>`:''}</g>`;
  }).join(''):series(key,'download_bytes');
  const ticks=first===last?[points[0]]:[0,.33,.66,1].map(f=>points[Math.round(f*(points.length-1))]);
  return `<svg viewBox="0 0 620 222" role="img" aria-label="${bars?'Потребление':'История'}: ${escapeHTML(name)}${second?` и ${escapeHTML(chartNames[second]||second)}`:''}">
    <rect class="chart-surface" x="74" y="40" width="528" height="142"/>
    ${(percent?[0,.25,.5,.75,1]:[0,1/3,2/3,1]).map(i=>`<line x1="74" x2="602" y1="${y(max*i)}" y2="${y(max*i)}" class="chart-grid"/><text x="0" y="${y(max*i)+4}">${escapeHTML(label(max*i))}</text>`).join('')}
    ${columns}
    ${hits}
    <line class="chart-cursor" x1="0" x2="0" y1="40" y2="182" data-css="display:none"/>
    ${ticks.map((p,i)=>`<text x="${x(p)}" y="211" text-anchor="${i===0?'start':(i===ticks.length-1?'end':'middle')}">${stamp(p.at)}</text>`).join('')}</svg>
    ${clipped?`<span class="chart-badge">пик ${escapeHTML(bytes(rawMax))} · выше шкалы</span>`:''}`;
}
function clientStatistics(report){
  const clients=report?.clients||[],total=clients.reduce((n,c)=>n+c.count,0),day=report?.client_day||state.statisticsDay||new Date().toISOString().slice(0,10);
  const colors=['var(--accent)','#56c6a6','#939eac','#e9bb72'];let offset=0;const circumference=2*Math.PI*63;
  const ring=clients.map((c,i)=>{const length=total?c.count/total*circumference:0;const path=`<circle cx="83" cy="83" r="63" fill="none" stroke="${colors[i%colors.length]}" stroke-width="17" stroke-dasharray="${length} ${circumference-length}" stroke-dashoffset="${-offset}"/>`;offset+=length;return path;}).join('');
  return `<section class="panel client-statistics"><div class="panel-head"><h2>VPN-клиенты</h2><label class="statistics-date"><span>День UTC</span><input id="statistics-day" type="date" value="${escapeHTML(day)}" max="${new Date().toISOString().slice(0,10)}" min="${new Date(Date.now()-30*86400000).toISOString().slice(0,10)}" aria-label="День статистики VPN-клиентов UTC"></label></div>
    <p class="hint">Приложения, которые запрашивали подписку. Один токен учитывается один раз за UTC-день по User-Agent последнего успешного запроса.</p>
    <div class="client-statistics-layout"><div class="client-ring"><svg viewBox="0 0 166 166" role="img" aria-label="Распределение приложений по токенам подписки"><circle cx="83" cy="83" r="63" fill="none" stroke="var(--border)" stroke-width="17"/>${ring}</svg><div><strong>${report?number(total):'—'}</strong><small>${tokensWord(total)} за день</small></div></div>
    <div class="table-wrap"><table><thead><tr><th>Приложение</th><th class="num">Уникальные токены</th><th class="num">Доля</th></tr></thead><tbody>${clients.map((c,i)=>`<tr><td><span class="client-swatch" data-css="background:${colors[i%colors.length]}"></span>${escapeHTML(c.name)}</td><td class="num">${number(c.count)}</td><td class="num">${(c.count/total*100).toLocaleString('ru-RU',{maximumFractionDigits:1})}%</td></tr>`).join('')||`<tr><td colspan="3" class="empty">${report?'За этот день ещё нет запросов подписки.':'Не удалось загрузить статистику подписок.'}</td></tr>`}</tbody></table></div></div><p class="hint">Это запросы подписки, а не активные VPN-подключения. User-Agent сообщает клиент; неопределённые приложения показаны отдельно.</p></section>`;
}
// Вкладки «Обзор» и «Сеть». Сеть показывает локации и ноды на тех данных, что панель уже
// собирает: связь, задержка, группы, клиенты главного сервера. Счётчиков интерфейсов нет.
function overviewTabs(){
  const network=location.hash==='#overview/network';
  return `<div class="overview-tabs" role="tablist" aria-label="Раздел обзора">
    <button type="button" role="tab" data-overview-tab="charts" aria-pressed="${network?'false':'true'}">Статистика</button>
    <button type="button" role="tab" data-overview-tab="network" aria-pressed="${network?'true':'false'}">Сеть</button></div>`;
}
function networkView(){
  const nodes=state.nodes?.nodes||[],metrics=state.metrics,info=state.info;
  const onlineUsers=metrics?state.users.filter(isOnline).length:null;
  const sessions=metrics?metrics.clients.reduce((sum,client)=>sum+client.sessions,0):null;
  const rows=[{
    id:'main',name:info?.server_name||state.status?.domain||'Главный сервер',domain:state.status?.domain||'—',
    country:info?.server_country||'',role:'главный',online:true,latency:null,version:'',synced:true,
    clients:onlineUsers,sessions,seen:null
  }].concat(nodes.map(node=>({
    id:node.id,name:node.name||node.domain,domain:node.domain,country:node.country||'',role:'нода',
    online:node.online!==false,latency:typeof node.latency==='number'?node.latency:null,
    version:node.endpoint_version||'',synced:Boolean(node.online&&node.applied_revision===state.nodes?.users_revision),
    clients:null,sessions:null,seen:node.last_seen_at||null
  })));
  const reachable=rows.filter(row=>row.online).length;
  const latencies=rows.map(row=>row.latency).filter(value=>value!=null);
  const average=latencies.length?Math.round(latencies.reduce((sum,value)=>sum+value,0)/latencies.length):null;
  const summary=[
    ['Локаций',number(rows.length),'Главный сервер и ноды'],
    ['На связи',`${reachable} из ${rows.length}`,'Проверка синхронизации с панелью'],
    ['Средняя задержка',average==null?'—':`${number(average)} мс`,latencies.length?'Замер панели до нод':'Замеров пока нет'],
    ['VPN-клиентов',onlineUsers==null?'—':number(onlineUsers),sessions==null?'Нет данных':`${number(sessions)} ${connectionsWord(sessions)}`]
  ];
  const cell=(row)=>`<tr>
    <td class="user-name">${row.country?flagImage(row.country)+' ':''}${escapeHTML(row.name)}<small>${escapeHTML(row.role)}</small></td>
    <td data-label="Домен">${escapeHTML(row.domain)}</td>
    <td data-label="Связь" class="${row.online?'ok':'warn'}">${row.online?'На связи':'Нет связи'}${row.seen?`<small>${escapeHTML(ago(row.seen))}</small>`:''}</td>
    <td data-label="Задержка" class="num">${row.latency==null?'—':`${number(row.latency)} мс`}</td>
    <td data-label="Синхронизация" class="${row.role==='главный'?'':row.synced?'ok':'warn'}">${row.role==='главный'?'Источник':row.synced?'Синхронизирована':'Ожидает'}</td>
    <td data-label="Группы"><div class="loc-chips">${row.role==='главный'?'<span class="muted">все</span>':(groupsOf(row.id).length?groupsOf(row.id).map(group=>`<span class="loc-chip">${escapeHTML(group.name)}</span>`).join(''):'<span class="loc-chip all">Все локации</span>')}</div></td>
    <td data-label="Клиентов" class="num">${row.clients==null?'—':number(row.clients)}</td></tr>`;
  return `<section class="strip overview-metrics statistics-metrics">${summary.map(([label,value,detail])=>`<div class="stat"><p>${label}</p><div class="value">${value}</div><small>${escapeHTML(detail)}</small></div>`).join('')}</section>
    <section class="panel"><div class="panel-head"><h2>Локации и ноды</h2><span class="hint">Клиенты показаны для главного сервера</span></div>
    <div class="table-wrap"><table class="user-table network-table"><thead><tr><th>Локация</th><th>Домен</th><th>Связь</th><th class="num">Задержка</th><th>Синхронизация</th><th>Группы</th><th class="num">Клиентов</th></tr></thead>
    <tbody>${rows.map(cell).join('')}</tbody></table></div>
    <p class="hint">Задержка — замер панели до ноды; «Ожидает» значит, что нода ещё не применила текущий список пользователей. Клиенты нод видны в «Обзоре» при выборе локации: панель не собирает их одним запросом по всем нодам.</p></section>`;
}
document.addEventListener('click',event=>{
  const tab=event.target.closest('[data-overview-tab]');
  if(!tab)return;
  const next=tab.dataset.overviewTab==='network'?'#overview/network':'#overview';
  if(location.hash!==next)location.hash=next;
  render();
});
function overview(){
  const report=state.statistics,latest=report?.latest;
  const isNode=state.statisticsNode!=='main';
  const selectedNode=state.nodes?.nodes.find(n=>n.id===state.statisticsNode);
  const locationRows=[{id:'main',name:state.info?.server_name||state.status.domain,role:'главный',online:true,latency:null},...(state.nodes?.nodes||[]).map(n=>({id:n.id,name:n.name||n.domain,role:'нода',online:n.online!==false,latency:n.latency??null}))];
    const currentLocation=locationRows.find(r=>r.id===state.statisticsNode)||locationRows[0];
    const locationDot=r=>r.id!=='main'&&!r.online?'location-dot-off':(r.id==='main'?'location-dot-main':'location-dot-on');
    const locationRow=r=>`<button type="button" class="location-row" role="option" data-statistics-location="${escapeHTML(r.id)}" aria-selected="${r.id===state.statisticsNode}"><span class="location-dot ${locationDot(r)}"></span><span class="location-name">${escapeHTML(r.name)}</span><span class="location-meta">${r.online?(r.latency==null?r.role:`${r.role} · ${number(r.latency)} мс`):'не на связи'}</span><svg class="location-check" width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true"><path d="M3 7.5 5.8 10 11 4.5" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg></button>`;
    const selector=`<div class="statistics-location"><span class="location-label" id="statistics-location-label">Локация</span><div class="location-picker" id="statistics-location-picker"><button type="button" class="location-button" id="statistics-location-button" aria-haspopup="listbox" aria-expanded="false" aria-labelledby="statistics-location-label statistics-location-value"><span class="location-dot ${locationDot(currentLocation)}"></span><span class="location-name" id="statistics-location-value">${escapeHTML(currentLocation.name)}</span><span class="location-role">${currentLocation.role}</span><svg class="location-chevron" width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true"><path d="M2.5 4.5 6 8l3.5-3.5" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg></button><div class="location-panel" id="statistics-location-panel" role="listbox" aria-labelledby="statistics-location-label" hidden><p class="location-panel-head">Локации</p>${locationRows.map(locationRow).join('')}<p class="location-panel-foot">↑ ↓ выбор · Enter перейти · Esc закрыть</p></div></div></div>`;
  const fresh=latest&&Date.now()/1000-latest.at<180&&(!isNode||report?.node_online!==false);
  const memory=p=>p.memory_total>0&&p.memory_used!=null?p.memory_used/p.memory_total*100:null;
  const disk=p=>p.disk_total>0&&p.disk_used!=null?p.disk_used/p.disk_total*100:null;
  const pct=v=>v==null?'—':`${v.toLocaleString('ru-RU',{maximumFractionDigits:1})}%`;
  const points=(report?.points||[]).map(p=>({...p,memory:memory(p),disk:disk(p)}));
  const count=isNode?(fresh?latest.online:null):(state.metrics?state.users.filter(isOnline).length:null);
  const sessions=isNode?(fresh?latest.connections:null):state.metrics?.clients.reduce((sum,c)=>sum+c.sessions,0);
  const covered=report?.coverage_24h_seconds||0;
  const total=report?.clients.reduce((sum,c)=>sum+c.count,0);
  const metrics=[['Онлайн сейчас',count==null?'—':number(count),sessions==null?'Нет данных':`${number(sessions)} ${connectionsWord(sessions)}`],
    ['Трафик за 24ч',report&&covered?bytes(report.download_24h+report.upload_24h):'—',covered?`↓ ${bytes(report.download_24h)} · ↑ ${bytes(report.upload_24h)}`:'Нужны два замера'],
    ['Загрузка CPU',pct(fresh?latest.cpu:null),latest?.cores?`${latest.cores} vCPU`:'Нет замера'],
    ['Память',pct(fresh?memory(latest):null),fresh&&latest.memory_total?`${bytes(latest.memory_used)} из ${bytes(latest.memory_total)}`:'Нет замера'],
    ['Диск',pct(fresh?disk(latest):null),fresh&&latest.disk_total?`${bytes(latest.disk_used)} из ${bytes(latest.disk_total)}`:'Нет замера'],
    isNode?['Связь с нодой',(report?.node_online??selectedNode?.online)?'На связи':'Нет связи',latest?'Последний замер: '+new Date(latest.at*1000).toLocaleTimeString('ru-RU'):'Обновите агент ноды для сбора метрик']:['VPN-клиенты',total==null?'—':number(total),'Уникальных токенов за выбранный день']];
  const download=points.reduce((n,p)=>n+(p.download||0),0),upload=points.reduce((n,p)=>n+(p.upload||0),0);
  const resourceCards=[['Загрузка CPU','cpu',pct(fresh?latest.cpu:null),latest?.cores?`${latest.cores} vCPU`:'Нет данных'],['Память','memory',pct(fresh?memory(latest):null),'Без освобождаемого файлового кэша'],['Диск','disk',pct(fresh?disk(latest):null),fresh&&latest.disk_total?`Свободно ${bytes(latest.disk_total-latest.disk_used)}`:'Файловая система панели']];
  return `<section class="strip overview-metrics statistics-metrics">${metrics.map(([label,value,detail])=>`<div class="stat"><p>${label}</p><div class="value">${value}</div><small>${detail}</small></div>`).join('')}</section>
    <div class="statistics-controls">${selector}<div class="statistics-periods" role="group" aria-label="Период статистики">${Object.entries(statisticPeriods).map(([key,label])=>`<button data-statistics-period="${key}" aria-pressed="${state.statisticsPeriod===key}">${label}</button>`).join('')}</div><span class="hint">${isNode?'замеры примерно раз в минуту':'замеры раз в минуту'}</span></div>
    ${!report?'<p class="error">История статистики недоступна. Нажмите «Обновить».</p>':''}
    <div class="split statistics-charts"><section class="panel traffic-chart"><div class="panel-head"><h2>Онлайн сейчас</h2><span class="hint">Уникальные пользователи</span></div>${statisticsChart(points,'online')}</section>
    <section class="panel traffic-chart"><div class="panel-head"><h2>Потребление трафика</h2><span class="hint">↓ ${bytes(download)} · ↑ ${bytes(upload)}</span></div>${statisticsChart(points,'download',{bars:true,second:'upload'})}<div class="chart-legend"><span>Загрузка</span><span>Отдача</span></div></section></div>
    <p class="hint statistics-coverage">За последние 24ч измерено <strong>${Math.floor(covered/3600)} ч ${Math.floor(covered%3600/60)} мин</strong>.${covered<86310?' История неполная: время до установки, перерывы и сбросы счётчиков не восстанавливаются.':''} Данные только выбранной локации. Для метрик ноды обновите её агент; история начнёт накапливаться после обновления.</p>
    <section class="statistics-resources"><div class="panel-head"><h2>Ресурсы сервера</h2>${latest&&!fresh?'<span class="warn">Последний замер устарел</span>':''}</div><div class="statistics-resource-grid">${resourceCards.map(([label,key,value,detail])=>`<section class="panel traffic-chart"><div class="panel-head"><h2>${label}</h2><strong class="statistics-resource-value">${value}</strong></div>${statisticsChart(points,key,{percent:true})}<p class="hint">${detail}</p></section>`).join('')}</div><p class="hint">Графики ресурсов показывают средние значения по интервалам; карточки — последний свежий замер.</p></section>
    ${isNode?'':clientStatistics(report)}
    ${isNode?'':`<details class="overview-live-details"><summary>Скорость VPN и трафик пользователей сейчас</summary><div class="live-speed-details">${liveTrafficChart()}</div>${liveTrafficView()}</details>
    <details class="overview-history"><summary>История расхода по месяцам</summary>${trafficChart()}${trafficView()}</details>
    <details class="overview-diagnostics"><summary>Диагностика сервера</summary>${healthBlock(state.diagnostics)}</details>`}`;
}

async function loadUsageHistory(usage) {
  if (!usage) return [];
  const months = usage.months.filter(month => month < usage.month).sort().slice(-5);
  const prior = await Promise.allSettled(months.map(month => api('/usage?' + new URLSearchParams({month}))));
  return [...prior.filter(r => r.status === 'fulfilled').map(r => r.value), usage];
}

function securityView() {
  return `<div class="split">
    <section class="panel"><div class="panel-head"><h2>Пароль администратора</h2></div>
      <p>После смены пароля потребуется повторный вход в других сеансах панели.</p>
      <div class="dialog-actions"><button class="primary" id="change-password" type="button">Сменить пароль</button></div>
    </section>
    <section class="panel"><div class="panel-head"><h2>Двухфакторная аутентификация</h2></div>
      <div id="factor-settings" aria-live="polite"><p>Загружаем состояние 2FA…</p></div>
    </section>
  </div>`;
}
function flagOf(code){return /^[A-Z]{2}$/.test(code||'')?String.fromCodePoint(...[...code].map(c=>0x1F1E6+c.charCodeAt(0)-65)):'';}
function identityForm(i){if(!i)return '';const name=i.server_name||'',country=i.server_country||'';return `<section class="panel identity"><div class="panel-head"><h2>Название в профилях</h2></div><form id="server-identity"><div class="fields"><label>Название сервера<input name="name" maxlength="64" value="${escapeHTML(name)}" placeholder="${escapeHTML(i.domain||'')}" autocomplete="off"><small>Так сервер называется в клиентах. Пусто — клиенты показывают домен.</small></label><label>Страна<input name="country" maxlength="2" pattern="[A-Za-z]{2}" value="${escapeHTML(country)}" placeholder="NL" autocomplete="off"><small>Код ISO из двух букв — по нему клиенты рисуют флаг ${flagImage(country)}.</small></label></div><p class="hint">Изменение касается новых профилей: выданные ссылки обновятся после повторного импорта.</p><p id="identity-error" class="error" role="alert"></p><div class="dialog-actions"><button type="submit" class="primary">Сохранить</button></div></form></section>`;}
// Канал обновлений: панель показывает опубликованную версию и текущий этап установки.
let updateOffline=false;
// Установку делает обновлятор от root, поэтому здесь только запрос и чтение его отчёта.
// Шаги в том порядке, в каком их проходит netlane-update; ещё не начатые показываются серыми.
const updateSteps=[['requested','Запрос отправлен'],['accepted','Обновление запущено'],['channel','Канал обновлений'],['download','Скачивание комплекта'],['verify','Контрольная сумма'],['unpack','Проверка программ'],['backup','Резервная копия'],['install','Установка'],['restart','Перезапуск служб'],['health','Проверка работы'],['finish','Служебные файлы'],['done','Готово']];
function updateStageText(u){
  const p=u.progress;
  if(p?.stage==='requested')return p.waiting>20?`Запрос отправлен ${p.waiting} с назад, обновление ещё не запустилось`:'Запрос отправлен, ждём запуска обновления';
  if(p?.stage==='done')return (p.version?p.version+' установлена':'обновление установлено')+(p.detail?' · '+p.detail:'');
  if(p?.stage==='failed'){const at=updateSteps.find(([id])=>id===(p.failed_stage||(p.steps||[]).slice(-1)[0]?.stage));return at?`остановилось на шаге «${at[1]}»`:'обновление остановилось';}
  if(p?.message)return p.message+(p.detail?' · '+p.detail:'');
  return 'Обновление запущено';
}
function updatePercent(u){
  if(u.progress&&typeof u.progress.percent==='number')return u.progress.percent;
  return u.progress?.stage==='failed'?100:3;
}
function clock(seconds){return new Date(seconds*1000).toLocaleTimeString('ru-RU',{hour:'2-digit',minute:'2-digit',second:'2-digit'});}
// Список шагов: что сделано, что идёт сейчас, где остановилось.
function updateStepList(u){
  const p=u.progress;if(!p)return '';
  const reported=(p.steps||[]).slice();
  if(p.stage==='requested')reported.push({stage:'requested',message:'Запрос отправлен',detail:p.waiting>0?`${p.waiting} с назад`:'',at:p.at});
  const failed=p.stage==='failed'?(p.failed_stage||reported[reported.length-1]?.stage):'';
  const finished=p.stage==='done';
  const seen=new Map(reported.map((step,i)=>[step.stage,{...step,next:reported[i+1]?.at}]));
  const last=reported[reported.length-1]?.stage;
  // Шаги до первого сообщённого не показываем: ручной запуск по SSH начинается без запроса.
  const firstIndex=Math.max(0,updateSteps.findIndex(([id])=>seen.has(id)));
  const rows=updateSteps.slice(firstIndex).filter(([id])=>!(finished&&!seen.has(id)&&id!=='done')&&!(failed&&!seen.has(id))).map(([id,label])=>{
    const step=seen.get(id);
    let mark='pending';
    if(id==='done'&&finished)mark='done';
    else if(step&&id===failed)mark='failed';
    else if(step&&(id!==last||finished))mark='done';
    else if(step)mark='current';
    const text=step?.message&&step.message!==label?step.message:label;
    const took=step?.next&&step.next-step.at>=1?` · ${step.next-step.at} с`:'';
    return `<li class="update-step is-${mark}"><span class="update-step-mark" aria-hidden="true"></span><span class="update-step-text"><strong>${escapeHTML(text)}</strong>${step?.detail?`<small>${escapeHTML(step.detail)}</small>`:''}</span><span class="update-step-time">${step?.at?escapeHTML(clock(step.at)+took):''}</span></li>`;
  }).join('');
  return `<ol class="update-steps" aria-label="Шаги обновления">${rows}</ol>`;
}
function updateBar(u){
  // Пока панель перезапускается, соединения нет: показываем это, а не замершую полосу.
  const offline=updateOffline&&!(u.progress&&u.progress.stage!=='requested');
  const failed=u.progress?.stage==='failed',done=u.progress?.stage==='done';
  const percent=offline?Math.max(85,updatePercent(u)):Math.max(0,Math.min(100,updatePercent(u)));
  const waiting=u.progress?.stage==='requested'&&u.progress.waiting>30
    ?`<p class="hint">Обновление не запустилось за ${u.progress.waiting} с. На сервере его запускает служба <code>trusttunnel-update</code>; запустите её по SSH: <code>systemctl start trusttunnel-update.service</code>. Журнал: <code>journalctl -u trusttunnel-update -n 50</code>.</p>`
    :'';
  const failure=failed?`<p class="hint">Причина: ${escapeHTML(u.progress.message||'неизвестна')}. Если программы успели поменяться, обновлятор вернул прежнюю версию. Журнал: <code>journalctl -u trusttunnel-update -n 80</code>.</p>`:'';
  return `<div class="update-progress" role="status" aria-live="polite">
    <div class="update-bar${failed?' is-failed':''}${done?' is-done':''}"><span data-css="width:${percent}%"></span></div>
    <p class="update-stage"><strong>${failed?'Не удалось':done?'Готово':percent+'%'}</strong> · ${escapeHTML(offline?'Панель перезапускается, ждём ответа':updateStageText(u))}</p>${updateStepList(u)}${waiting}${failure}</div>`;
}
function updateCard(){
  const u=state.updates;
  if(!u)return '<section class="panel update-panel"><h2>Обновление панели</h2><p class="empty">Проверка обновлений недоступна. Нажмите «Обновить».</p></section>';
  const current=kv('Установлена',u.current||'—');
  const stage=u.progress?.stage;
  if(stage==='failed')return `<section class="panel update-panel"><h2>Обновление панели</h2>${current}${u.latest?kv('Опубликована',u.latest):''}${updateBar(u)}<div class="update-actions"><button class="primary" data-update-apply="${escapeHTML(u.latest||'')}">Повторить обновление</button></div></section>`;
  if(stage==='done'&&!state.updating)return `<section class="panel update-panel"><h2>Обновление панели</h2>${current}${updateBar(u)}</section>`;
  const running=Boolean(u.progress||state.updating);
  if(running)return `<section class="panel update-panel"><h2>Обновление панели</h2>${current}${u.latest?kv('Опубликована',u.latest):''}${updateBar(u)}</section>`;
  if(!u.reachable)return `<section class="panel update-panel"><h2>Обновление панели</h2>${current}<p class="hint">Канал обновлений не ответил: у сервера нет выхода в интернет или GitHub недоступен. Обновление по SSH: <code>netlane-update</code>.</p></section>`;
  if(!u.available)return `<section class="panel update-panel"><h2>Обновление панели</h2>${current}<p class="hint">Опубликована ${escapeHTML(u.latest)} — установлена последняя версия.</p></section>`;
  const notes=u.notes?`<p class="hint">${escapeHTML(u.notes)}</p>`:'';
  const link=u.notes_url?`<p class="hint"><a href="${escapeHTML(u.notes_url)}" target="_blank" rel="noreferrer">Примечания выпуска ${escapeHTML(u.latest)}</a></p>`:'';
  const slow=u.can_apply&&u.instant_start===false?`<p class="hint">Обновление начнётся в течение двух минут после нажатия. Чтобы оно начиналось сразу, один раз выполните по SSH <code>sudo netlane-update --check</code>.</p>`:'';
  const action=u.can_apply?`${slow}<div class="update-actions"><button class="primary" data-update-apply="${escapeHTML(u.latest)}">Обновить до ${escapeHTML(u.latest)}</button></div>`:`<p class="hint">Панель не может установить обновление сама. Один раз выполните по SSH <code>sudo netlane-update --check</code> — он включит запуск обновлений из панели, дальше версии ставятся кнопкой.</p>`;
  return `<section class="panel update-panel"><h2>Обновление панели</h2>${current}${kv('Опубликована',u.latest)}${notes}${link}${action}</section>`;
}
// Ход обновления, начатого в другой вкладке или до перезагрузки страницы: опрашиваем чаще обычного.
setInterval(async()=>{
  const stage=state.updates?.progress?.stage;
  if(!state.csrf||state.updating||document.hidden||!stage||stage==='done'||stage==='failed')return;
  try{const next=await api('/updates');state.updates=next;if(state.view==='server')$('content').innerHTML=serverView();paintVersion();}catch(error){}
},2000);
function serverView(){const i=state.info;return `${identityForm(i)}<div class="split"><section class="panel"><h2>Параметры панели</h2>${kv('Домен',i?.domain??'—')}${kv('Порт панели',i?.panel_listen??'—')}${kv('Метрики',i?.metrics_listen??'—')}${kv('Управляемый endpoint',i?.control_socket?i.control_socket:'не настроен',i?.control_socket?'':'muted')}</section><section class="panel"><h2>Обслуживание</h2><p class="hint">Панель не имеет прав менять DNS, сертификаты и службы: обслуживание выполняется по SSH от root. Порядок и команды описаны в документации установки.</p></section></div>${updateCard()}`;}
const auditLabels={'node.invite_created':'Создано приглашение ноды','node.joined':'Нода подключена','node.deleted':'Нода удалена','subscription.created':'Создана ссылка подписки','subscription.rotated':'Ссылка подписки перевыпущена','subscription.settings_updated':'Правила и DNS подписок обновлены','user.created':'Пользователь создан','user.delete_requested':'Запрошено удаление','user.deleted':'Пользователь удалён','user.policy_updated':'Политика изменена','user.policy_applied':'Политика применена','user.apply_requested':'Запрошено применение','user.disconnect_requested':'Запрошено отключение транспортных соединений','user.profile_exported':'Выдан профиль подключения','auth.login':'Вход администратора','admin.password_changed':'Пароль администратора изменён','admin.2fa_enable':'2FA включена','admin.2fa_disable':'2FA отключена','admin.2fa_reset':'2FA выключена на сервере (reset-2fa)','admin.2fa_rotate':'Резервные коды обновлены','auth.login_failed':'Неудачный вход','panel.update_started':'Запущено обновление панели','server.updated':'Сервер изменён','user.group':'Пользователь перенесён в группу','group.create':'Группа создана','group.rename':'Группа переименована','group.locations':'Локации группы изменены','group.delete':'Группа удалена','node.updated':'Нода изменена'};
// Administrator events (login, password change) record the client address, not a user.
const auditByAddress=a=>a.startsWith('auth.')||a.startsWith('admin.')||a==='panel.update_started';
function auditTarget(e){if(auditByAddress(e.action))return{text:'IP '+e.target,user:''};if(e.action.startsWith('group.')&&e.action!=='group.create'){const group=(state.groups?.groups||[]).find(g=>g.id===e.target);return{text:group?'Группа «'+group.name+'»':e.target,user:''};}if(e.action==='group.create')return{text:'Группа «'+e.target+'»',user:''};if(e.action.startsWith('node.')||e.action==='server.updated'||e.action==='subscription.settings_updated'){const node=(state.nodes?.nodes||[]).find(n=>n.id===e.target);return{text:node?node.name:e.target,user:''};}const name=(state.users.find(u=>u.id===e.target)||{}).username||e.target;return{text:name,user:name};}
// The application that last fetched the user's subscription. A Netlane older than the newest
// version seen among the users is marked: the panel does not ask the client channel itself.
function netlaneVersion(client){const m=/^Netlane (\d+)\.(\d+)\.(\d+)$/.exec(client||'');return m?m.slice(1).map(Number):null;}
function newerVersion(a,b){for(let i=0;i<3;i++){if(a[i]!==b[i])return a[i]>b[i];}return false;}
function newestNetlane(){let best=null;for(const u of state.users){const v=netlaneVersion(u.client);if(v&&(!best||newerVersion(v,best)))best=v;}return best;}
function clientLine(u){if(!u.client)return '';const v=netlaneVersion(u.client),best=newestNetlane();const old=v&&best&&newerVersion(best,v);const when=u.client_day?` · ${new Date(u.client_day+'T00:00:00Z').toLocaleDateString('ru-RU')}`:'';return `<small class="client${old?' outdated':''}" title="Приложение, которое последним забирало подписку${old?'; есть версия '+best.join('.'):''}">${escapeHTML(u.client)}${old?' — устарел':''}${when}</small>`;}
// QR codes come from the panel as SVG data URIs (img-src 'self' data: in the CSP).
function setQR(id,src){const img=$(id);if(!img)return;if(src&&src.startsWith('data:image/svg+xml;base64,')){img.src=src;img.hidden=false;}else{img.removeAttribute('src');img.hidden=true;}}
function auditEvents(){const newest=state.audit?state.audit.events:[];const floor=newest.length?newest[newest.length-1].id:Infinity;return newest.concat((state.auditOlder||[]).filter(e=>e.id<floor));}
function auditMore(){const older=state.auditOlder||[];return older.length?state.auditOlderMore:!!state.audit?.more;}
function auditView(){var ev=auditEvents(),fa=state.auditFilterAction,fu=state.auditFilterUser;var actions=[],users=[];for(var i=0;i<ev.length;i++){var e=ev[i],uname=auditTarget(e).user;if(actions.indexOf(e.action)<0)actions.push(e.action);if(uname&&users.indexOf(uname)<0)users.push(uname);}var list=ev.filter(function(e){var uname=auditTarget(e).user;return (!fa||e.action===fa)&&(!fu||uname===fu);});var html='<div class="toolbar"><label>Действие<select id="audit-action"><option value="">Любое</option>';actions.forEach(function(a){html+='<option value="'+escapeHTML(a)+'"'+(a===fa?' selected':'')+'>'+escapeHTML(auditLabels[a]||a)+'</option>';});html+='</select></label><label>Пользователь<select id="audit-user"><option value="">Любой</option>';users.forEach(function(u){html+='<option value="'+escapeHTML(u)+'"'+(u===fu?' selected':'')+'>'+escapeHTML(u)+'</option>';});html+='</select></label><p class="muted">'+list.length+' событий</p></div><p class="hint">Пароли и профили в журнал не записываются.</p><div class="table-wrap"><table class="audit-table"><thead><tr><th>Время</th><th>Действие</th><th>Администратор</th><th>Объект</th></tr></thead><tbody>';list.forEach(function(e){var uname=auditTarget(e).text;html+='<tr><td class="audit-time">'+escapeHTML(new Date(e.happened_at).toLocaleString('ru-RU'))+'</td><td class="audit-action">'+escapeHTML(auditLabels[e.action]||e.action)+'</td><td>'+escapeHTML(e.actor)+'</td><td class="audit-target">'+escapeHTML(uname)+'</td></tr>';});if(!list.length)html+='<tr><td colspan="4" class="empty">Событий по выбранным фильтрам нет.</td></tr>';html+='</tbody></table></div>';if(auditMore())html+='<div class="table-more"><button type="button" class="secondary" id="audit-more">Показать более ранние</button></div>';return html;}
function hasNodes(){return !!state.nodes?.nodes.length;}
function ago(stamp){if(!stamp)return 'ещё не было';const s=Math.max(0,(Date.now()-Date.parse(stamp))/1000);if(s<60)return 'только что';if(s<3600)return Math.round(s/60)+' мин назад';if(s<86400)return Math.round(s/3600)+' ч назад';return new Date(stamp).toLocaleString('ru-RU');}
function groupsOf(locationID){return (state.groups?.groups||[]).filter(g=>!g.locations.length||g.locations.includes(locationID));}
function nodesView(){const n=state.nodes;if(!n)return '<section class="panel"><h2>Ноды</h2><p class="empty">Список нод недоступен. Нажмите «Обновить».</p></section>';const rows=n.nodes.map(x=>{const synced=x.online&&x.applied_revision===n.users_revision;const icon=flagImage(x.country);return `<tr><td>${icon?icon+' ':''}${escapeHTML(x.name||x.domain)}</td><td>${escapeHTML(x.domain)}</td><td class="${x.online?'ok':'warn'}">${x.online?'На связи':'Нет связи'}<small>${escapeHTML(ago(x.last_seen_at))}</small></td><td class="${synced?'ok':'warn'}">${synced?'Синхронизирована':'Ожидает'}</td><td><div class="loc-chips">${groupsOf(x.id).length?groupsOf(x.id).map(g=>`<span class="loc-chip">${escapeHTML(g.name)}</span>`).join(''):'<span class="muted">—</span>'}</div></td><td>${escapeHTML(x.endpoint_version||'—')}</td><td><div class="actions"><button data-node-groups="${escapeHTML(x.id)}">Группы</button><button data-node-detail="${escapeHTML(x.id)}">Подробнее</button><button data-node-edit="${escapeHTML(x.id)}">Изменить</button><button data-node-delete="${escapeHTML(x.id)}">Удалить</button></div></td></tr>`;}).join('');return `<div class="toolbar"><p class="muted">Главный сервер раздаёт пользователей нодам. Пользователь один и тот же во всех локациях.</p><button id="node-add" class="primary">Добавить ноду</button></div><div class="table-wrap"><table class="node-table"><thead><tr><th>Локация</th><th>Домен</th><th>Связь</th><th>Синхронизация</th><th>Группы</th><th>Версия</th><th class="num">Действия</th></tr></thead><tbody>${rows||'<tr><td colspan="8" class="empty">Нод пока нет. Добавьте VPS в другой стране — пользователи получат его в подписке.</td></tr>'}</tbody></table></div><p class="hint">Пока есть ноды, месячная квота недоступна: каждая нода считает трафик отдельно. Расход в «Обзоре» → «История расхода по месяцам» суммируется по всем локациям.</p>`;}
function groupLocationLabels(locations){if(!locations.length)return '<span class="loc-chip all">Все локации, включая новые</span>';return locations.map(id=>{const node=state.nodes?.nodes?.find(n=>n.id===id);const label=node?flagImage(node.country)+' '+escapeHTML(node.name||node.domain):'Локация удалена';return `<span class="loc-chip">${label}</span>`;}).join('');}
function groupsView(){const data=state.groups;if(!data)return '<section class="panel"><h2>Группы</h2><p class="empty">Список групп недоступен. Нажмите «Обновить».</p></section>';const groups=data.groups||[];const rows=groups.map(g=>`<tr><td class="user-name">${escapeHTML(g.name)}${g.default?'<small>Группа по умолчанию</small>':''}</td><td><div class="loc-chips">${groupLocationLabels(g.locations||[])}</div></td><td class="num" data-label="Участники">${g.members}</td><td><div class="actions"><button data-group-edit="${escapeHTML(g.id)}">Изменить</button>${g.default||g.members>0?'':`<button data-group-delete="${escapeHTML(g.id)}">Удалить</button>`}</div></td></tr>`).join('');return `<div class="toolbar"><p class="muted">Группа определяет, какие локации доступны её участникам. У пользователя одна группа; нода обслуживает участников привязанных к ней групп.</p><button id="group-add" class="primary">Создать группу</button></div><div class="table-wrap"><table class="user-table groups-table"><thead><tr><th>Группа</th><th>Локации</th><th class="num">Участники</th><th class="num">Действия</th></tr></thead><tbody>${rows||'<tr><td colspan="4" class="empty">Групп пока нет. Создайте первую — в неё можно перевести пользователей.</td></tr>'}</tbody></table></div><p class="hint">Группу с участниками удалить нельзя: сначала переведите их. Группа без локаций покрывает всё, включая ноды, добавленные позже.</p>`;}
function openGroupEditor(group=null){const dialog=$('group-editor'),f=$('group-form'),nodes=state.nodes?.nodes||[],assigned=new Set(group?.locations||[]);state.editingGroup=group;$('group-editor-title').textContent=group?`Группа ${group.name}`:'Новая группа';f.elements.name.value=group?.name||'';$('group-locations').innerHTML=nodes.length?nodes.map(n=>`<label class="check"><input type="checkbox" value="${escapeHTML(n.id)}"${assigned.has(n.id)?' checked':''}>${flagImage(n.country)} ${escapeHTML(n.name||n.domain)}</label>`).join(''):'<p class="hint">Нод пока нет. Группа без локаций покрывает всё, включая добавленные позже.</p>';const remove=$('delete-group');remove.hidden=!group;remove.disabled=!group||group.default||group.members>0;remove.title=!group?'':group.default?'Группу по умолчанию удалить нельзя.':group.members>0?`В группе ${group.members} участников — сначала переведите их в другую группу.`:'';$('group-error').textContent='';dialog.showModal();}
// Подвал навигации: установленная версия и, если есть, доступное обновление — ссылкой на «Сервер».
function paintVersion(){
  const box=$('panel-version'),u=state.updates;
  if(!box)return;
  if(!u)return void (box.textContent='');
  if(u.available&&u.latest)box.innerHTML=`Версия ${escapeHTML(u.current||'—')} · <a href="#server">обновление ${escapeHTML(u.latest)}</a>`;
  else box.textContent=u.current?`Версия ${u.current}`:'';
}
function render(){if(!state.status)return;const titles={overview:'Обзор',users:'Пользователи',groups:'Группы',nodes:'Ноды',server:'Сервер',audit:'Журнал',security:'Безопасность',routing:'Маршрутизация'};state.view=Object.hasOwn(titles,location.hash.slice(1))?location.hash.slice(1):'overview';$('page-title').textContent=titles[state.view];$('page-caption').textContent=state.view==='overview'?`Обновлено ${state.loadedAt?.toLocaleTimeString('ru-RU')||'—'} · автообновление каждые 15 с`:`Обновлено ${state.loadedAt?.toLocaleTimeString('ru-RU')||'—'}`;paintVersion();$('create-user').hidden=state.view!=='users';document.querySelectorAll('nav a').forEach(a=>{if(a.hash==='#'+state.view)a.setAttribute('aria-current','page');else a.removeAttribute('aria-current');});if(state.view==='overview')$('content').innerHTML=overviewTabs()+(location.hash==='#overview/network'?networkView():overview());if(state.view==='server')$('content').innerHTML=serverView();if(state.view==='audit')$('content').innerHTML=auditView();if(state.view==='groups')$('content').innerHTML=groupsView();if(state.view==='nodes')$('content').innerHTML=nodesView();if(state.view==='routing'){$('content').innerHTML=sharedRulesPage();loadSharedSettings();}if(state.view==='security'){$('content').innerHTML=securityView();loadFactorStatus();}if(state.view==='users'){if(!$('search'))$('content').innerHTML=`<div class="toolbar"><label><span class="sr-only">Поиск пользователя</span><input id="search" type="search" placeholder="Поиск по имени" aria-label="Поиск по имени"></label><select id="user-filter" aria-label="Доступ"><option value="all">Все пользователи</option><option value="online">В сети</option><option value="enabled">Разрешён доступ</option><option value="disabled">Отключены</option><option value="pending">Не применены</option><option value="expired">Срок истёк</option><option value="exhausted">Квота исчерпана</option></select><select id="user-group" aria-label="Группа"><option value="all">Все группы</option></select><p id="user-count"></p></div><div id="user-list"></div>`;filterUsers();}}
function syncGroupFilter(){const select=$('user-group');if(!select)return;const groups=state.groups?.groups||[];const signature=groups.map(g=>g.id+':'+g.name).join('|');if(select.dataset.signature===signature)return;const current=select.value;select.dataset.signature=signature;select.innerHTML='<option value="all">Все группы</option>'+groups.map(g=>`<option value="${escapeHTML(g.id)}">${escapeHTML(g.name)}</option>`).join('');select.value=groups.some(g=>g.id===current)?current:'all';}
function filterUsers(){syncGroupFilter();const query=$('search').value.toLowerCase(),filter=$('user-filter').value,group=$('user-group')?.value||'all';const users=state.users.filter(u=>u.username.toLowerCase().includes(query)&&(group==='all'||(u.group?.id||'')===group)&&(filter==='all'||filter==='online'&&isOnline(u)||filter==='enabled'&&u.policy.enabled||filter==='disabled'&&!u.policy.enabled||filter==='pending'&&policyStatus(u)[0]==='Не применено'||filter==='expired'&&policyStatus(u)[0]==='Срок истёк'||filter==='exhausted'&&policyStatus(u)[0]==='Квота исчерпана'));$('user-count').textContent=`${users.length} из ${state.users.length}`;$('user-list').innerHTML=userTable(users);}
function refreshView(){
  const expanded=[...$('content').querySelectorAll('.overview-history[open],.overview-diagnostics[open],.overview-live-details[open]')].map(panel=>panel.className);
  render();
  for(const panel of $('content').querySelectorAll('details'))if(expanded.includes(panel.className))panel.open=true;
  fitTrafficCharts();
}
function fitTrafficCharts(){
  for(const svg of document.querySelectorAll('.traffic-chart svg')){
    const width=svg.clientWidth;
    if(width<120)continue;
    const x=value=>80+(Number(value)-74)*(width-98)/528;
    svg.setAttribute('viewBox',`0 0 ${width} 222`);
    for(const node of svg.querySelectorAll('[x],[x1],[x2],[cx],[points],[width]')){
      if(!node.chartGeometry)node.chartGeometry=Object.fromEntries(['x','x1','x2','cx','points','width'].filter(a=>node.hasAttribute(a)).map(a=>[a,node.getAttribute(a)]));
      for(const [key,value] of Object.entries(node.chartGeometry)){
        if(key==='points')node.setAttribute(key,value.split(' ').map(point=>{const [px,py]=point.split(',');return `${x(px)},${py}`;}).join(' '));
        else if(key==='width')node.setAttribute(key,String(Number(value)*(width-98)/528));
        else node.setAttribute(key,Number(value)===0?'0':String(x(value)));
      }
    }
  }
}
new ResizeObserver(fitTrafficCharts).observe($('content'));
document.addEventListener('toggle',fitTrafficCharts,true);
async function load(){if(state.loading||!state.csrf)return;captureSharedDns();state.loading=true;$('refresh').disabled=true;try{const [users,status]=await Promise.all([api('/users'),api('/status')]);const results=await Promise.allSettled([api('/endpoint/control'),api('/endpoint/clients'),api('/audit'),api('/diagnostics'),api('/info'),status.capabilities.durable_quotas?api('/usage?'+new URLSearchParams({month:state.usageMonth})):Promise.resolve(null),api('/nodes'),api('/statistics?'+new URLSearchParams({period:state.statisticsPeriod,day:state.statisticsDay,node:state.statisticsNode})),api('/groups'),api('/updates')]);if(!state.csrf)return;state.users=users.users;state.status=status;[state.control,state.metrics,state.audit,state.diagnostics,state.info,state.usage,state.nodes,state.statistics,state.groups,state.updates]=results.map(r=>r.status==='fulfilled'?r.value:null);if(state.statisticsNode!=='main'&&state.nodes&&!state.nodes.nodes.some(n=>n.id===state.statisticsNode)){state.statisticsNode='main';state.statistics=await api('/statistics?'+new URLSearchParams({period:state.statisticsPeriod,day:state.statisticsDay,node:'main'}));}sampleLiveTraffic(state.metrics);state.usageHistory=await loadUsageHistory(state.usage);if(!state.csrf)return;state.loadedAt=new Date();const errors=results.map((r,i)=>r.status==='rejected'?['Управление endpoint','Метрики','Журнал','Работоспособность','Информация','Учёт расхода','Ноды','Статистика','Группы','Обновление панели'][i]:null).filter(Boolean);$('page-error').textContent=errors.length?`Не удалось обновить: ${errors.join(', ')}. Показатели этих блоков недоступны.`:'';$('page-error').hidden=!errors.length;refreshView();}catch(e){$('page-error').textContent=e.message;$('page-error').hidden=false;}finally{state.loading=false;$('refresh').disabled=false;}}
function openEditor(user=null){state.editing=user;$('delete-user').hidden=!user;$('delete-user').disabled=!state.status?.capabilities.remove_user;$('delete-user').title=state.status?.capabilities.remove_user?'':'Обновите endpoint для удаления пользователей.';$('editor-title').textContent=user?`Пользователь ${user.username}`:'Новый пользователь';const f=$('user-form');f.elements.username.value=user?.username||'';f.elements.username.disabled=!!user;const groupSelect=f.elements.group,groupList=state.groups?.groups||[];groupSelect.innerHTML=groupList.map(g=>`<option value="${escapeHTML(g.id)}">${escapeHTML(g.name)}</option>`).join('')||'<option value="">Нет доступных групп</option>';groupSelect.value=user?.group?.id||state.groups?.default_id||groupList[0]?.id||'';groupSelect.disabled=!groupList.length;f.elements.download.value=user?.policy.download_mbps??0;f.elements.upload.value=user?.policy.upload_mbps??0;f.elements.enabled.checked=user?.policy.enabled??true;f.elements.quota.value=(user?.policy.quota_bytes||0)/1073741824;f.elements.quota.disabled=!state.status?.capabilities.durable_quotas||hasNodes();$('quota-field').hidden=hasNodes()&&!(user?.policy.quota_bytes);$('quota-hint').textContent=f.elements.quota.disabled?'Текущий endpoint ещё не поддерживает месячную квоту.':'0 — без лимита. 1 ГиБ = 1 073 741 824 байта. Изменение лимита не обнуляет расход.';f.elements.expires.value=localDateTime(user?.policy.expires_at);f.elements.expires.disabled=!state.status?.capabilities.expiry;document.querySelectorAll('[data-expiry-days],#clear-expiry').forEach(b=>b.disabled=f.elements.expires.disabled);$('expiry-hint').textContent=f.elements.expires.disabled?'Текущий endpoint ещё не поддерживает срок доступа.':'Время в часовом поясе устройства. Пустое поле — без срока.';$('policy-note').textContent=user?'Расход за текущий месяц сохраняется при изменении политики.':'';$('editor-error').textContent='';$('editor').showModal();}
async function run(button,action,errorId='page-error'){if(state.busy)return;state.busy=true;button.disabled=true;$(errorId).textContent='';$(errorId).hidden=false;try{await action();}catch(e){$(errorId).textContent=e.message;}finally{state.busy=false;button.disabled=false;}}
function showLoginFactor(required) {
  $('login-credentials').hidden = required;
  $('login-factor').hidden = !required;
  const code = $('login-form').elements.code;
  code.required = required;
  if (required) code.focus();
  else code.value = '';
}

$('login-factor-back').addEventListener('click', () => {
  $('login-form').elements.password.value = '';
  $('login-error').textContent = '';
  showLoginFactor(false);
  $('login-form').elements.password.focus();
});

$('login-form').addEventListener('submit', event => {
  event.preventDefault();
  run(event.submitter, async () => {
    const form = event.target;
    const result = await api('/auth/login', {method:'POST',body:JSON.stringify({
      username:form.elements.username.value,password:form.elements.password.value,code:form.elements.code.value
    })});
    if (result.totp_required) {
      showLoginFactor(true);
      return;
    }
    form.elements.password.value = '';
    showLoginFactor(false);
    showWorkspace(result);
    await load();
  }, 'login-error');
});
$('user-form').addEventListener('submit',event=>{event.preventDefault();run(event.submitter,async()=>{const f=event.target,user=state.editing,groupID=f.elements.group.value,policy={...(user?.policy||{quota_bytes:0}),enabled:f.elements.enabled.checked,quota_bytes:quotaFromForm(f,user),download_mbps:Number(f.elements.download.value),upload_mbps:Number(f.elements.upload.value),expires_at:expiryFromForm(f,user)};const result=await api(user?'/users/'+user.id:'/users',{method:user?'PUT':'POST',body:JSON.stringify(user?{expected_revision:user.desired_revision,policy}:{username:f.elements.username.value,policy,group_id:groupID})});state.editing=result.user;f.elements.username.disabled=true;const moved=!!user&&groupID!==(user.group?.id||'');if(moved)await api('/users/'+result.user.id+'/group',{method:'PUT',body:JSON.stringify({group_id:groupID})});try{await api('/users/'+result.user.id+'/apply',{method:'POST'});}catch(e){await load();throw new Error('Настройки сохранены, но не применены. '+e.message);}$('editor').close();notify(moved?'Доступ обновлён, пользователь переведён в другую группу.':'Доступ и скорости подтверждены endpoint.');await load();},'editor-error');});
$('create-user').addEventListener('click',()=>openEditor());$('refresh').addEventListener('click',()=>load());
document.addEventListener('click',event=>{if(event.target.closest('#group-add')){openGroupEditor();return;}const edit=event.target.closest('[data-group-edit]');if(edit){const group=state.groups?.groups.find(g=>g.id===edit.dataset.groupEdit);if(group)openGroupEditor(group);return;}const remove=event.target.closest('[data-group-delete]');if(!remove)return;const group=state.groups?.groups.find(g=>g.id===remove.dataset.groupDelete);if(!group)return;run(remove,async()=>{await api('/groups/'+group.id,{method:'DELETE'});notify(`Группа ${group.name} удалена.`);await load();});});
$('group-form').addEventListener('submit',event=>{event.preventDefault();run(event.submitter,async()=>{const f=event.target,group=state.editingGroup,locations=[...$('group-locations').querySelectorAll('input:checked')].map(input=>input.value);const result=await api(group?'/groups/'+group.id:'/groups',{method:group?'PUT':'POST',body:JSON.stringify({name:f.elements.name.value,locations})});$('group-editor').close();notify(group?`Группа ${result.group.name} сохранена.`:`Группа ${result.group.name} создана.`);await load();},'group-error');});
$('delete-group').addEventListener('click',()=>{const group=state.editingGroup;if(!group||state.busy)return;run($('delete-group'),async()=>{await api('/groups/'+group.id,{method:'DELETE'});$('group-editor').close();notify(`Группа ${group.name} удалена.`);await load();},'group-error');});
document.addEventListener('change',event=>{const box=event.target.closest('[data-select]');if(box){if(box.checked)state.selected.add(box.dataset.select);else state.selected.delete(box.dataset.select);box.closest('tr')?.classList.toggle('is-selected',box.checked);refreshBulkBar();return;}if(event.target.id!=='select-all')return;const boxes=[...$('user-list').querySelectorAll('[data-select]')],on=event.target.checked;boxes.forEach(b=>{b.checked=on;b.closest('tr')?.classList.toggle('is-selected',on);if(on)state.selected.add(b.dataset.select);else state.selected.delete(b.dataset.select);});refreshBulkBar();});
document.addEventListener('click',event=>{const toggle=event.target.closest('[data-menu]');if(toggle){const user=state.users.find(u=>u.id===toggle.dataset.menu);if(user){if(state.rowMenu===user.id){closeRowMenu();state.rowMenu=null;}else{openRowMenu(toggle,user);state.rowMenu=user.id;}}return;}if(event.target.closest('.row-menu')){setTimeout(()=>{closeRowMenu();state.rowMenu=null;},0);return;}closeRowMenu();state.rowMenu=null;});
document.addEventListener('click',event=>{const copy=event.target.closest('[data-menu-subscription]');if(!copy)return;run(copy,async()=>{const result=await api('/users/'+copy.dataset.menuSubscription+'/subscription',{method:'POST',body:JSON.stringify({rotate:false})});await navigator.clipboard.writeText(result.url);notify('Ссылка подписки скопирована в буфер обмена.');});});
document.addEventListener('click',event=>{const button=event.target.closest('[data-bulk]');if(!button||state.busy)return;const ids=[...state.selected];if(!ids.length)return;const kind=button.dataset.bulk;if(kind==='clear'){state.selected.clear();filterUsers();return;}if(kind==='move'){const group=$('bulk-group')?.value;if(!group)return;run(button,async()=>{await api('/users/groups',{method:'POST',body:JSON.stringify({user_ids:ids,group_id:group})});state.selected.clear();notify(`Переведено пользователей: ${ids.length}.`);await load();});return;}if(kind==='delete'){$('bulk-delete-description').textContent=`Будет удалено пользователей: ${ids.length}. Активные сессии завершатся, профили перестанут работать, восстановить доступ нельзя.`;$('bulk-delete-error').textContent='';$('bulk-delete-confirm').showModal();return;}const enable=kind==='unblock';run(button,async()=>{let failed=0;for(const id of ids){const user=state.users.find(u=>u.id===id);if(!user)continue;try{await api('/users/'+id,{method:'PUT',body:JSON.stringify({expected_revision:user.desired_revision,policy:{...user.policy,enabled:enable}})});await api('/users/'+id+'/apply',{method:'POST'});}catch{failed++;}}state.selected.clear();notify(failed?`Готово, но ${failed} из ${ids.length} не удалось: обновите список и повторите.`:`${enable?'Разблокировано':'Заблокировано'} пользователей: ${ids.length}.`);await load();});});
document.addEventListener('keydown',event=>{if(event.key!=='Escape'||!document.querySelector('.row-menu'))return;closeRowMenu();state.rowMenu=null;});
document.addEventListener('click',event=>{const open=event.target.closest('[data-node-groups]');if(!open)return;const node=state.nodes?.nodes.find(n=>n.id===open.dataset.nodeGroups);if(!node)return;state.nodeGroups=node.id;const groups=state.groups?.groups||[];$('node-groups-title').textContent=`Группы ноды ${node.name||node.domain}`;$('node-groups-domain').textContent=node.domain;$('node-groups-list').innerHTML=groups.length?groups.map(g=>{const all=!g.locations.length,attached=all||g.locations.includes(node.id);return `<label class="check"><input type="checkbox" value="${escapeHTML(g.id)}"${attached?' checked':''}${all?' disabled':''}>${escapeHTML(g.name)}${all?' <small class="muted">все локации</small>':''}</label>`;}).join(''):'<p class="hint">Групп пока нет: нода обслуживает всех, пока группы не заданы.</p>';$('node-groups-error').textContent='';$('node-groups').showModal();});
$('node-groups-form').addEventListener('submit',event=>{event.preventDefault();run(event.submitter,async()=>{const nodeID=state.nodeGroups,groups=state.groups?.groups||[];let changed=0;for(const input of $('node-groups-list').querySelectorAll('input:not([disabled])')){const group=groups.find(g=>g.id===input.value);if(!group)continue;const attached=group.locations.includes(nodeID);if(input.checked===attached)continue;const locations=input.checked?[...group.locations,nodeID]:group.locations.filter(id=>id!==nodeID);await api('/groups/'+group.id,{method:'PUT',body:JSON.stringify({locations})});changed++;}$('node-groups').close();notify(changed?`Группы ноды обновлены: ${changed}. Участники получат доступ при следующей синхронизации.`:'Изменений нет: нода уже привязана так же.');await load();},'node-groups-error');});
$('bulk-delete-form').addEventListener('submit',event=>{event.preventDefault();run(event.submitter,async()=>{const ids=[...state.selected];let failed=0;for(const id of ids){const user=state.users.find(u=>u.id===id);if(!user)continue;try{await api('/users/'+id,{method:'DELETE',body:JSON.stringify({expected_revision:user.desired_revision})});}catch{failed++;}}$('bulk-delete-confirm').close();state.selected.clear();notify(failed?`Удалено ${ids.length-failed} из ${ids.length}: остальные не подтвердили отзыв.`:`Удалено пользователей: ${ids.length}.`);await load();},'bulk-delete-error');});
$('delete-user').addEventListener('click',()=>{if(state.busy||!state.editing)return;state.deleting=state.editing;$('delete-description').textContent=`Пользователь ${state.deleting.username} будет удалён из панели и VPN-сервера.`;$('delete-error').textContent='';$('editor').close();$('delete-confirm').showModal();});
$('delete-form').addEventListener('submit',event=>{event.preventDefault();run(event.submitter,async()=>{const user=state.deleting;if(!user)return;await api('/users/'+user.id,{method:'DELETE',body:JSON.stringify({expected_revision:user.desired_revision})});$('delete-confirm').close();state.editing=null;notify(`Пользователь ${user.username} удалён. Доступ отозван на VPN-сервере.`);await load();},'delete-error');});
$('delete-confirm').addEventListener('close',()=>{state.deleting=null;});
function updateTransportHint(){$('transport-hint').textContent=$('profile-form').elements.protocol.value==='http3'?'HTTP/3 — QUIC через UDP/443. Попробуйте при потерях пакетов; если сеть блокирует UDP, выберите HTTP/2.':'HTTP/2 — начните с него: соединение через TCP/443, обычно совместимо с большим числом сетей.';}
$('profile-form').addEventListener('change',()=>{updateTransportHint();state.profileRequest++;state.profile=null;$('profile-result').hidden=true;$('profile-link').value='';setQR('profile-qr','');});
document.addEventListener('click',event=>{if(event.target.id==='usage-more')usagePage(event.target);});
document.addEventListener('submit',event=>{if(event.target.id!=='server-identity')return;event.preventDefault();const f=event.target.elements;run(event.submitter,async()=>{const saved=await api('/server',{method:'PUT',body:JSON.stringify({server_name:f.name.value.trim(),server_country:f.country.value.trim().toUpperCase()})});if(state.info){state.info.server_name=saved.server_name;state.info.server_country=saved.server_country;}render();notify(saved.server_name?`Название «${saved.server_name}» будет в новых профилях.`:'Название убрано: клиенты покажут домен.');},'identity-error');});
document.addEventListener('change',event=>{if(event.target.id==='usage-month'){if(state.busy){event.target.value=state.usage.month;return;}state.usageMonth=event.target.value;load();}});
document.querySelectorAll('[data-expiry-days]').forEach(button=>button.addEventListener('click',()=>{const field=$('user-form').elements.expires;const current=field.value?new Date(field.value).getTime():0;field.value=localDateTime(new Date(Math.max(Date.now(),current)+Number(button.dataset.expiryDays)*86400000).toISOString());}));
$('clear-expiry').addEventListener('click',()=>$('user-form').elements.expires.value='');
async function loadFactorStatus() {
  const target = $('factor-settings');
  if (!target) return;
  try {
    const status = await api('/auth/2fa');
    if (!target.isConnected) return;
    target.innerHTML = status.enabled
      ? `<p class="ok">Включена</p><p class="hint">Осталось резервных кодов: ${number(status.recovery_remaining)}.</p>
         <div class="dialog-actions"><button type="button" data-factor="rotate">Обновить резервные коды</button><button type="button" data-factor="disable" class="danger">Отключить</button></div>`
      : '<p>После пароля потребуется код из приложения-аутентификатора.</p><div class="dialog-actions"><button type="button" data-factor="enable" class="primary">Настроить 2FA</button></div>';
  } catch (error) {
    if (target.isConnected) target.textContent = error.message;
  }
}

let factorAction;
document.addEventListener('click', event => {
  const button = event.target.closest('[data-factor]');
  if (!button) return;
  run(button, async () => {
    const action = button.dataset.factor;
    const setup = action === 'enable' ? await api('/auth/2fa/setup', {method:'POST'}) : null;
    if (state.view !== 'security' || !state.csrf) return;
    factorAction = action;
    $('factor-form').reset();
    $('factor-title').textContent = {enable:'Настройка 2FA',disable:'Отключение 2FA',rotate:'Новые резервные коды'}[action];
    $('factor-setup').hidden = !setup;
    $('factor-secret').value = setup?.secret || '';setQR('factor-qr',setup?.qr);
    $('factor-code-label').textContent = setup ? 'Код из приложения' : 'Код из приложения или резервный код';
    $('factor-error').textContent = '';
    $('factor-dialog').showModal();
  });
});

$('factor-form').addEventListener('submit', event => {
  event.preventDefault();
  run(event.submitter, async () => {
    const form = event.target;
    const result = await api('/auth/2fa', {method:'POST',body:JSON.stringify({
      action:factorAction,secret:$('factor-secret').value,password:form.elements.password.value,code:form.elements.code.value
    })});
    $('factor-dialog').close();
    if (result.recovery_codes?.length) {
      $('factor-codes').textContent = result.recovery_codes.join('\n');
      $('factor-recovery').showModal();
    }
    await loadFactorStatus();
  },'factor-error');
});
$('factor-dialog').addEventListener('close', () => {if (!$('factor-dialog').open) $('factor-form').reset();});
$('factor-recovery').addEventListener('close', () => {if (!$('factor-recovery').open) $('factor-codes').textContent = '';});

document.addEventListener('click',event=>{if(!event.target.closest('#change-password'))return;$('password-form').reset();$('password-error').textContent='';$('password-dialog').showModal();});
$('password-dialog').addEventListener('close',()=>$('password-form').reset());
$('password-form').addEventListener('submit',event=>{event.preventDefault();const form=event.target;run(event.submitter,async()=>{const f=form.elements;if(f.next.value!==f.confirm.value)throw new Error('Новые пароли не совпадают.');await api('/auth/password',{method:'POST',body:JSON.stringify({current_password:f.current.value,new_password:f.next.value})});$('password-dialog').close();await load();notify('Пароль администратора изменён. Остальные сессии панели завершены.');},'password-error');});
$('logout').addEventListener('click',event=>run(event.currentTarget,async()=>{await api('/auth/logout',{method:'POST'});showLogin();}));
document.addEventListener('click',event=>{const close=event.target.closest('[data-close]');if(close){$(close.dataset.close).close();return;}const button=event.target.closest('[data-action]');if(!button)return;const user=state.users.find(u=>u.id===button.dataset.id);if(!user)return;if(button.dataset.action==='edit'){openEditor(user);return;}if(button.dataset.action==='profile'){state.profileUser=user;state.profile=null;$('profile-result').hidden=true;$('profile-link').value='';setQR('profile-qr','');$('profile-error').textContent='';$('profile-user').textContent=user.username;$('profile-server').textContent=state.status.domain;$('profile-form').reset();updateTransportHint();for(const help of $('profile-form').querySelectorAll('details'))help.open=false;$('subscription-result').hidden=true;$('subscription-show').hidden=false;$('subscription-url').value='';setQR('subscription-qr','');$('subscription-rotate').hidden=true;$('subscription-error').textContent='';$('profile').showModal();return;}run(button,async()=>{if(button.dataset.action==='disconnect'){if(!confirm(`Завершить транспортные соединения пользователя ${user.username}? Он сможет подключиться снова.`))return;const result=await api('/users/'+user.id+'/disconnect',{method:'POST'});notify(`Запрошено отключение транспортных соединений: ${result.disconnect_requested}.`);}else if(button.dataset.action==='apply'){await api('/users/'+user.id+'/apply',{method:'POST'});notify('Политика подтверждена endpoint.');}await load();});});
$('profile-form').addEventListener('submit',event=>{event.preventDefault();run(event.submitter,async()=>{state.profile=null;$('profile-result').hidden=true;$('profile-link').value='';setQR('profile-qr','');const f=event.target;const request=++state.profileRequest;const profile=await api('/users/'+state.profileUser.id+'/profile',{method:'POST',body:JSON.stringify({protocol:f.elements.protocol.value,anti_dpi:f.elements.anti_dpi.checked})});if(!$('profile').open||request!==state.profileRequest)return;state.profile=profile;$('profile-link').value=state.profile.deep_link;setQR('profile-qr',state.profile.qr);$('profile-result').hidden=false;},'profile-error');});
$('copy-profile').addEventListener('click',event=>run(event.currentTarget,async()=>{if(!state.profile)return;await navigator.clipboard.writeText(state.profile.deep_link);$('profile-error').textContent='Ссылка скопирована.';},'profile-error'));
$('download-profile').addEventListener('click',()=>{if(!state.profile)return;const url=URL.createObjectURL(new Blob([state.profile.toml],{type:'application/toml'}));const link=document.createElement('a');link.href=url;link.download=state.profile.filename;link.click();setTimeout(()=>URL.revokeObjectURL(url),1000);});
$('profile').addEventListener('close',()=>{state.profileRequest++;state.profile=null;state.profileUser=null;$('profile-link').value='';setQR('profile-qr','');$('profile-result').hidden=true;});
document.addEventListener('input',event=>{if(event.target.id==='search')filterUsers();});document.addEventListener('change',event=>{if(event.target.id==='user-filter'||event.target.id==='user-group')filterUsers();});
document.addEventListener('change',event=>{if(event.target.id==='audit-action'){state.auditFilterAction=event.target.value;render();}if(event.target.id==='audit-user'){state.auditFilterUser=event.target.value;render();}});
window.addEventListener('hashchange',()=>{captureSharedDns();notify('');render();});
setInterval(() => {
  if (!state.csrf || document.hidden || state.busy) return;
  if (nodeDialog.open) {
    if (!nodeDetails.request) loadNodeDetails();
    return;
  }
  if (!document.querySelector('dialog[open]') && state.view !== 'routing') load();
}, 15000);
document.addEventListener('visibilitychange', () => {
  if (!document.hidden && state.csrf && !state.busy && !document.querySelector('dialog[open]') && state.view !== 'routing') load();
});
(async()=>{try{showWorkspace(await api('/auth/session'));await load();}catch{showLogin();}})();

async function subscriptionLink(button,rotate){const user=state.profileUser;if(!user)return;await run(button,async()=>{const result=await api('/users/'+user.id+'/subscription',{method:'POST',body:JSON.stringify({rotate})});if(state.profileUser!==user)return;$('subscription-url').value=result.url;setQR('subscription-qr',result.qr);$('subscription-result').hidden=false;$('subscription-show').hidden=true;$('subscription-rotate').hidden=false;if(!rotate)$('subscription-copy').focus();if(rotate)notify(`Ссылка подписки ${user.username} перевыпущена, старая больше не работает.`);},'subscription-error');}
$('subscription-show').addEventListener('click',event=>subscriptionLink(event.currentTarget,false));
$('subscription-rotate').addEventListener('click',event=>{if(!confirm('Перевыпустить ссылку подписки? Старая ссылка сразу перестанет работать, клиентам нужно будет добавить новую.'))return;subscriptionLink(event.currentTarget,true);});
$('subscription-copy').addEventListener('click',event=>run(event.currentTarget,async()=>{await navigator.clipboard.writeText($('subscription-url').value);$('subscription-error').textContent='Ссылка подписки скопирована.';},'subscription-error'));
$('profile').addEventListener('close',()=>{if($('profile').open)return;state.profileUser=null;$('subscription-url').value='';setQR('subscription-qr','');$('subscription-result').hidden=true;});
document.addEventListener('click',event=>{if(event.target.id==='node-add'){run(event.target,async()=>{const result=await api('/nodes/invite',{method:'POST',body:'{}'});$('node-command').value=result.command;$('node-expires').textContent='Код действует до '+new Date(result.expires_at).toLocaleString('ru-RU')+' и подходит для одной ноды.';$('node-invite-error').textContent='';$('node-invite').showModal();});return;}const del=event.target.closest('[data-node-delete]');if(del){const node=state.nodes?.nodes.find(n=>n.id===del.dataset.nodeDelete);if(!node)return;state.deletingNode=node;$('node-delete-description').textContent=`Нода ${(node.name||node.domain)} (${node.domain}) будет отключена от этого сервера.`;$('node-delete-error').textContent='';$('node-delete').showModal();}});
const nodeDetails = {id:null, tab:'status', data:null, error:'', request:null};
const nodeDialog = $('node-details');

function nodeDate(value) {
  return value ? new Date(value).toLocaleString('ru-RU') : '—';
}

function nodeUserStatus(user) {
  if (user.expected_revision !== user.applied_revision) return ['Ожидает применения','warn'];
  if (user.expected_revision === null) return ['Доступ отсутствует','muted'];
  if (!user.enabled) return ['Отключён','muted'];
  if (user.expires_at && Date.parse(user.expires_at) <= Date.now()) return ['Срок истёк','muted'];
  return ['Доступ разрешён','ok'];
}

function nodeDetailTable(headers, rows, empty) {
  return `<div class="table-wrap"><table><thead><tr>${headers}</tr></thead><tbody>${rows || `<tr><td colspan="3" class="empty">${empty}</td></tr>`}</tbody></table></div>`;
}

function renderNodeDetails() {
  for (const button of nodeDialog.querySelectorAll('[data-node-tab]')) {
    button.setAttribute('aria-pressed', String(button.dataset.nodeTab === nodeDetails.tab));
  }
  const body = $('node-detail-body');
  const details = nodeDetails.data;
  if (!details) {
    body.innerHTML = nodeDetails.error
      ? `<p class="error" role="alert">${escapeHTML(nodeDetails.error)}</p>`
      : '<p class="muted">Загрузка данных…</p>';
    return;
  }
  const {node, users, users_revision: revision, month} = details;
  if (nodeDetails.tab === 'users') {
    const rows = users.filter(u => u.expected_revision !== null || u.applied_revision !== null).map(u => {
      const [label, color] = nodeUserStatus(u);
      return `<tr><td>${escapeHTML(u.username)}</td><td class="${color}">${label}</td><td class="num">${u.applied_revision ?? '—'} / ${u.expected_revision ?? '—'}</td></tr>`;
    }).join('');
    body.innerHTML = '<p class="hint">Ревизии последнего отчёта ноды и настроек, применённых на главном сервере.</p>' +
      nodeDetailTable('<th>Пользователь</th><th>Доступ</th><th class="num">Нода / главный</th>', rows, 'Главный сервер ещё не передал пользователей.');
  } else if (nodeDetails.tab === 'traffic') {
    const active = users.filter(u => u.download_bytes || u.upload_bytes);
    const down = active.reduce((total,u) => total + u.download_bytes, 0);
    const up = active.reduce((total,u) => total + u.upload_bytes, 0);
    const rows = active.map(u => `<tr><td>${escapeHTML(u.username)}</td><td class="num">${bytes(u.download_bytes)}</td><td class="num">${bytes(u.upload_bytes)}</td></tr>`).join('');
    body.innerHTML = `<p class="hint">Последний отчёт ноды за ${escapeHTML(month)} (UTC).</p>
      <dl class="node-detail-facts"><dt>Загрузка</dt><dd>${bytes(down)}</dd><dt>Отдача</dt><dd>${bytes(up)}</dd></dl>` +
      nodeDetailTable('<th>Пользователь</th><th class="num">Загрузка</th><th class="num">Отдача</th>', rows, 'Нода пока не сообщила о расходе за этот месяц.');
  } else {
    const sync = !node.online ? 'Нет свежего отчёта' :
      node.applied_revision === revision ? 'Синхронизирована' : 'Ожидает синхронизации';
    body.innerHTML = `<dl class="node-detail-facts">
      <dt>Связь с главным сервером</dt><dd class="${node.online ? 'ok' : 'warn'}">${node.online ? 'На связи' : 'Нет связи'}</dd>
      <dt>Синхронизация</dt><dd>${sync}</dd>
      <dt>Последний контакт</dt><dd>${escapeHTML(nodeDate(node.last_seen_at))}</dd>
      <dt>Нода добавлена</dt><dd>${escapeHTML(nodeDate(node.created_at))}</dd>
      <dt>Ревизия ноды / главного сервера</dt><dd>${node.applied_revision} / ${revision}</dd>
      <dt>Версия</dt><dd>${escapeHTML(node.endpoint_version || '—')}</dd>
    </dl>`;
  }
}

async function loadNodeDetails() {
  nodeDetails.request?.abort();
  const request = new AbortController();
  nodeDetails.request = request;
  nodeDetails.error = '';
  $('node-detail-refresh').disabled = true;
  renderNodeDetails();
  try {
    const data = await api('/nodes/' + encodeURIComponent(nodeDetails.id), {signal:request.signal});
    if (!request.signal.aborted) nodeDetails.data = data;
  } catch (error) {
    if (!request.signal.aborted) {
      nodeDetails.data = null;
      nodeDetails.error = error.message;
    }
  } finally {
    if (nodeDetails.request === request) nodeDetails.request = null;
    if (!request.signal.aborted) {
      $('node-detail-refresh').disabled = false;
      renderNodeDetails();
    }
  }
}

document.addEventListener('click', event => {
  const open = event.target.closest('[data-node-detail]');
  if (!open) return;
  const node = state.nodes?.nodes.find(n => n.id === open.dataset.nodeDetail);
  if (!node) return;
  nodeDetails.id = node.id;
  nodeDetails.data = null;
  nodeDetails.tab = 'status';
  $('node-details-title').textContent = node.name || node.domain;
  $('node-details-domain').textContent = node.domain;
  nodeDialog.showModal();
  loadNodeDetails();
});
nodeDialog.addEventListener('click', event => {
  const tab = event.target.closest('[data-node-tab]');
  if (!tab) return;
  nodeDetails.tab = tab.dataset.nodeTab;
  renderNodeDetails();
});
nodeDialog.addEventListener('close', () => {
  if (nodeDialog.open) return;
  nodeDetails.request?.abort();
  if (state.csrf && state.view === 'nodes') load();
});
$('node-detail-refresh').addEventListener('click', loadNodeDetails);

$('node-copy').addEventListener('click',event=>run(event.currentTarget,async()=>{await navigator.clipboard.writeText($('node-command').value);$('node-invite-error').textContent='Команда скопирована.';},'node-invite-error'));
$('node-invite').addEventListener('close',()=>{$('node-command').value='';load();});
$('node-delete-form').addEventListener('submit',event=>{event.preventDefault();run(event.submitter,async()=>{const node=state.deletingNode;if(!node)return;await api('/nodes/'+node.id,{method:'DELETE'});$('node-delete').close();notify(`Нода ${node.name||node.domain} удалена.`);await load();},'node-delete-error');});


let sharedSettings = null;
let sharedRuleId = null;
let sharedDirty = false;
let sharedLoading = false;

function sharedRulesPage() {
  return `<section class="panel"><div class="panel-head"><h2>Правила и DNS подписки</h2></div>
    <p>Эти настройки получают все пользователи подписки. Приложение забирает их при запуске и по расписанию подписки; вручную — щелчком по строке аккаунта внизу навигации или кнопкой «Обновить» на странице «Серверы».</p>
    <p class="hint">Пользователь выбирает набор на странице «Подключение» или «Правила». Личные наборы сохраняются. Изменения применяются при следующем подключении.</p>
    <div id="shared-settings-body" aria-live="polite"></div>
    <p id="shared-settings-error" class="error" role="alert"></p>
  </section>`;
}

async function loadSharedSettings() {
  if (sharedLoading) return;
  sharedLoading = true;
  try {
    if (!sharedSettings) sharedSettings = await api('/client-settings');
    if (state.view === 'routing') renderSharedSettings();
  } catch (error) {
    if ($('shared-settings-error')) $('shared-settings-error').textContent = error.message;
  } finally { sharedLoading = false; }
}

function renderSharedSettings() {
  const target = $('shared-settings-body');
  if (!target || !sharedSettings) return;
  const inherited = sharedSettings.dns_servers === null;
  target.innerHTML = `<div class="shared-rules-toolbar"><h3>Наборы правил</h3>
      <button type="button" id="shared-add-rule">Добавить набор</button></div>
    <div class="table-wrap"><table><thead><tr><th>Название</th><th>Маршрут адресов</th><th>Адресов / приложений</th><th>Действия</th></tr></thead>
    <tbody>${sharedSettings.rule_sets.map(set => `<tr><td>${escapeHTML(set.name)}</td>
      <td>${set.routing.Mode==='selective'?'Через VPN':'Напрямую'}</td>
      <td>${set.routing.Destinations.length} / ${set.routing.Applications.length}</td>
      <td><div class="actions"><button type="button" data-shared-edit="${escapeHTML(set.id)}">Изменить</button><button type="button" data-shared-delete="${escapeHTML(set.id)}" class="danger">Удалить</button></div></td></tr>`).join('') || '<tr><td colspan="4" class="empty">Наборы ещё не созданы.</td></tr>'}</tbody></table></div>
    <label class="check shared-dns"><input type="checkbox" id="shared-dns-override" ${inherited?'':'checked'}>Задавать DNS для серверов подписки</label>
    <label>DNS-серверы<textarea id="shared-dns" rows="4" ${inherited?'disabled':''} placeholder="1.1.1.1&#10;tls://1.1.1.1&#10;https://dns.example.com/dns-query">${escapeHTML((sharedSettings.dns_servers || []).join('\n'))}</textarea></label>
    <p class="hint">До 8 адресов, по одному на строку. Пустой список при включённой настройке — DNS движка по умолчанию. При выключенной настройке используются личные DNS пользователя.</p>
    <div class="dialog-actions"><span id="shared-dirty" class="muted">${sharedDirty?'Есть несохранённые изменения':'Ревизия '+sharedSettings.revision}</span><button type="button" id="shared-reload">Загрузить сохранённое</button><button type="button" class="primary" id="shared-save">Сохранить для подписок</button></div>`;
}

function captureSharedDns() {
  if (!sharedSettings || !$('shared-dns')) return;
  sharedSettings.dns_servers = $('shared-dns-override').checked
    ? $('shared-dns').value.split(/\r?\n/).map(s=>s.trim()).filter(Boolean) : null;
}

function markSharedDirty() {
  sharedDirty = true;
  if ($('shared-dirty')) $('shared-dirty').textContent = 'Есть несохранённые изменения';
}

function editSharedRule(id = null) {
  captureSharedDns();
  const rule = sharedSettings?.rule_sets.find(set=>set.id===id);
  sharedRuleId = id;
  const form = $('shared-rule-form');
  form.elements.name.value = rule?.name || '';
  form.elements.mode.value = rule?.routing.Mode || 'selective';
  form.elements.destinations.value = (rule?.routing.Destinations || []).join('\n');
  form.elements.applications.value = (rule?.routing.Applications || []).map(app=>app.Action+' '+app.Name).join('\n');
  $('shared-rule-error').textContent = '';
  $('shared-rule-editor').showModal();
}

document.addEventListener('input', event => {
  if (event.target.id === 'shared-dns') markSharedDirty();
});
document.addEventListener('change', event => {
  if (event.target.id !== 'shared-dns-override') return;
  $('shared-dns').disabled = !event.target.checked;
  markSharedDirty();
});
document.addEventListener('click', event => {
  const button = event.target.closest('button');
  if (!button) return;
  if (button.id==='shared-add-rule') {
    if (sharedSettings.rule_sets.length>=10) { $('shared-settings-error').textContent='Можно создать до 10 наборов.'; return; }
    editSharedRule();
  }
  if (button.dataset.sharedEdit) editSharedRule(button.dataset.sharedEdit);
  if (button.dataset.sharedDelete) {
    if (!confirm('Удалить набор из подписок? В клиентах он исчезнет после обновления подписки.')) return;
    captureSharedDns();
    sharedSettings.rule_sets = sharedSettings.rule_sets.filter(set=>set.id!==button.dataset.sharedDelete);
    markSharedDirty();renderSharedSettings();
  }
  if (button.id==='shared-reload') {
    if (sharedDirty && !confirm('Загрузить настройки с сервера и отменить несохранённые правки?')) return;
    sharedSettings=null;sharedDirty=false;loadSharedSettings();
  }
  if (button.id==='shared-save') run(button,async()=>{
    captureSharedDns();
    const saved=await api('/client-settings',{method:'PUT',body:JSON.stringify(sharedSettings)});
    sharedSettings=saved;sharedDirty=false;renderSharedSettings();notify('Правила и DNS опубликованы в подписках.');
  },'shared-settings-error');
});
$('shared-rule-form').addEventListener('submit',event=>{
  event.preventDefault();
  const form=event.target;
  const apps=form.elements.applications.value.split(/\r?\n/).map(s=>s.trim()).filter(Boolean);
  const applications=[];
  for(const line of apps) {
    const match=/^(vpn|direct)\s+(.+\.exe)$/i.exec(line);
    if(!match){$('shared-rule-error').textContent='Укажите маршрут vpn или direct и имя процесса .exe.';return;}
    applications.push({Action:match[1].toLowerCase(),Name:match[2]});
  }
  const set={id:sharedRuleId || crypto.randomUUID().replaceAll('-',''),name:form.elements.name.value.trim(),routing:{
    Mode:form.elements.mode.value,Destinations:form.elements.destinations.value.split(/\r?\n/).map(s=>s.trim()).filter(Boolean),Applications:applications
  }};
  const index=sharedSettings.rule_sets.findIndex(s=>s.id===set.id);
  if(index<0)sharedSettings.rule_sets.push(set);else sharedSettings.rule_sets[index]=set;
  markSharedDirty();renderSharedSettings();$('shared-rule-editor').close();
});

document.addEventListener('click',event=>{const button=event.target.closest('[data-statistics-period]');if(!button||state.loading||state.busy)return;state.statisticsPeriod=button.dataset.statisticsPeriod;load();});
document.addEventListener('change',event=>{if(event.target.id!=='statistics-day'||state.loading||state.busy)return;state.statisticsDay=event.target.value;load();});

function locationPanel(open,focus){const picker=$('statistics-location-picker'),panel=$('statistics-location-panel'),button=$('statistics-location-button');if(!picker||!panel||!button)return;panel.hidden=!open;button.setAttribute('aria-expanded',String(open));picker.classList.toggle('is-open',open);if(!open){if(focus)button.focus();return;}const selected=[...panel.querySelectorAll('.location-row')].find(r=>r.getAttribute('aria-selected')==='true');(focus&&selected?selected:button).focus();}
document.addEventListener('click',event=>{if(event.target.closest('#statistics-location-button')){locationPanel($('statistics-location-panel').hidden);return;}const row=event.target.closest('[data-statistics-location]');if(row){if(state.loading||state.busy)return;locationPanel(false);if(row.dataset.statisticsLocation!==state.statisticsNode){state.statisticsNode=row.dataset.statisticsLocation;state.statistics=null;load();}return;}if(!event.target.closest('#statistics-location-picker'))locationPanel(false);});
document.addEventListener('keydown',event=>{const panel=$('statistics-location-panel');if(!panel||panel.hidden)return;if(event.key==='Escape'){event.preventDefault();locationPanel(false,true);return;}if(event.key!=='ArrowDown'&&event.key!=='ArrowUp')return;event.preventDefault();const rows=[...panel.querySelectorAll('.location-row')];if(!rows.length)return;const index=rows.indexOf(document.activeElement),step=event.key==='ArrowDown'?1:-1;rows[index<0?(step>0?0:rows.length-1):Math.min(Math.max(index+step,0),rows.length-1)].focus();});
// Запрос обновления: страница показывает этап и процент, а когда панель вернётся новой
// версией — перезагружается.
document.addEventListener('click',event=>{
  const button=event.target.closest('[data-update-apply]');
  if(!button||state.busy||state.updating)return;
  const version=button.dataset.updateApply;
  if(!confirm(`Обновить панель до ${version}? Службы перезапустятся, VPN-соединения прервутся на несколько секунд.`))return;
  state.updating=true;render();
  (async()=>{
    try{await api('/updates/apply',{method:'POST'});}
    catch(error){
      // Панель перезапускается раньше ответа: это не ошибка, обновление уже идёт.
      if(!/соединени/i.test(error.message)){state.updating=false;render();$('page-error').textContent=error.message;$('page-error').hidden=false;return;}
    }
    const from=state.updates?.current;
    for(let i=0;i<300;i++){
      await new Promise(done=>setTimeout(done,2000));
      try{
        const next=await api('/updates');
        state.updates=next;updateOffline=false;
        if(state.view==='server')$('content').innerHTML=serverView();
        paintVersion();
        if(next&&next.current&&next.current!==from){location.reload();return;}
        if(next&&next.progress&&next.progress.stage==='failed'){state.updating=false;render();notify('Обновление не удалось: причина на карточке обновления.');return;}
        if(next&&next.progress&&next.progress.stage==='done'){state.updating=false;render();return;}
      }
      catch(error){
        if(/Сессия/i.test(error.message)){location.reload();return;}
        updateOffline=true;if(state.view==='server')$('content').innerHTML=serverView();paintVersion();
      }
    }
    state.updating=false;render();
    notify('Обновление идёт дольше обычного. Журнал: journalctl -u trusttunnel-admin -n 50');
  })();
});
const chartTip=(()=>{let node=null;const colors=['var(--accent)','#56c6a6'];return{show(hit,x,y){if(!node){node=document.createElement('div');node.className='chart-tip';node.hidden=true;document.body.appendChild(node);}const parts=String(hit.dataset.tip||'').split('|');node.textContent='';const head=document.createElement('div');head.className='chart-tip-time';head.textContent=parts[0]||'';node.appendChild(head);parts.slice(1).forEach((part,index)=>{const[title,value]=part.split('~');const row=document.createElement('div');row.className='chart-tip-row';const swatch=document.createElement('span');swatch.className='chart-tip-swatch';swatch.style.background=colors[index%colors.length];const label=document.createElement('span');label.textContent=title||'';const amount=document.createElement('span');amount.textContent=value||'';row.append(swatch,label,amount);node.appendChild(row);});node.hidden=false;const width=node.offsetWidth,height=node.offsetHeight;node.style.left=Math.max(8,Math.min(x+16,innerWidth-width-8))+'px';node.style.top=Math.max(8,Math.min(y-height/2,innerHeight-height-8))+'px';},hide(){if(node)node.hidden=true;}};})();
document.addEventListener('mousemove',event=>{const svg=event.target instanceof Element?event.target.closest('svg'):null;const hits=svg?svg.querySelectorAll('.chart-hit'):null;if(!hits||!hits.length){chartTip.hide();return;}const box=svg.getBoundingClientRect(),pointer=(event.clientX-box.left)/box.width*620;let hit=null,best=Infinity;for(const candidate of hits){const centre=Number(candidate.getAttribute('x'))+Number(candidate.getAttribute('width'))/2,distance=Math.abs(centre-pointer);if(distance<best){best=distance;hit=candidate;}}const cursor=svg.querySelector('.chart-cursor');if(cursor){const centre=Number(hit.getAttribute('x'))+Number(hit.getAttribute('width'))/2;cursor.setAttribute('x1',centre);cursor.setAttribute('x2',centre);cursor.style.display='';}chartTip.show(hit,event.clientX,event.clientY);});
document.addEventListener('mouseout',event=>{const svg=event.target instanceof Element?event.target.closest('svg'):null;if(!svg||(event.relatedTarget&&svg.contains(event.relatedTarget)))return;const cursor=svg.querySelector('.chart-cursor');if(cursor)cursor.style.display='none';});// Older audit pages stay until the page is reloaded; the regular refresh only replaces the newest page.
document.addEventListener('click',event=>{const button=event.target.closest('#audit-more');if(!button)return;run(button,async()=>{const events=auditEvents();const page=await api('/audit?'+new URLSearchParams({before:events[events.length-1].id}));state.auditOlder=(state.auditOlder||[]).concat(page.events);state.auditOlderMore=page.more;render();});});
// Node name and country: what clients show for the location.
document.addEventListener('click',event=>{const open=event.target.closest('[data-node-edit]');if(!open)return;const node=state.nodes?.nodes.find(n=>n.id===open.dataset.nodeEdit);if(!node)return;state.nodeEdit=node.id;const f=$('node-edit-form').elements;f.name.value=node.name||'';f.country.value=node.country||'';$('node-edit-domain').textContent=node.domain||'';$('node-edit-error').textContent='';$('node-edit').showModal();f.name.focus();});
$('node-edit-form').addEventListener('submit',event=>{event.preventDefault();const f=event.target.elements;run(event.submitter,async()=>{const saved=await api('/nodes/'+encodeURIComponent(state.nodeEdit),{method:'PUT',body:JSON.stringify({name:f.name.value.trim(),country:f.country.value.trim().toUpperCase()})});const list=state.nodes?.nodes||[];const i=list.findIndex(n=>n.id===saved.node.id);if(i>=0)list[i]=Object.assign({},list[i],saved.node);$('node-edit').close();render();notify(`Нода «${saved.node.name||saved.node.domain}» сохранена.`);},'node-edit-error');});
