// Deliberately small test DOM, not a browser/layout emulator. No HTML parser:
// text and properties are observable, while layout, CSP and paint need a browser.
class FakeNode {
 constructor(tag='',text=''){this.tagName=tag.toUpperCase();this.data=text;this.children=[];this.parentNode=null;this.dataset={};this.style={};this.attributes={};this.className='';this.value='';this.disabled=false;this.open=false;this.scrollTop=0;this.scrollHeight=600;this.clientHeight=400;this.clientWidth=800;this.scrollWidth=0;this.type='';this.id='';this.selectionStart=0;this.selectionEnd=0;this.classList={contains:c=>this.className.split(' ').includes(c),add:(...c)=>{this.className=[this.className,...c].join(' ');},remove:c=>{this.className=this.className.split(' ').filter(x=>x!==c).join(' ');}};}
 append(...nodes){for(let n of nodes){if(typeof n==='string')n=new FakeNode('',n);if(n.parentNode)n.remove();n.parentNode=this;this.children.push(n);}}
 addEventListener(type,fn){this['on'+type]=fn;}
 appendChild(n){this.append(n);return n;}
 insertBefore(n,c){if(n===c)return n;if(n.parentNode)n.remove();n.parentNode=this;const i=c?this.children.indexOf(c):-1;if(i<0)this.children.push(n);else this.children.splice(i,0,n);return n;}
 removeChild(n){n.remove();return n;}
 get childNodes(){return this.children;}
 get firstChild(){return this.children[0]||null;}
 get nextSibling(){if(!this.parentNode)return null;const a=this.parentNode.children;return a[a.indexOf(this)+1]||null;}
 prepend(...nodes){for(const n of nodes.reverse()){if(n.parentNode)n.remove();n.parentNode=this;this.children.unshift(n);}}
 replaceChildren(...nodes){for(const n of this.children)n.parentNode=null;this.children=[];this.append(...nodes);}
 remove(){if(this.parentNode){const a=this.parentNode.children;const i=a.indexOf(this);if(i>=0)a.splice(i,1);this.parentNode=null;if(globalThis.clearDetachedScroll){const reset=n=>{if(n.classList.contains('transcript'))n.scrollTop=0;for(const c of n.children)reset(c);};reset(this);}}}
 replaceWith(n){if(this.parentNode){const p=this.parentNode;const i=p.children.indexOf(this);p.children[i]=n;n.parentNode=p;this.parentNode=null;}}
 setAttribute(k,v){if(k.startsWith('data-'))this.dataset[k.slice(5).replace(/-([a-z])/g,(_,c)=>c.toUpperCase())]=v;else this.attributes[k]=String(v);}
 getAttribute(k){if(k.startsWith('data-'))return this.dataset[k.slice(5).replace(/-([a-z])/g,(_,c)=>c.toUpperCase())]??null;return this.attributes[k]??null;}
 matches(s){s=s.trim();if(s.startsWith('.'))return this.className.split(' ').includes(s.slice(1));if(s.startsWith('#'))return this.id===s.slice(1);let not=s.includes(':not(:disabled)');s=s.replace(':not(:disabled)','');if(not&&this.disabled)return false;const attr=/^\[([^=\]]+)(?:=['"]?([^'"\]]+)['"]?)?\]$/.exec(s);if(attr){const v=this.getAttribute(attr[1]);return v!=null&&(attr[2]==null||v===attr[2]);}return this.tagName.toLowerCase()===s;}
 querySelectorAll(s){const a=[];const opts=s.split(',');function walk(n){for(const c of n.children){if(opts.some(o=>c.matches(o)))a.push(c);walk(c);}}walk(this);return a;}
 querySelector(s){return this.querySelectorAll(s)[0]||null;}
 closest(s){for(let n=this;n;n=n.parentNode)if(n.matches(s))return n;return null;}
 contains(child){for(let n=child;n;n=n.parentNode)if(n===this)return true;return false;}
 get textContent(){return this.data+this.children.map(c=>c.textContent).join('');}
 set textContent(s){this.data=String(s);this.children=[];}
 get firstElementChild(){return this.children.find(c=>c.tagName)||null;}
 get isConnected(){return this===document.body||!!this.parentNode?.isConnected;}
 getBoundingClientRect(){return {width:this.textContent.length*8,height:22};}
 focus(){document.activeElement=this;}
 setSelectionRange(a,b){this.selectionStart=a;this.selectionEnd=b;}
 click(){if(!this.disabled&&this.onclick)this.onclick({preventDefault(){},stopPropagation(){}});}
}
class FakeInput extends FakeNode{};class FakeSelect extends FakeNode{};class FakeTextArea extends FakeNode{};
globalThis.HTMLInputElement=FakeInput;globalThis.HTMLSelectElement=FakeSelect;globalThis.HTMLTextAreaElement=FakeTextArea;
globalThis.HTMLElement=FakeNode;
globalThis.document={body:new FakeNode('body'),activeElement:null,hidden:false,createElement:tag=>tag==='input'?new FakeInput(tag):tag==='select'?new FakeSelect(tag):tag==='textarea'?new FakeTextArea(tag):new FakeNode(tag),createTextNode:s=>new FakeNode('',String(s)),getElementById(id){if(this.body.id===id)return this.body;return this.body.querySelector('#'+id);},querySelector(s){return this.body.querySelector(s);},querySelectorAll(s){return this.body.querySelectorAll(s);},addEventListener(){}};
const app=document.createElement('div');app.id='app';document.body.append(app);
globalThis.URL=class {constructor(s){const m=/^(https?):\/\/([^/\?#]+)(.*)$/.exec(s);if(!m)throw Error('URL');this.protocol=m[1]+':';const a=m[2].split('@');this.username=a.length>1?a[0].split(':')[0]:'';this.password=a.length>1?(a[0].split(':')[1]||''):'';const host=a[a.length-1];this.hostname=host.startsWith('[')?host.slice(0,host.indexOf(']')+1):host.split(':')[0].toLowerCase();}static createObjectURL(){return 'blob:test';}static revokeObjectURL(){}};
globalThis.URLSearchParams=class{constructor(s){this.s=s;}get(k){const m=this.s.split('&').find(s=>s.split('=')[0]===k);return m?decodeURIComponent(m.slice(k.length+1)):null;}};
let serial=0;const timers=new Map();const queue=[];
globalThis.setTimeout=(fn,ms)=>{const id=++serial;timers.set(id,fn);return id;};globalThis.clearTimeout=id=>timers.delete(id);globalThis.setInterval=()=>++serial;
globalThis.requestAnimationFrame=fn=>{queue.push(fn);return ++serial;};
globalThis.flush=()=>{const a=queue.splice(0);for(const fn of a)fn();};globalThis.flushTimers=()=>{const a=[...timers.values()];timers.clear();for(const fn of a)fn();};
globalThis.window=globalThis;globalThis.innerWidth=1400;globalThis.innerHeight=900;
globalThis.location={protocol:'http:',host:'127.0.0.1:1234',hash:'',pathname:'/',search:''};
const storage=new Map();globalThis.sessionStorage={setItem:(k,v)=>storage.set(k,v),getItem:k=>storage.get(k)||null};globalThis.history={replaceState(a,b,url){location.hash='';}};globalThis.navigator={};globalThis.confirm=()=>true;globalThis.prompt=()=>null;
globalThis.testDone=false;

globalThis.performance={mark(){},measure(){},clearMarks(){},clearMeasures(){},getEntriesByName(){return [];}};
