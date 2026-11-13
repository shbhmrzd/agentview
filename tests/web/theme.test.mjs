import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';

const script = readFileSync(new URL('../../internal/dashboard/web/theme.js', import.meta.url), 'utf8');
const paletteCSS = readFileSync(new URL('../../internal/dashboard/web/themes.css', import.meta.url), 'utf8');
const styles = readFileSync(new URL('../../internal/dashboard/web/style.css', import.meta.url), 'utf8');
function boot({saved = null, dark = false, blocked = false} = {}) {
  const values = new Map([['agentview-theme-v1', saved]]), handlers = {}, windowEvents = {};
  const select = {value:'', options:[], append(option){this.options.push(option);}, addEventListener(name, fn){handlers[name] = fn;}};
  const document = {documentElement:{dataset:{}}, createElement:() => ({}), getElementById:() => select, addEventListener(name,fn){handlers[name] = fn;}};
  const media = {matches:dark, addEventListener(name,fn){this.change = fn;}};
  const localStorage = {getItem:key => {if(blocked) throw Error('blocked'); return values.get(key);}, setItem:(key,value) => {if(blocked) throw Error('blocked'); values.set(key,value);}};
  vm.runInNewContext(script, {window:{matchMedia:() => media, addEventListener(name,fn){windowEvents[name] = fn;}}, document, localStorage});
  return {document, select, values, media, handlers, windowEvents};
}

test('theme applies before DOM ready, uses OS default, and remembers explicit choice', () => {
  const page = boot({dark:true});
  assert.equal(page.document.documentElement.dataset.theme, 'dark');
  page.handlers.DOMContentLoaded();
  assert.deepEqual(page.select.options.map(x=>x.value), ['light','dark']);
  page.select.value = 'light'; page.handlers.change();
  assert.equal(page.values.get('agentview-theme-v1'), 'light');
  page.media.change();
  assert.equal(page.document.documentElement.dataset.theme, 'light');
  assert.equal(boot({saved:'light',dark:true}).document.documentElement.dataset.theme, 'light');
});

test('unknown preferences fall back; blocked storage does not break theme selection', () => {
  const page = boot({saved:'unknown', blocked:true});
  page.handlers.DOMContentLoaded();
  page.media.matches = true; page.media.change();
  assert.equal(page.document.documentElement.dataset.theme, 'dark');
  page.select.value = 'light'; page.handlers.change();
  assert.equal(page.document.documentElement.dataset.theme, 'light');
  page.select.value = 'unregistered-theme'; page.handlers.change();
  assert.equal(page.document.documentElement.dataset.theme, 'light');
});

test('theme choice synchronizes across tabs and removing it restores OS preference', () => {
  const page = boot({saved:'dark'});
  page.handlers.DOMContentLoaded();
  page.values.set('agentview-theme-v1', 'light'); page.windowEvents.storage({key:'agentview-theme-v1'});
  assert.equal(page.select.value, 'light');
  page.values.delete('agentview-theme-v1'); page.media.matches = true; page.windowEvents.storage({key:null});
  assert.equal(page.select.value, 'dark');
});

test('both palettes implement every component token with readable text contrast', () => {
  assert.doesNotMatch(styles, /#[\da-f]{3,8}\b/i, 'component colors must use theme tokens');
  const tokens = new Set([...styles.matchAll(/var\(--([\w-]+)\)/g)].map(x=>x[1]));
  tokens.delete('mono');
  const luminance = color => {
    const rgb = color.slice(1).match(/../g).map(x=>parseInt(x,16)/255).map(x=>x<=0.04045?x/12.92:((x+0.055)/1.055)**2.4);
    return rgb[0]*0.2126 + rgb[1]*0.7152 + rgb[2]*0.0722;
  };
  const palettes = [...paletteCSS.matchAll(/:root\[data-theme="(light|dark)"\]\s*\{([^}]+)\}/g)];
  assert.equal(palettes.length, 2);
  for (const [,name,body] of palettes) {
    const palette = Object.fromEntries([...body.matchAll(/--([\w-]+):\s*(#[\da-f]+);/g)].map(x=>[x[1],x[2]]));
    for (const token of tokens) assert.ok(palette[token], `${name} missing ${token}`);
    for (const [text, background] of [['ink','surface'],['muted','surface'],['muted','wash'],['accent','surface'],['on-primary','primary'],['success','success-bg'],['warning','warning-bg']]) {
      const levels = [luminance(palette[text]), luminance(palette[background])].sort((a,b)=>a-b);
      assert.ok((levels[1]+0.05)/(levels[0]+0.05)>=4.5, `${name}: ${text} on ${background}`);
    }
  }
});
