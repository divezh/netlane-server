// Local regression check; all API requests are intercepted, no VPS is contacted.
import assert from 'node:assert/strict';
import {readFileSync, mkdirSync} from 'node:fs';
import {fileURLToPath, pathToFileURL} from 'node:url';
import path from 'node:path';

const {chromium} = await import(process.env.TT_PLAYWRIGHT_MODULE
  ? pathToFileURL(process.env.TT_PLAYWRIGHT_MODULE).href : 'playwright-core');
const root = fileURLToPath(new URL('../web/', import.meta.url));
// msedge on Windows; set TT_BROWSER_CHANNEL= (empty) to use the bundled Chromium, as in CI containers.
const channel = process.env.TT_BROWSER_CHANNEL ?? 'msedge';
const browser = await chromium.launch({...(channel ? {channel} : {}), headless:true});
const page = await browser.newPage({viewport:{width:1440,height:1000}});
await page.clock.install({time:new Date('2026-11-05T10:00:00Z')});
const errors = [];
page.on('pageerror', error => errors.push(error.message));
const policy = {enabled:true,download_mbps:0,upload_mbps:0,quota_bytes:0};
const groups = [
  {id:'all',name:'Все локации',created_at:'2026-11-05T09:00:00Z',locations:[],members:1,default:true},
  {id:'work',name:'Работа',created_at:'2026-11-05T09:00:00Z',locations:['wars'],members:1}
];
const users = ['admin','employee'].map((username,i) => ({
  id:`u${i}`,username,policy,desired_revision:1,applied_revision:1,
  group:i===0?{id:'all',name:'Все локации'}:{id:'work',name:'Работа'}
}));
let clients = [{username:'employee',sessions:2,inbound:1,outbound:1}];
let observedAt = Date.parse('2026-11-05T10:00:00Z');
let statisticsMissing=false;
let statisticsEmpty=false;
let statisticsStale=false;
const statisticsQueries=[];
let nodes=[];
function statisticsReport(url){
 if(statisticsEmpty)return {points:[],clients:[],latest:null,coverage_24h_seconds:0,client_day:url.searchParams.get('day')||'2026-11-05'};
 const at=observedAt/1000;
 if(url.searchParams.get('node')==='wars')return {scope:'wars',location:'Warsaw',node_online:nodes[0]?.online,latest:{at,online:4,connections:6,cpu:67,cores:2,memory_total:1024**3,memory_used:512*1024**2,disk_total:20*1024**3,disk_used:4*1024**3},points:[{at,online:4,cpu:67,memory_total:1024**3,memory_used:512*1024**2,disk_total:20*1024**3,disk_used:4*1024**3,download:1024,upload:512}],download_24h:1024,upload_24h:512,coverage_24h_seconds:60,clients:[]};
 const resources={cpu:23,memory_used:860*1024**2,memory_total:2*1024**3,disk_used:8.4*1024**3,disk_total:40*1024**3,cores:2};
 return {observed_at:at,started_at:at-1200,latest:{at:statisticsStale?at-600:at,online:1,...resources},
 points:Array.from({length:20},(_,i)=>({at:at-(19-i)*60,online:i%3,cpu:20+i/5,...resources,download:1024**2,upload:512*1024})),
 download_24h:20*1024**2,upload_24h:10*1024**2,coverage_24h_seconds:1200,scope:'main',sample_interval_seconds:60,
 clients:[{name:'Netlane',count:1}],client_day:url.searchParams.get('day')||'2026-11-05'};
}
const routes = {
  '/statistics': url => statisticsReport(url),
  '/auth/session': () => ({username:'admin',csrf_token:'test'}),
  '/users': () => ({users}),
  '/status': () => ({domain:'vpn.example',capabilities:{durable_quotas:true}}),
  '/endpoint/control': () => ({users:users.map(u=>({...u,...policy,revision:1}))}),
  '/endpoint/clients': () => ({clients,observed_at:new Date(observedAt).toISOString()}),
  '/audit': () => ({events:[]}),
  '/diagnostics': () => ({verdict:'ok',checks:[]}),
  '/info': () => ({server_name:'Test'}),
  '/nodes': () => ({nodes,users_revision:1}),
  '/groups': () => ({groups,default_id:'all'}),
  '/updates': () => ({current:'0.5.16',can_apply:false,reachable:true,latest:'0.5.16',available:false}),
  '/usage': () => ({month:'2026-11',months:['2026-11'],totals:{charged_bytes:2,
    download_bytes:1,upload_bytes:1},main_bytes:2,
    records:users.map(u=>({user_id:u.id,charged_bytes:1}))})
};
await page.route('**/*', async route => {
  const url = new URL(route.request().url());
  if (url.pathname.startsWith('/api/v1')) {
    const key = url.pathname.slice('/api/v1'.length);
    if(key==='/statistics'){
      statisticsQueries.push(url.searchParams.toString());
      if(statisticsMissing)return route.fulfill({status:503,json:{error:'statistics_unavailable'}});
    }
    if (key === '/endpoint/clients' && clients === null) {
      return route.fulfill({status:503,json:{error:'endpoint_metrics_unavailable'}});
    }
    assert.ok(routes[key], `Unexpected API request: ${key}`);
    return route.fulfill({json:routes[key](url)});
  }
  const relative = url.pathname === '/' ? 'index.html' : url.pathname.slice(1);
  const file = path.resolve(root,relative);
  assert.ok(file.startsWith(root), 'Static path outside web directory');
  const types = {'.html':'text/html','.js':'text/javascript','.css':'text/css',
    '.png':'image/png','.ico':'image/x-icon','.woff2':'font/woff2'};
  return route.fulfill({body:readFileSync(file),contentType:types[path.extname(file)]});
});

