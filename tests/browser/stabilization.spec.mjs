import {test,expect} from '@playwright/test';
import {spawn} from 'node:child_process';
import {mkdtemp,writeFile,readFile,appendFile,rename,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join,resolve} from 'node:path';
let root,url,server;
const message=text=>JSON.stringify({type:'response_item',payload:{type:'message',role:'assistant',content:text}})+'\n';
test.beforeEach(async({page})=>{
 root=await mkdtemp(join(tmpdir(),'agentview-review-'));
 await writeFile(join(root,'rollout-parent.jsonl'),await readFile('testdata/native-shapes/codex.jsonl'));
 await writeFile(join(root,'rollout-child.jsonl'),JSON.stringify({type:'session_meta',payload:{id:'fixture-child',parent_thread_id:'fixture-parent',agent_path:'/root/review',cwd:'/demo/project'}})+'\n'+message('Child result'));
 await writeFile(join(root,'rollout-long.jsonl'),JSON.stringify({type:'session_meta',payload:{id:'long-history',cwd:'/demo/long-history'}})+'\n'+Array.from({length:5200},(_,i)=>message('History marker '+i)).join(''));
 server=spawn(resolve('bin/agentview'),['serve','--agent','codex','--dir',root,'--include-content','--poll','100ms','--listen','127.0.0.1:0'],{stdio:['ignore','pipe','pipe']});
 url=await new Promise((resolve,reject)=>{let output='';const timer=setTimeout(()=>reject(new Error('startup timeout')),10000);server.once('error',reject);server.stdout.on('data',data=>{output+=data;const match=output.match(/http:\/\/127\.0\.0\.1:\d+\/#token=[A-Za-z0-9]+/);if(match){clearTimeout(timer);resolve(match[0]);}});});
 await page.goto(url);await page.locator('#tour-skip').click();await page.locator('#window').selectOption('all');
});
test.afterEach(async()=>{if(server&&server.exitCode===null)await new Promise(resolve=>{server.once('exit',resolve);server.kill('SIGTERM');});if(root)await rm(root,{recursive:true,force:true});});
async function select(page,q){await page.locator('#search').fill(q);await page.locator('#session-list button').first().click();}

test('real source failure must offer a usable recovery action',async({page})=>{
 await select(page,'long-history');await expect(page.locator('#next-history')).toBeVisible();
 const path=join(root,'rollout-long.jsonl');await rename(path,path+'.away');
 await page.locator('#next-history').click();await expect(page.getByRole('alert')).toContainText('Refresh history');
 await rename(path+'.away',path);
 await expect(page.locator('#next-history')).toHaveCount(0);
 await expect(page.locator('#timeline')).toContainText('History marker 0');
 const response=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/history');await page.locator('#refresh-history').click();
 expect((await response).status()).toBe(200);
 await expect(page.getByRole('alert')).toHaveCount(0);
 await page.locator('#next-history').click();await expect(page.locator('#view-note')).toContainText('page 2');
});

test('spawn payload expansions survive an actual inventory update',async({page})=>{
 await select(page,'PRIVATE_FIXTURE');await page.locator('#agents-tab').click();
 const node=page.locator('.tree-node').last();await node.locator(':scope > summary').click();await node.locator('.tree-inspect > summary').click();
 const card=node.locator('details.tool');await card.locator(':scope > summary').click();await card.locator('.paired-request > summary').click();
 await appendFile(join(root,'rollout-parent.jsonl'),message('FORCE_REVIEW_UPDATE'));
 await expect(page.locator('.agent-tree')).toContainText('FORCE_REVIEW_UPDATE');
 await expect(card).toHaveAttribute('open','');
 await expect(card.locator('.paired-request')).toHaveAttribute('open','');
});

test('a recovered live request clears its connection error',async({page})=>{
 await select(page,'long-history');await expect(page.locator('#next-history')).toBeVisible();
 await page.route('**/api/session?**',route=>route.abort(),{times:1});
 await page.locator('#live-tab').click();await expect(page.locator('#notice')).toContainText('Failed to fetch');
 for(let i=0;i<2;i++)await page.waitForResponse(r=>new URL(r.url()).pathname==='/api/session'&&r.status()===200);
 await expect(page.locator('#notice')).toBeHidden();
});

test('session search links a late failure to full input and the following reply',async({page})=>{
 const request=JSON.stringify({type:'response_item',payload:{type:'function_call',call_id:'late-call',name:'exec_command',arguments:JSON.stringify({cmd:'FULL_INPUT_'+ 'x'.repeat(1500)+'_END_OF_INPUT'})}})+'\n';
 const result=JSON.stringify({type:'response_item',payload:{type:'function_call_output',call_id:'late-call',output:JSON.stringify({exit_code:2,output:'LATE_SEARCH_FAILURE'})}})+'\n';
 await appendFile(join(root,'rollout-long.jsonl'),request+result+message('FOLLOWING_REPLY_CANARY'));
 await select(page,'long-history');await expect(page.locator('#next-history')).toBeVisible();
 await page.locator('#event-type').selectOption('failures');
 await page.locator('#search-session').click();
 const failure=page.locator('details.tool[data-kind="tool.result"]');
 await expect(failure).toHaveCount(1);await failure.locator(':scope > summary').click();
 await expect(failure.locator(':scope > .outcome')).toContainText('failure');
 await expect(page.locator('#view-note')).toContainText('Session search');
 await failure.getByRole('button',{name:'Open full request'}).click();
 const input=page.locator('details.tool[data-kind="tool.requested"]');
 await expect(input).toHaveCount(1);await input.locator(':scope > summary').click();
 await expect(input.locator(':scope > pre')).toContainText('END_OF_INPUT');
 await input.getByRole('button',{name:'Find result',exact:true}).click();
 await failure.locator(':scope > summary').click();
 await failure.getByRole('button',{name:'Find following reply'}).click();
 await expect(page.locator('.event-content')).toHaveText('FOLLOWING_REPLY_CANARY');
 await expect(page.locator('#view-note')).toContainText('not acknowledgement');
 await page.getByRole('button',{name:'Find result late-call',exact:true}).click();
 await expect(failure).toHaveCount(1);
 await page.locator('#clear-session-search').click();
 await expect(page.locator('#timeline')).toContainText('History marker 0');
});

test('pagination keeps keyboard focus during delayed fetch and when returning to page one',async({page})=>{
 await select(page,'long-history');await expect(page.locator('#next-history')).toBeVisible();
 let release;const gate=new Promise(resolve=>{release=resolve;});
 await page.route('**/api/history?**',async route=>{await gate;await route.continue();},{times:1});
 await page.locator('#next-history').focus();await page.keyboard.press('Enter');
 await expect(page.locator('#next-history')).toHaveAttribute('aria-disabled','true');
 await expect(page.locator('#next-history')).toBeFocused();release();
 await expect(page.locator('#view-note')).toContainText('page 2');
 await page.locator('#previous-history').click();
 await expect(page.locator('#view-note')).toContainText('page 1');
 await expect(page.locator('#next-history')).toBeFocused();
});
