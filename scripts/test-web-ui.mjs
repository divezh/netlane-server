import {pathToFileURL} from 'node:url';
import {readFileSync,mkdirSync} from 'node:fs';
import assert from 'node:assert/strict';
import {spawnSync} from 'node:child_process';
// Optional browser QA driver: pass the zero-dependency CDP module path through the environment.
if(!process.env.TT_BROWSER_DRIVER)throw Error('Set TT_BROWSER_DRIVER to the path of a CDP module exporting Browser and sleep.');
const {Browser,sleep}=await import(pathToFileURL(process.env.TT_BROWSER_DRIVER).href);
const base=process.argv[2]||'http://127.0.0.1:8088';
const live=process.argv.includes('--live');
const password=readFileSync(0,'utf8').trim();
const browser=await Browser.launch();
const page=await browser.newPage();
const username='browser-check-'+Date.now();
const focusUser=()=>page.eval(name=>{const q=document.getElementById('search');q.value=name;q.dispatchEvent(new Event('input',{bubbles:true}));},username);
let created=false;
const poll=async(fn)=>{for(let i=0;i<100;i++){if(await page.eval(fn))return;await sleep(100);}throw Error('Browser state timeout: '+fn.toString());};
mkdirSync('.taste-skill/working-ui',{recursive:true});
try{
 await page.goto(base);
 await page.eval(p=>{const f=document.getElementById('login-form');f.elements.password.value=p;f.requestSubmit(f.querySelector('[type=submit]'));},password);
 await poll(()=>!document.getElementById('workspace').hidden&&!!state.loadedAt);
 await page.eval(()=>document.getElementById('create-user').click());
 const expiry=live&&await page.eval(()=>!!state.status.capabilities.expiry);
 const quotas=live&&await page.eval(()=>!!state.status.capabilities.durable_quotas);
 if(quotas)await page.eval(()=>document.getElementById('user-form').elements.quota.value='1');
 if(expiry)await page.eval(()=>document.querySelector('[data-expiry-days="7"]').click());
 for(const [width,height]of [[1440,900],[768,1024],[390,844]]){await page.setViewport({width,height});await page.screenshot({path:`.taste-skill/working-ui/expiry-editor-${width}.jpg`});}
 await page.setViewport({width:1440,height:900});
 created=true;
 await page.eval(name=>{const f=document.getElementById('user-form');f.elements.username.value=name;f.requestSubmit(f.querySelector('[type=submit]'));},username);
 if(live)await poll(()=>!document.getElementById('editor').open&&!state.busy);else await poll(()=>document.getElementById('editor-error').textContent.includes('сохранены, но не применены')&&!state.busy);
 await page.eval(()=>{document.getElementById('editor').close();location.hash='users';});
 await poll(()=>!!document.getElementById('search'));
 await focusUser();
 assert.equal(await page.eval(()=>document.querySelectorAll('#user-list tbody tr').length),1);
 if(expiry)assert.equal(await page.eval(name=>{const user=state.users.find(u=>u.username===name);const original=Date.now;try{Date.now=()=>Date.parse(user.policy.expires_at)+60000;return policyStatus(user)[0];}finally{Date.now=original;}},username),'Доступ разрешён','client clock overrides confirmed endpoint expiry');
 if(live){
  await page.eval(()=>document.querySelector('#user-list [data-action=profile]').click());
  assert.match(await page.eval(()=>document.getElementById('transport-hint').textContent),/HTTP\/2/);
  assert.match(await page.eval(()=>document.getElementById('anti-dpi-hint').textContent),/глубокого анализа/);
  await page.eval(()=>document.querySelector('.transport-help').open=true);
  for(const [width,height] of [[1440,900],[768,1024],[390,844]]){await page.setViewport({width,height});assert.equal(await page.eval(()=>document.getElementById('profile').scrollWidth<=document.getElementById('profile').clientWidth),true);await page.screenshot({path:`.taste-skill/working-ui/transport-help-${width}.jpg`});}
  await page.setViewport({width:1440,height:900});
  await page.eval(()=>{document.querySelector('.transport-help').open=false;const field=document.getElementById('profile-form').elements.protocol;field.value='http3';field.dispatchEvent(new Event('change',{bubbles:true}));});
  assert.match(await page.eval(()=>document.getElementById('transport-hint').textContent),/UDP\/443/);
  await page.eval(()=>{const f=document.getElementById('profile-form');f.elements.protocol.value='http3';f.elements.anti_dpi.checked=true;f.requestSubmit(f.querySelector('[type=submit]'));});
  await poll(()=>!document.getElementById('profile-result').hidden);
  if(quotas&&process.env.TT_QA_CONTAINER){
   const link=await page.eval(()=>state.profile.deep_link);
   const result=spawnSync('docker',['exec','-i',process.env.TT_QA_CONTAINER,'python3','/src/scripts/test-web-traffic.py'],{input:JSON.stringify({deep_link:link}),encoding:'utf8',timeout:15000});
   assert.equal(result.status,0,result.stderr);assert.equal(result.stdout.trim(),'256');
  }
  assert.equal(await page.eval(()=>document.getElementById('profile-link').value.startsWith('tt://?')&&state.profile.toml.includes('upstream_protocol = "http3"')),true);
  await page.eval(()=>{const field=document.getElementById('profile-form').elements.anti_dpi;field.checked=false;field.dispatchEvent(new Event('change',{bubbles:true}));});
  assert.equal(await page.eval(()=>state.profile===null&&document.getElementById('profile-result').hidden&&document.getElementById('profile-link').value===''),true,'changed settings kept stale profile');
  await page.eval(()=>document.getElementById('profile').close());
  await poll(()=>document.getElementById('profile-link').value===''&&state.profile===null);
  await poll(()=>!state.busy);
  await page.eval(async()=>{
   const original=window.fetch;
   window.fetch=async(...args)=>{const response=await original(...args);if(String(args[0]).endsWith('/profile'))await new Promise(r=>setTimeout(r,250));return response;};
   try{
    document.querySelector('#user-list [data-action=profile]').click();
    const f=document.getElementById('profile-form');f.requestSubmit(f.querySelector('[type=submit]'));
    await new Promise(r=>setTimeout(r,30));document.getElementById('profile').close();
    await new Promise(r=>setTimeout(r,350));
   }finally{window.fetch=original;}
  });
  await poll(()=>!state.busy);
  assert.equal(await page.eval(()=>state.profile===null&&document.getElementById('profile-link').value===''),true,'late profile response survived dialog close');
 }
 if(quotas&&process.env.TT_QA_CONTAINER){
  await page.eval(()=>load());await focusUser();
  assert.equal(await page.eval(name=>state.control.users.find(u=>u.username===name).quota_used_bytes,username),256);
  await page.eval(()=>{document.querySelector('#user-list [data-action=edit]').click();const f=document.getElementById('user-form');f.elements.quota.value=128/1073741824;f.requestSubmit(f.querySelector('[type=submit]'));});
  await poll(()=>!document.getElementById('editor').open&&!state.busy);await focusUser();
  assert.equal(await page.eval(name=>policyStatus(state.users.find(u=>u.username===name))[0],username),'Квота исчерпана');
  await page.eval(()=>{document.querySelector('#user-list [data-action=profile]').click();const f=document.getElementById('profile-form');f.requestSubmit(f.querySelector('[type=submit]'));});
  await poll(()=>document.getElementById('profile-error').textContent.includes('Месячная квота исчерпана'));
  await page.eval(()=>{document.getElementById('profile').close();document.querySelector('#user-list [data-action=edit]').click();const f=document.getElementById('user-form');f.elements.quota.value=512/1073741824;f.requestSubmit(f.querySelector('[type=submit]'));});
  await poll(()=>!document.getElementById('editor').open&&!state.busy);await focusUser();
  assert.equal(await page.eval(name=>state.control.users.find(u=>u.username===name).quota_used_bytes,username),256);
 }
 await focusUser();
 if(expiry){
  await page.eval(()=>document.querySelector('#user-list [data-action=edit]').click());
  await page.eval(()=>{const f=document.getElementById('user-form');f.elements.expires.value=localDateTime(new Date(Date.now()-60000).toISOString());f.requestSubmit(f.querySelector('[type=submit]'));});
  await poll(()=>!document.getElementById('editor').open&&!state.busy);
  assert.equal(await page.eval(name=>policyStatus(state.users.find(u=>u.username===name))[0],username),'Срок истёк');
  await focusUser();
  await page.eval(()=>{document.querySelector('#user-list [data-action=profile]').click();document.getElementById('profile-form').requestSubmit(document.querySelector('#profile-form [type=submit]'));});
  await poll(()=>document.getElementById('profile-error').textContent.includes('Срок доступа истёк'));
  await page.eval(()=>{document.getElementById('profile').close();document.querySelector('#user-list [data-action=edit]').click();document.querySelector('[data-expiry-days="7"]').click();const f=document.getElementById('user-form');f.requestSubmit(f.querySelector('[type=submit]'));});
  await poll(()=>!document.getElementById('editor').open&&!state.busy);
  assert.equal(await page.eval(name=>policyStatus(state.users.find(u=>u.username===name))[0],username),'Доступ разрешён');
 }
 await focusUser();
 await page.eval(()=>document.querySelector('#user-list [data-action=edit]').click());
 await page.eval(()=>{const f=document.getElementById('user-form');f.elements.download.value='7';f.elements.upload.value='3';f.elements.enabled.checked=false;f.requestSubmit(f.querySelector('[type=submit]'));});
 if(live)await poll(()=>!document.getElementById('editor').open&&!state.busy);else await poll(()=>document.getElementById('editor-error').textContent.includes('сохранены, но не применены')&&!state.busy);
 await page.eval(()=>document.getElementById('editor').close());
 await page.goto(base+'/#users');
 await poll(()=>!!document.getElementById('user-list'));
 assert.equal(await page.eval(name=>{const u=state.users.find(u=>u.username===name);return !!u&&!u.policy.enabled&&u.policy.download_mbps===7&&u.policy.upload_mbps===3;},username),true);
 for(const [width,height]of [[1440,900],[768,1024],[390,844]]){
  await page.setViewport({width,height});
  for(const view of ['overview','users','traffic','server','audit']){
   await page.eval(view=>{location.hash=view;},view);await sleep(150);
   assert.equal(await page.eval(()=>document.documentElement.scrollWidth<=innerWidth),true,view+' page overflows at '+width);
   await page.screenshot({path:`.taste-skill/working-ui/${live?'vps':'local'}-${view}-${width}.jpg`});
  }
 }
 assert.deepEqual(page.consoleErrors,[]);
 if(live&&await page.eval(()=>state.status.capabilities.remove_user)){
  await page.eval(()=>location.hash='users');await poll(()=>!!document.getElementById('search'));await focusUser();
  await page.eval(()=>{document.querySelector('#user-list [data-action=edit]').click();document.getElementById('delete-user').click();});
  assert.equal(await page.eval(name=>document.getElementById('delete-description').textContent.includes(name),username),true);
  for(const [width,height] of [[1440,900],[768,1024],[390,844]]){await page.setViewport({width,height});assert.equal(await page.eval(()=>document.getElementById('delete-confirm').scrollWidth<=document.getElementById('delete-confirm').clientWidth),true);await page.screenshot({path:`.taste-skill/working-ui/delete-confirm-${width}.jpg`});}
  await page.eval(()=>document.querySelector('#delete-confirm [data-close]').click());
  assert.equal(await page.eval(name=>state.users.some(u=>u.username===name),username),true,'cancel deleted user');
  await page.eval(()=>{document.querySelector('#user-list [data-action=edit]').click();document.getElementById('delete-user').click();const f=document.getElementById('delete-form');f.requestSubmit(f.querySelector('[type=submit]'));});
  await poll(()=>!document.getElementById('delete-confirm').open&&!state.busy);
  assert.equal(await page.eval(name=>state.users.some(u=>u.username===name)||state.control.users.some(u=>u.username===name),username),false,'deleted user still present');
  assert.equal(await page.eval(()=>state.audit.events.some(e=>e.action==='user.deleted')),true);
  created=false;
 }
 await page.eval(()=>document.getElementById('logout').click());
 await poll(()=>!document.getElementById('login-screen')?.hidden&&document.getElementById('workspace').hidden);
 assert.equal(await page.eval(async()=> (await fetch('/api/v1/users')).status),401);
 console.log('Browser passed: login, create, '+(live?'apply, profile, transport help, stale-profile guard, ':'apply failure, ')+(expiry?'expiry, renewal, ':'')+(quotas?'quota form, ':'')+'edit, disable, session restore, filter, 5 screens × 3 widths, delete confirmation/cancel, logout. QA user '+(created?'disabled: ':'deleted: ')+username);
}catch(error){
 console.error(await page.eval(()=>({editorError:document.getElementById('editor-error').textContent,deleteError:document.getElementById('delete-error').textContent,busy:state.busy,invalidFields:[...document.getElementById('user-form').elements].filter(e=>e.willValidate&&!e.checkValidity()).map(e=>({name:e.name,message:e.validationMessage}))})));
 throw error;
}finally{
 if(created){try{await page.eval(async name=>{if(!state.csrf)return;const u=(await api('/users')).users.find(u=>u.username===name);if(!u)return;if(u.policy.enabled)await api('/users/'+u.id,{method:'PUT',body:JSON.stringify({expected_revision:u.desired_revision,policy:{...u.policy,enabled:false}})});await api('/users/'+u.id+'/apply',{method:'POST'});},username);}catch{console.error('QA cleanup needs checking for '+username);}}
 await browser.close();
}
