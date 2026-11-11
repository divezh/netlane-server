// Browser smoke test of the panel's nodes view and subscription link (Playwright).
// Usage: node scripts/test-web-nodes.mjs <panel URL> <decoy URL> <screenshot dir>  (admin password on stdin)
import {readFileSync,mkdirSync} from 'node:fs';
import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
const {chromium}=createRequire(import.meta.url)('playwright');
const [base,decoy,shots='.taste-skill/nodes']=process.argv.slice(2);
const password=readFileSync(0,'utf8').trim();
mkdirSync(shots,{recursive:true});
const browser=await chromium.launch();
const page=await browser.newPage({viewport:{width:1280,height:800}});
page.on('pageerror',e=>{throw e;});
try{
 await page.goto(base);
 await page.fill('#login-form [name=password]',password);
 await page.click('#login-form [type=submit]');
 await page.waitForFunction(()=>!document.getElementById('workspace').hidden&&state.nodes);
 // A user to open the profile of.
 await page.evaluate(async()=>{await api('/users',{method:'POST',body:JSON.stringify({username:'smoke',password:'smoke-password-123',policy:{enabled:true}})});await load();});
 await page.goto(base+'#nodes');
 await page.waitForSelector('#node-add');
 assert.match(await page.textContent('#content'),/Нод пока нет/);
 await page.click('#node-add');
 await page.waitForSelector('#node-invite[open]');
 const command=await page.inputValue('#node-command');
 assert.match(command,/setup\.sh --join 'https:\/\/[^']+\/node\/join#[A-Za-z0-9_-]+'/);
 await page.screenshot({path:shots+'/invite-1280.png'});
 // Join a node through the decoy server the way setup.sh does.
 const join=command.match(/'([^']+)'/)[1];
 const url=new URL(join);
 const reply=await fetch(decoy+url.pathname,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({code:url.hash.slice(1),name:'Франкфурт',country:'DE',domain:'de.example.com',endpoint_version:'1.0'})});
 assert.equal(reply.status,200);
 const {node_token}=await reply.json();
 const sync=await fetch(decoy+url.pathname.replace('/join','/sync'),{method:'POST',headers:{Authorization:'Bearer '+node_token},body:'{"revision":0}'});
 assert.equal(sync.status,200);
 await page.click('[data-close=node-invite]');
 await page.waitForFunction(()=>state.nodes?.nodes.length===1);
 await page.goto(base+'#nodes');
 await page.waitForSelector('[data-node-delete]');
 const row=await page.textContent('.node-table tbody');
 assert.match(row,/🇩🇪 Франкфурт/);assert.match(row,/На связи/);
 for(const [w,h] of [[1280,800],[390,844]]){await page.setViewportSize({width:w,height:h});await page.screenshot({path:`${shots}/nodes-${w}.png`,fullPage:true});}
 await page.setViewportSize({width:1280,height:800});
 // Users: locations column; editor hides quota.
 await page.goto(base+'#users');
 await page.waitForSelector('#user-list tbody tr');
 assert.match(await page.textContent('.user-table thead'),/Локации/);
 await page.click('#user-list [data-action=edit]');
 assert.equal(await page.isHidden('#quota-field'),true);
 await page.click('[data-close=editor]');
 // Subscription link in the profile dialog.
 await page.click('#user-list [data-action=profile]');
 await page.click('#subscription-show');
 await page.waitForSelector('#subscription-result:not([hidden])');
 const sub=await page.inputValue('#subscription-url');
 assert.match(sub,/^https:\/\/[^/]+\/[a-z2-7]{26}\/sub\/[A-Za-z0-9_-]+$/);
 await page.screenshot({path:shots+'/subscription-1280.png'});
 const feed=await (await fetch(decoy+new URL(sub).pathname)).json();
 assert.equal(feed.version,1);
 await page.click('[data-close=profile]');
 // Delete the node.
 await page.goto(base+'#nodes');
 await page.waitForSelector('[data-node-delete]');
 await page.click('[data-node-delete]');
 await page.click('#node-delete-form [type=submit]');
 await page.waitForFunction(()=>state.nodes?.nodes.length===0);
 const revoked=await fetch(decoy+url.pathname.replace('/join','/sync'),{method:'POST',headers:{Authorization:'Bearer '+node_token},body:'{"revision":0}'});
 assert.equal(revoked.status,401);
 console.log('web nodes smoke: OK');
}finally{await browser.close();}
