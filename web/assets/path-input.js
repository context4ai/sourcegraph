(function (root) {
  'use strict';
  function mount(input, {groups = [], rows: savedRows, pending = '', onChange = () => {}} = {}) {
    const A = root.App, M = root.Management, doc = input.ownerDocument;
    let rows = savedRows ? savedRows.map(x => ({...x})) : groups.flatMap(g => g.paths.map(path => ({path,group:g.name}))), composing = false, error = '';
    const control = doc.createElement('div'), list = doc.createElement('ul'), entry = doc.createElement('div'), add = doc.createElement('button'), message = doc.createElement('p');
    const help = doc.createElement('button'), helpWrap = doc.createElement('span'), tip = doc.createElement('span');
    help.type='button'; help.dataset.pathHelp=''; help.textContent='?';
    help.className='ml-1.5 inline-flex h-4 w-4 items-center justify-center rounded-full border border-current align-middle text-[11px] leading-none text-muted hover:text-ink focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2';
    helpWrap.className='inline-flex';
    tip.id=input.id+'-help'; tip.setAttribute('role','tooltip'); tip.hidden=true;
    tip.className='fixed z-[100] rounded-control border border-line bg-card px-3 py-2 text-[13px] leading-relaxed text-ink shadow-lg';
    tip.style.width='min(300px, calc(100vw - 24px))';
    help.setAttribute('aria-describedby',tip.id);
    helpWrap.append(help,tip); doc.querySelector('label[for="'+input.id+'"]')?.after(helpWrap);
    function showTip(){
      tip.hidden=false;
      const rect=help.getBoundingClientRect(), view=doc.defaultView;
      tip.style.left=Math.max(12,Math.min(rect.left,view.innerWidth-tip.offsetWidth-12))+'px';
      tip.style.top=(rect.bottom+8+tip.offsetHeight>view.innerHeight-12?Math.max(12,rect.top-tip.offsetHeight-8):rect.bottom+8)+'px';
    }
    function hideTip(){tip.hidden=true;}
    helpWrap.addEventListener('mouseenter',showTip);
    helpWrap.addEventListener('mouseleave',hideTip);
    help.addEventListener('focus',showTip); help.addEventListener('blur',hideTip);
    help.addEventListener('click',showTip);
    help.addEventListener('keydown',event=>{if(event.key==='Escape'){hideTip();event.stopPropagation();}});
    control.id = input.id + '-control'; control.className = 'mt-2';
    list.className = 'space-y-2'; entry.className = 'mt-2 flex min-w-0 gap-2';
    add.type = 'button'; add.className = 'shrink-0 rounded-control bg-ink/[0.05] px-3 py-2 text-[13px] hover:bg-ink/[0.08]';
    message.id = input.id + '-error'; message.className = 'mt-1.5 text-[13px] text-danger empty:hidden'; message.setAttribute('role','alert');
    input.replaceWith(control); input.value = pending; input.required = false; input.autocomplete = 'off'; input.spellcheck = false;
    input.className = 'min-w-0 flex-1 rounded-control bg-ink/[0.04] px-3 py-2 font-mono text-[13.5px] outline-none ring-1 ring-transparent focus:ring-ink/20';
    input.setAttribute('aria-describedby',message.id); entry.append(input,add); control.append(list,entry,message);
    function snapshot() { return {rows:rows.map(x=>({...x})),pending:input.value}; }
    function showError(value) { error=value; message.textContent=value; input.setAttribute('aria-invalid',value?'true':'false'); }
    function changed() { showError(''); onChange(snapshot()); }
    function render() {
      tip.textContent=A.t('同组目录一起下载并建立索引，可按业务分组。最多 16 组，共 64 个目录。','Directories in one group are downloaded and indexed together. Group them by business area. Up to 16 groups and 64 directories in total.');
      help.setAttribute('aria-label',A.t('路径分组说明','About path groups'));
      list.replaceChildren();
      rows.forEach((item,index)=>{
        const li=doc.createElement('li'), name=doc.createElement('span'), label=doc.createElement('label'), group=doc.createElement('input'), remove=doc.createElement('button');
        li.dataset.pathTag=item.path; li.className='flex flex-wrap items-center gap-2 rounded-control bg-ink/[0.05] p-2';
        name.className='min-w-0 flex-1 break-all font-mono text-[13px]'; name.textContent=item.path;
        label.className='flex items-center gap-1.5 text-[12px] text-muted'; label.textContent=A.t('分组','Group');
        group.value=item.group; group.type='text'; group.autocomplete='off'; group.spellcheck=false; group.maxLength=64; group.dataset.pathGroup=item.path;
        group.className='w-24 min-w-0 rounded-control bg-card px-2 py-1 font-mono text-[12px] text-ink outline-none focus:ring-1 focus:ring-ink/20';
        group.setAttribute('aria-label',A.t('分组：','Group: ')+item.path); group.oninput=()=>{rows[index].group=group.value;changed();}; label.append(group);
        remove.type='button'; remove.dataset.removePath=item.path; remove.className='shrink-0 rounded-control px-2 py-1 text-muted hover:bg-sand'; remove.textContent='×'; remove.setAttribute('aria-label',A.t('移除路径 ','Remove path ')+item.path);
        remove.onclick=()=>{rows.splice(index,1);changed();render();input.focus();}; li.append(name,label,remove); list.append(li);
      });
      input.placeholder=A.t('输入相对目录，回车添加','Relative directory, Enter to add'); add.textContent=A.t('添加','Add'); add.setAttribute('aria-label',A.t('添加路径','Add path')); showError(error);
    }
    function commit(required=false) {
      const pendingPaths=input.value.split(/[\r\n]/).map(x=>x.trim()).filter(Boolean);
      let next=rows.map(x=>({...x}));
      try {
        for(const raw of pendingPaths){const path=M.directoryPath(raw);if(!next.some(x=>x.path===path))next.push({path,group:M.DEFAULT_PATH_GROUP});}
        if(next.length||required)M.groupPathRows(next);
      } catch(e) { showError(e.message); throw e; }
      const didChange=input.value!==''; rows=next;input.value='';if(didChange)changed();else showError('');render();return rows.length?M.groupPathRows(rows):[];
    }
    function addPending(){try{commit();}catch(_){}}
    input.addEventListener('input',changed);
    input.addEventListener('compositionstart',()=>{composing=true;}); input.addEventListener('compositionend',()=>{composing=false;});
    input.addEventListener('keydown',e=>{if(e.key!=='Enter')return;e.stopPropagation();if(composing||e.isComposing||e.keyCode===229)return;e.preventDefault();addPending();});
    input.addEventListener('paste',e=>{
      const text=e.clipboardData?.getData('text');if(!text||!/[\r\n]/.test(text))return;e.preventDefault();
      // Validate the full pasted batch first: a text input would otherwise silently strip newlines.
      const original=input.value, prior=rows.map(x=>({...x}));
      try {const paths=(input.value+'\n'+text).split(/[\r\n]+/).map(x=>x.trim()).filter(Boolean);const next=rows.map(x=>({...x}));for(const raw of paths){const path=M.directoryPath(raw);if(!next.some(x=>x.path===path))next.push({path,group:M.DEFAULT_PATH_GROUP});}M.groupPathRows(next);rows=next;input.value='';changed();render();}
      catch(err){rows=prior;input.value=original;showError(err.message);}
    });
    add.onclick=()=>{addPending();input.focus();};render();
    return {snapshot,value:()=>commit(true),refresh:render};
  }
  root.PathInput={mount};
})(typeof window==='undefined'?globalThis:window);
