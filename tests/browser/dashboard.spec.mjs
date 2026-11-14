import {test, expect} from '@playwright/test';
import {spawn} from 'node:child_process';
import {mkdtemp, writeFile, readFile, appendFile, rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join, resolve} from 'node:path';

let child, root, url;
const message = text => JSON.stringify({type:'response_item',payload:{type:'message',role:'assistant',content:text}})+'\n';
test.beforeAll(async () => {
  root=await mkdtemp(join(tmpdir(),'agentview-browser-'));
  await writeFile(join(root,'rollout-parent.jsonl'),await readFile('testdata/native-shapes/codex.jsonl'));
  await writeFile(join(root,'rollout-child.jsonl'),JSON.stringify({type:'session_meta',payload:{id:'fixture-child',parent_thread_id:'fixture-parent',agent_path:'/root/review',cwd:'/demo/project'}})+'\n'+message('Child investigation result'));
  await writeFile(join(root,'rollout-long.jsonl'),JSON.stringify({type:'session_meta',payload:{id:'long-history',cwd:'/demo/long-history'}})+'\n'+Array.from({length:410},(_,i)=>message(`History marker ${i}`)).join(''));
  child=spawn(resolve('bin/agentview'),['serve','--agent','codex','--dir',root,'--include-content','--poll','100ms','--listen','127.0.0.1:0'],{stdio:['ignore','pipe','pipe']});
  url=await new Promise((resolve,reject)=>{
    let output='';const timer=setTimeout(()=>reject(new Error('Server startup timeout: '+output)),10000);
    child.on('error',error=>{clearTimeout(timer);reject(error);});
    child.on('exit',code=>{clearTimeout(timer);reject(new Error(`Server exit ${code}: ${output}`));});
    child.stderr.on('data',data=>{output+=data;});
    child.stdout.on('data',data=>{output+=data;const match=output.match(/http:\/\/127\.0\.0\.1:\d+\/#token=[A-Za-z0-9]+/);if(match){clearTimeout(timer);resolve(match[0]);}});
  });
});
test.afterAll(async()=>{
  if(child && child.exitCode===null){await new Promise(resolve=>{child.once('exit',resolve);child.kill('SIGTERM');});}
  if(root)await rm(root,{recursive:true,force:true});
});
async function open(page) {
  await page.goto(url);
  await expect(page.locator('#tour-dialog')).toBeVisible();
  await page.locator('#tour-skip').click();
  await expect(page.locator('#connection')).toContainText('Connected');
  await page.locator('#window').selectOption('all');
}
async function select(page,text) {await page.locator('#search').fill(text);await page.locator('#session-list button').first().click();}

test('failure payload, paired request, agent tree, tour and theme are usable',async({page})=>{
  const errors=[];page.on('pageerror',error=>errors.push(error.message));
  await open(page);await select(page,'PRIVATE_FIXTURE');
  await expect(page.locator('#metric-strip')).toContainText('Failure evidence');
  await page.locator('#history-tab').click();
  await page.locator('#event-type').selectOption('failures');
  const result=page.locator('#timeline details.tool').first();await result.locator(':scope > summary').click();
  await expect(result.locator(':scope > .outcome')).toContainText('Outcome: failure · exit 2');
  await result.getByText('Matched input payload',{exact:true}).click();
  await expect(result.locator('.paired-request')).toContainText('exit 2');
  await page.locator('#agents-tab').click();
  await expect(page.locator('.agent-tree')).toContainText('fixture-child');
  await page.locator('.tree-node').last().locator(':scope > summary').click();
  await expect(page.locator('.agent-tree')).toContainText('Recorded spawn request & result');
  await page.locator('.tree-node').last().locator('.tree-inspect > summary').click();
  const link=page.locator('.agent-tree details.tool');await link.locator(':scope > summary').click();
  await expect(link.getByRole('button',{name:'Open linked child →'})).toBeVisible();
  await link.getByRole('button',{name:'Open linked child →'}).click();
  await expect(page.locator('#detail-id')).toHaveText('fixture-child');
  await page.locator('#theme-select').selectOption('dark');
  await expect(page.locator('html')).toHaveAttribute('data-theme','dark');
  await page.locator('#take-tour').click();await expect(page.locator('#tour-dialog')).toBeVisible();
  await page.locator('#tour-payload-shortcut').click();await expect(page.locator('#tour-payload-demo')).toBeVisible();
  expect(errors).toEqual([]);
});

test('history pages reach the final record and live appends arrive',async({page})=>{
  await open(page);await select(page,'long-history');
  await expect(page.locator('#timeline')).toContainText('History marker 0');
  await page.locator('#next-history').focus();await page.keyboard.press('Enter');await expect(page.locator('#view-note')).toContainText('page 2');
  await expect(page.locator('#next-history')).toBeFocused();
  await page.locator('#next-history').click();await expect(page.locator('#timeline')).toContainText('History marker 409');
  await expect(page.locator('#next-history')).toHaveCount(0);
  await expect(page.locator('#previous-history')).toBeFocused();
  await page.getByRole('button',{name:'Previous page'}).click();await expect(page.locator('#view-note')).toContainText('page 2');
  await page.getByRole('button',{name:'Next page',exact:true}).click();await expect(page.locator('#view-note')).toContainText('page 3');
  await page.locator('#live-tab').click();
  await appendFile(join(root,'rollout-long.jsonl'),message('LIVE_BROWSER_CANARY'));
  await expect(page.locator('#timeline')).toContainText('LIVE_BROWSER_CANARY');
  await expect(page.locator('#timeline')).toContainText('Time unknown');
});

test('redacted export downloads structural evidence without private text',async({page})=>{
  await open(page);await select(page,'PRIVATE_FIXTURE');
  const pending=page.waitForEvent('download');await page.locator('#export-investigation').click();
  const download=await pending;const text=await readFile(await download.path(),'utf8');
  expect(text).not.toContain('PRIVATE_FIXTURE');expect(text).not.toContain(root);
  const rows=text.trim().split('\n').map(JSON.parse);
  expect(rows.at(-1)).toMatchObject({type:'coverage',complete:true});
  expect(rows.some(row=>row.outcome?.status==='failure')).toBe(true);
  await expect(page.locator('#notice')).toContainText('Export contains structural evidence');
  for(let i=0;i<2;i++) await page.waitForResponse(r=>new URL(r.url()).pathname==='/api/sessions');
  await expect(page.locator('#notice')).toContainText('Export contains structural evidence');
});


test('failed next page preserves current events, previous navigation and retry',async({page})=>{
  await open(page);await select(page,'long-history');
  await page.locator('#next-history').click();await expect(page.locator('#view-note')).toContainText('page 2');
  const previous=await page.locator('#timeline .event-content').allTextContents();
  await page.route('**/api/history?**',route=>route.fulfill({status:503,body:'Synthetic read failure'}),{times:1});
  await page.locator('#next-history').click();
  await expect(page.getByRole('alert')).toContainText('Synthetic read failure');
  await expect(page.locator('#view-note')).toContainText('page 2');
  expect(await page.locator('#timeline .event-content').allTextContents()).toEqual(previous);
  await expect(page.locator('#previous-history')).toBeEnabled();
  await expect(page.locator('#next-history')).toBeFocused();
  await page.keyboard.press('Enter');await expect(page.locator('#timeline')).toContainText('History marker 409');
  await expect(page.getByRole('alert')).toHaveCount(0);
});


test('Claude string error metadata remains visible in Failures and retry durations are shown',async({page})=>{
  const claudeRoot=await mkdtemp(join(tmpdir(),'agentview-claude-browser-'));
  let process;
  try {
    await writeFile(join(claudeRoot,'session.jsonl'),await readFile('testdata/native-shapes/claude.jsonl'));
    process=spawn(resolve('bin/agentview'),['serve','--agent','claude','--dir',claudeRoot,'--include-content','--poll','100ms','--listen','127.0.0.1:0'],{stdio:['ignore','pipe','pipe']});
    const claudeURL=await new Promise((resolve,reject)=>{
      let output='';const timer=setTimeout(()=>reject(new Error('Claude fixture startup timeout')),10000);
      process.once('error',error=>{clearTimeout(timer);reject(error);});
      process.once('exit',code=>{clearTimeout(timer);reject(new Error('Fixture exited '+code));});
      process.stdout.on('data',data=>{output+=data;const match=output.match(/http:\/\/127\.0\.0\.1:\d+\/#token=[A-Za-z0-9]+/);if(match){clearTimeout(timer);resolve(match[0]);}});
    });
    await page.goto(claudeURL);await page.locator('#tour-skip').click();
    await page.locator('#window').selectOption('all');await select(page,'PRIVATE_FIXTURE native title');
    await page.locator('#event-type').selectOption('failures');
    const result=page.locator('details.tool[data-call-i-d="error-string"]');
    await result.locator(':scope > summary').click();
    await expect(result.locator(':scope > .outcome')).toHaveText('Outcome: failure · native_is_error');
    await expect(result.locator(':scope > pre')).toContainText('PRIVATE_FIXTURE tool response');
    await page.locator('#metrics-tab').click();
    await expect(page.locator('.metric-row').filter({hasText:'Reported API duration excluding retries (ms)'})).toContainText('3,000');
  } finally {
    if(process && process.exitCode===null) await new Promise(resolve=>{process.once('exit',resolve);process.kill('SIGTERM');});
    await rm(claudeRoot,{recursive:true,force:true});
  }
});