async function check(expectedUsers, expectedSessions, dots) {
  const card = page.locator('.overview-metrics .stat').first();
  assert.equal(await card.locator('p').textContent(),'Онлайн сейчас');
  assert.equal(await card.locator('.value').textContent(),expectedUsers);
  assert.equal(await card.locator('small').textContent(),expectedSessions);
  assert.equal(await page.locator('.live-traffic-table .user-online-dot').count(),dots);
}
try {
  await page.goto('http://127.0.0.1:18749/');
  await page.locator('.overview-metrics').waitFor();
  await check('1','2 транспортных соединения',1);
  const output = fileURLToPath(new URL('../artifacts/',import.meta.url));
  mkdirSync(output,{recursive:true});
  await page.screenshot({path:path.join(output,'overview-online-users.png'),fullPage:true});
  // A fixed sidebar follows the viewport, not Playwright's stitched full-page canvas.
  // Render the whole document in one tall viewport for the README illustration.
  if(process.env.TT_CAPTURE_README==='1'){
    const viewport=page.viewportSize();
    const height=await page.evaluate(()=>document.documentElement.scrollHeight);
    await page.setViewportSize({width:viewport.width,height});
    await page.evaluate(()=>scrollTo(0,0));
    const sidebar=await page.locator('aside').boundingBox();
    assert.ok(Math.abs(sidebar.y)<1&&Math.abs(sidebar.height-height)<1,'README sidebar must fill the captured viewport');
    await page.screenshot({path:path.join(output,'overview-readme.png'),fullPage:false});
    await page.setViewportSize(viewport);
  }

  // Multiple metric rows for one username must still count as one person.
  clients = [{username:'employee',sessions:1},{username:'employee',sessions:1}];
  await page.evaluate(()=>load());
  await check('1','2 транспортных соединения',1);

  clients = [{username:'employee',sessions:2},{username:'admin',sessions:1}];
  await page.evaluate(()=>load());
  await check('2','3 транспортных соединения',2);

  // Raw endpoint sessions can include an account absent from the panel.
  clients = [{username:'employee',sessions:1},{username:'deleted',sessions:3}];
  await page.evaluate(()=>load());
  await check('1','4 транспортных соединения',1);

  clients = [];
  await page.evaluate(()=>load());
  await check('0','0 транспортных соединений',0);

  clients = null;
  await page.evaluate(()=>load());
  await check('—','Нет данных',0);

  // Live rates use counter deltas, not monthly totals. Exercise the actual timer.
  await page.evaluate(()=>resetLiveTraffic());
  clients = [{username:'employee',sessions:2,inbound:0,outbound:0}];
  await page.evaluate(()=>load());
  assert.equal(await page.locator('.live-rates strong').first().textContent(),'—');
  async function tick() {
    observedAt += 15000;
    await page.clock.runFor(15000);
    for(let i=0;i<100;i++){
      if(await page.evaluate(at=>!state.loading&&liveTraffic.previous?.at===at,observedAt))return;
      await new Promise(resolve=>setTimeout(resolve,20));
    }
    throw Error('Auto refresh did not complete');
  }
  clients = [{username:'employee',sessions:2,inbound:15360,outbound:30720}];
  await tick();
  assert.deepEqual(await page.evaluate(()=>liveTraffic.current),{download:2048,upload:1024});
  assert.match(await page.locator('.live-rates strong').first().textContent(),/2,0 КиБ\/с/);
  assert.equal(await page.locator('.live-speed-details .traffic-chart').locator('svg circle').count(),2);
  assert.match(await page.locator('#page-caption').textContent(),/автообновление каждые 15 с/);
  await page.screenshot({path:path.join(output,'overview-live-traffic.png'),fullPage:true});
  for(const width of [2560,1920,1440,768,390]){
    await page.setViewportSize({width,height:1000});
    const geometry=await page.evaluate(()=>{
      const main=document.querySelector('main'),strip=document.querySelector('.overview-metrics');
      return {viewport:innerWidth,right:main.getBoundingClientRect().right,
        contentRight:strip.getBoundingClientRect().right,padding:parseFloat(getComputedStyle(main).paddingRight),
        overflow:document.documentElement.scrollWidth>innerWidth};
    });
    assert.ok(Math.abs(geometry.right-width)<2,`main does not fill viewport at ${width}`);
    assert.ok(Math.abs(geometry.contentRight-(width-geometry.padding))<2,`content capped at ${width}`);
    assert.equal(geometry.overflow,false,`page overflow at ${width}`);
    if(width===2560||width===390)await page.screenshot({path:path.join(output,`overview-live-${width}.png`),fullPage:true});
  }
  await page.setViewportSize({width:1440,height:1000});
  await page.locator('.overview-history summary').click();
  await tick();
  assert.equal(await page.locator('.overview-history').evaluate(el=>el.open),true);

  await page.locator('[data-statistics-period="6h"]').click();
  for(let i=0;i<100;i++){if(await page.evaluate(()=>!state.loading&&state.statisticsPeriod==='6h'))break;await new Promise(r=>setTimeout(r,20));}
  assert.equal(await page.locator('[data-statistics-period="6h"]').getAttribute('aria-pressed'),'true');
  assert.ok(statisticsQueries.some(q=>q.includes('period=6h')));
  await page.locator('#statistics-day').fill('2026-11-04');
  await page.locator('#statistics-day').dispatchEvent('change');
  for(let i=0;i<100;i++){if(await page.evaluate(()=>!state.loading&&state.statisticsDay==='2026-11-04'))break;await new Promise(r=>setTimeout(r,20));}
  assert.ok(statisticsQueries.some(q=>q.includes('day=2026-11-04')));
  statisticsMissing=true;await page.evaluate(()=>load());
  assert.match(await page.locator('#content').textContent(),/История статистики недоступна/);
  assert.equal(await page.locator('.statistics-metrics .stat').nth(2).locator('.value').textContent(),'—');
  statisticsMissing=false;await page.evaluate(()=>load());
  statisticsEmpty=true;await page.evaluate(()=>load());
  assert.match(await page.locator('.statistics-charts').textContent(),/История ещё не накоплена/);
  assert.equal(await page.locator('.statistics-metrics .stat').nth(1).locator('.value').textContent(),'—');
  assert.equal(await page.locator('.client-ring strong').textContent(),'0');
  statisticsEmpty=false;statisticsStale=true;await page.evaluate(()=>load());
  assert.equal(await page.locator('.statistics-metrics .stat').nth(2).locator('.value').textContent(),'—');
  assert.match(await page.locator('.statistics-resources').textContent(),/Последний замер устарел/);
  statisticsStale=false;await page.evaluate(()=>load());
  const accent=await page.evaluate(()=>getComputedStyle(document.documentElement).getPropertyValue('--accent').trim());
  assert.equal(accent,'#22d3e6');

  // Location selection must never display main's metrics as the node's metrics.
  nodes=[{id:'wars',name:'Warsaw',domain:'wars.example.com',online:true}];
  await page.evaluate(()=>load());
  // The location picker is a button with a popover, not a select.
  await page.locator('#statistics-location-button').click();
  await page.locator('[data-statistics-location="wars"]').click();
  for(let i=0;i<100;i++){if(await page.evaluate(()=>!state.loading&&state.statistics?.scope==='wars'))break;await new Promise(r=>setTimeout(r,20));}
  assert.equal(await page.locator('.statistics-metrics .stat').first().locator('.value').textContent(),'4');
  assert.equal(await page.locator('.statistics-metrics .stat').nth(2).locator('.value').textContent(),'67%');
  assert.equal(await page.locator('.client-statistics').count(),0);
  assert.equal(await page.locator('.overview-live-details').count(),0);
  assert.match(await page.locator('.statistics-controls').textContent(),/Warsaw/);
  await page.evaluate(()=>scrollTo(0,0));
  await page.screenshot({path:path.join(output,'overview-node-warsaw.png'),fullPage:true});
  await page.setViewportSize({width:390,height:844});
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
  nodes[0].online=false;await page.evaluate(()=>load());
  assert.equal(await page.locator('.statistics-metrics .stat').first().locator('.value').textContent(),'—');
  nodes=[];await page.evaluate(()=>load());
  assert.equal(await page.evaluate(()=>state.statisticsNode),'main');
  await page.setViewportSize({width:1440,height:1000});

  // Auto refresh must preserve the users toolbar, focus, query and filter.
  await page.evaluate(()=>{location.hash='users';render();});
  await page.locator('#search').fill('employee');
  await page.locator('#user-filter').selectOption('online');
  await page.locator('#search').focus();
  clients = [{username:'employee',sessions:1,inbound:30720,outbound:61440}];
  await tick();
  assert.equal(await page.locator('#search').inputValue(),'employee');
  assert.equal(await page.locator('#user-filter').inputValue(),'online');
  assert.equal(await page.evaluate(()=>document.activeElement.id),'search');
  assert.match(await page.locator('#user-list').textContent(),/1 транспортное соединение/);

  // A counter reset is a gap, never a negative speed or a fabricated spike.
  clients = [{username:'employee',sessions:1,inbound:0,outbound:0}];
  await tick();
  assert.equal(await page.evaluate(()=>liveTraffic.current),null);
  clients = [{username:'employee',sessions:1,inbound:0,outbound:0}];
  await tick();
  assert.deepEqual(await page.evaluate(()=>liveTraffic.current),{download:0,upload:0});
  // Missing metrics and long sampling gaps also require a fresh baseline.
  await page.evaluate(()=>sampleLiveTraffic(null));
  assert.equal(await page.evaluate(()=>liveTraffic.current),null);
  await page.evaluate(()=>sampleLiveTraffic({clients:[],observed_at:new Date(Date.now()+120000).toISOString()}));
  assert.equal(await page.evaluate(()=>liveTraffic.current),null);
  // A node is attached to groups from its own row; an unrestricted group cannot be excluded.
  nodes=[{id:'wars',name:'Warsaw',domain:'wars.example.com',online:true}];
  await page.evaluate(()=>load());
  await page.evaluate(()=>{location.hash='nodes';render();});
  await page.locator('[data-node-groups]').first().click();
  await page.locator('#node-groups-list input').first().waitFor();
  assert.equal(await page.locator('#node-groups-list input').count(),2);
  assert.equal(await page.locator('#node-groups-list input').nth(0).isChecked(),true);
  assert.equal(await page.locator('#node-groups-list input').nth(0).isDisabled(),true,'a group without locations covers every node and cannot be unchecked');
  assert.equal(await page.locator('#node-groups-list input').nth(1).isChecked(),true);
  assert.equal(await page.locator('#node-groups-list input').nth(1).isDisabled(),false);
  await page.locator('#node-groups button[data-close]').first().click();  // Groups: the page lists them, the badge follows the user, the filter narrows the list.
  await page.evaluate(()=>{location.hash='groups';render();});
  await page.locator('.groups-table').waitFor();
  assert.equal(await page.locator('.groups-table tbody tr').count(),2);
  assert.match(await page.locator('.groups-table').textContent(),/Работа/);
  assert.equal(await page.locator('[data-group-delete]').count(),0,'default group and group with members must not offer deletion');
  await page.evaluate(()=>{location.hash='users';render();});
  await page.locator('#user-group').selectOption('work');
  assert.equal(await page.locator('#user-list tbody tr').count(),1);
  assert.match(await page.locator('#user-list').textContent(),/Работа/);
  await page.locator('#user-group').selectOption('all');
  // The row menu lives in the body: inside the table its own overflow would clip it.
  await page.locator('[data-menu]').first().click();
  assert.equal(await page.locator('body > .row-menu').count(),1);
  assert.equal(await page.locator('.row-menu button:not([disabled])').count(),3);
  assert.equal(await page.locator('.row-menu button[disabled]').count(),1);
  await page.keyboard.press('Escape');
  assert.equal(await page.locator('.row-menu').count(),0);
  // Selection reveals the bulk bar and the header box reports the mixed state.
  await page.locator('[data-select]').first().click();
  assert.match(await page.locator('.bulk-bar').textContent(),/Выбрано 1/);
  assert.equal(await page.locator('#select-all').evaluate(el=>el.indeterminate),true);
  await page.locator('[data-bulk="clear"]').click();
  assert.equal(await page.locator('.bulk-bar').count(),0);
  assert.deepEqual(errors,[]);  console.log('PASS: online users, transport connections, real timer refresh, live rates, counter resets, preserved filters and focus, groups and bulk selection');
} finally {
  await browser.close();
}
