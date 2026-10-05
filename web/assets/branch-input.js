(function (root) {
  'use strict';

  // Keep confirmed names separate from the pending input so rerenders preserve both.
  function mount(input, {branches = [], defaultBranch = false, pending = '', onChange = () => {}} = {}) {
    const A = root.App, M = root.Management, doc = input.ownerDocument;
    let names = branches.slice(), defaultSelected = defaultBranch, composing = false, error = '';
    const control = doc.createElement('div');
    control.id = input.id + '-control';
    control.dataset.branchInput = input.id;
    control.className = 'branch-input mt-2';
    const list = doc.createElement('ul');
    list.className = 'space-y-1.5';
    const entry = doc.createElement('div');
    entry.className = 'mt-2 flex min-w-0 items-center gap-2';
    const add = doc.createElement('button');
    add.type = 'button';
    add.className = 'shrink-0 rounded-control bg-ink/[0.05] px-3 py-2 text-[13px] hover:bg-ink/[0.08]';
    const message = doc.createElement('p');
    message.id = input.id + '-error';
    message.className = 'mt-1.5 text-[13px] text-danger empty:hidden';
    message.setAttribute('role', 'alert');
    input.replaceWith(control);
    input.value = pending;
    input.required = false;
    input.autocomplete = 'off';
    input.spellcheck = false;
    input.className = 'min-w-0 flex-1 rounded-control bg-ink/[0.04] px-3 py-2 font-mono text-[13.5px] outline-none ring-1 ring-transparent focus:ring-ink/20';
    input.setAttribute('aria-describedby', message.id);
    entry.append(input, add);
    control.append(list, entry, message);

    function snapshot() { return {branches: names.slice(), defaultBranch: defaultSelected, pending: input.value}; }
    function showError(value) {
      error = value;
      message.textContent = value;
      input.setAttribute('aria-invalid', value ? 'true' : 'false');
    }
    function changed() { showError(''); onChange(snapshot()); }
    function render() {
      list.replaceChildren();
      const defaultRow = doc.createElement('li'), defaultLabel = doc.createElement('span'), toggle = doc.createElement('button');
      defaultRow.className = 'flex min-w-0 items-center gap-3 rounded-control bg-ink/[0.05] py-1.5 pl-3 pr-1.5';
      defaultLabel.className = 'min-w-0 flex-1 text-[13.5px]';
      defaultLabel.textContent = A.t('默认主干（master/main）', 'Default branch (master/main)');
      toggle.type = 'button'; toggle.dataset.defaultBranch = ''; toggle.setAttribute('role','checkbox'); toggle.setAttribute('aria-checked',String(defaultSelected));
      toggle.setAttribute('aria-label', defaultLabel.textContent);
      toggle.className = 'shrink-0 rounded-[6px] px-2 py-1 hover:bg-sand '+(defaultSelected?'text-ink':'text-muted');
      toggle.textContent = defaultSelected ? '✓' : '○';
      toggle.onclick = () => {
        if (!defaultSelected) { try { M.branches(names, true); } catch(e) { showError(e.message); return; } }
        defaultSelected = !defaultSelected; changed(); render();
      };
      defaultRow.append(defaultLabel,toggle); list.append(defaultRow);
      names.forEach(name => {
        const row = doc.createElement('li');
        row.dataset.branchTag = name;
        row.className = 'flex min-w-0 items-center gap-3 rounded-control bg-ink/[0.05] py-1.5 pl-3 pr-1.5';
        const label = doc.createElement('span');
        label.className = 'min-w-0 flex-1 break-all font-mono text-[13.5px]';
        label.textContent = name;
        const remove = doc.createElement('button');
        remove.type = 'button';
        remove.dataset.removeBranch = name;
        remove.className = 'shrink-0 rounded-[6px] px-2 py-1 text-muted hover:bg-sand hover:text-ink';
        remove.setAttribute('aria-label', A.t('移除分支 ', 'Remove branch ') + name);
        remove.textContent = '×';
        remove.onclick = () => {
          names = names.filter(branch => branch !== name);
          changed();
          render();
          input.focus();
        };
        row.append(label, remove);
        list.append(row);
      });
      input.placeholder = A.t('输入分支名称', 'Enter a branch name');
      add.textContent = A.t('添加', 'Add');
      add.setAttribute('aria-label', A.t('添加分支', 'Add branch'));
      // Validation messages are bilingual, and must survive a language change.
      showError(error);
    }
    function commit(required = false) {
      const next = [...new Set([...names, ...M.splitBranches(input.value)])];
      try { if (next.length || required) M.branches(next, defaultSelected); }
      catch (e) { showError(e.message); throw e; }
      const didChange = input.value !== '' || next.length !== names.length;
      names = next;
      input.value = '';
      if (didChange) changed(); else showError('');
      render();
      return names.slice();
    }
    function addPending() { try { commit(); } catch (_) {} }
    input.addEventListener('compositionstart', () => { composing = true; });
    input.addEventListener('compositionend', () => { composing = false; });
    input.addEventListener('input', changed);
    input.addEventListener('keydown', e => {
      if (e.key !== 'Enter' && e.key !== ',') return;
      e.stopPropagation();
      // Let the IME confirm its candidate without also confirming a branch tag.
      if (composing || e.isComposing || e.keyCode === 229) return;
      e.preventDefault();
      addPending();
    });
    input.addEventListener('paste', e => {
      const text = e.clipboardData && e.clipboardData.getData('text');
      if (!text || !/[,\r\n]/.test(text)) return;
      e.preventDefault();
      const start = input.selectionStart ?? input.value.length, end = input.selectionEnd ?? start;
      // Text inputs strip newlines on assignment; retain them as branch separators.
      input.value = input.value.slice(0, start) + text.replace(/[\r\n]+/g, ',') + input.value.slice(end);
      changed();
      addPending();
    });
    add.onclick = () => { addPending(); input.focus(); };
    render();
    return {snapshot, value: () => commit(true), usesDefault: () => defaultSelected, refresh: render};
  }

  root.BranchInput = {mount};
})(typeof window === 'undefined' ? globalThis : window);
