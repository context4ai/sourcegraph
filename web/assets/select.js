// Select: a styled listbox in place of every native <select> on the site.
// The native element stays in the DOM, visually hidden, as the single source of truth for
// value, form submission and input/change events, so page code keeps using it directly.
// Opt out with data-native; hidden selects and multiple selects are left alone.
(function () {
  'use strict';
  var proto = HTMLSelectElement.prototype;
  var VALUE = Object.getOwnPropertyDescriptor(proto, 'value'), INDEX = Object.getOwnPropertyDescriptor(proto, 'selectedIndex');
  var icon = function (d, cls) { return '<svg viewBox="0 0 24 24" class="' + cls + '" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' + d + '</svg>'; };
  var CHEVRON = icon('<path d="m7 10 5 5 5-5"/>', 'h-3.5 w-3.5 shrink-0 text-muted');
  var CHECK = icon('<path d="M20 6 9 17l-5-5"/>', 'h-3.5 w-3.5');
  var uid = 0, panel = null, current = null;

  function options(sel) { return Array.prototype.slice.call(sel.options); }
  function usable(o) { return o && !o.disabled && !(o.parentNode && o.parentNode.disabled); }

  function ensurePanel() {
    if (panel) return panel;
    panel = document.createElement('div');
    panel.setAttribute('role', 'listbox');
    panel.id = 'select-listbox';
    panel.className = 'lift fixed z-50 hidden overflow-auto rounded-surface bg-card p-1.5 text-[13.5px] text-ink';
    panel.addEventListener('mousedown', function (e) { e.preventDefault(); });
    panel.addEventListener('click', function (e) {
      var row = e.target.closest('[data-i]');
      if (row && current) choose(+row.dataset.i);
    });
    panel.addEventListener('mousemove', function (e) {
      var row = e.target.closest('[data-i]');
      if (row && current && +row.dataset.i !== current.active) setActive(+row.dataset.i, false);
    });
    document.body.appendChild(panel);
    return panel;
  }

  function renderPanel() {
    var sel = current.sel, chosen = sel.selectedIndex;
    panel.innerHTML = options(sel).map(function (o, i) {
      var on = i === current.active, ok = usable(o);
      return '<div role="option" id="select-opt-' + i + '" data-i="' + i + '" aria-selected="' + (i === chosen) + '"' + (ok ? '' : ' aria-disabled="true"') +
        ' class="flex items-center gap-2 rounded-control px-2 py-1.5 ' + (ok ? 'cursor-pointer ' : 'opacity-40 ') + (on && ok ? 'bg-sand' : '') + '">' +
        '<span class="grid w-3.5 shrink-0 place-items-center">' + (i === chosen ? CHECK : '') + '</span>' +
        '<span class="min-w-0 truncate">' + App.esc(o.textContent) + '</span></div>';
    }).join('');
    current.trigger.setAttribute('aria-activedescendant', current.active >= 0 ? 'select-opt-' + current.active : '');
  }

  function place() {
    var r = current.trigger.getBoundingClientRect(), vw = document.documentElement.clientWidth, vh = window.innerHeight, gap = 4, edge = 8;
    var below = vh - r.bottom - gap - edge, above = r.top - gap - edge;
    panel.style.minWidth = Math.max(r.width, 112) + 'px';
    panel.style.maxWidth = (vw - edge * 2) + 'px';
    panel.style.maxHeight = '';
    var h = Math.min(panel.scrollHeight, 288), up = h > below && above > below;
    panel.style.maxHeight = Math.min(288, up ? above : below) + 'px';
    h = Math.min(h, up ? above : below);
    panel.style.top = (up ? r.top - gap - h : r.bottom + gap) + 'px';
    panel.style.left = Math.max(edge, Math.min(r.left, vw - edge - panel.offsetWidth)) + 'px';
  }

  function setActive(i, scroll) {
    current.active = i;
    renderPanel();
    if (scroll) { var row = panel.querySelector('[data-i="' + i + '"]'); if (row) row.scrollIntoView({block: 'nearest'}); }
  }

  function step(from, dir) {
    var list = options(current.sel);
    for (var i = from + dir; i >= 0 && i < list.length; i += dir) if (usable(list[i])) return i;
    return from;
  }

  function openFor(state) {
    if (current && current !== state) close(false);
    current = state;
    ensurePanel();
    // A modal <dialog> sits in the top layer and makes the rest of the page inert, so the list must live inside it.
    var host = state.trigger.closest('dialog[open]') || document.body;
    if (panel.parentNode !== host) host.appendChild(panel);
    state.trigger.setAttribute('aria-expanded', 'true');
    state.trigger.classList.add('ring-1', 'ring-ink/20');
    current.active = state.sel.selectedIndex >= 0 ? state.sel.selectedIndex : step(-1, 1);
    renderPanel();
    panel.classList.remove('hidden');
    place();
    setActive(current.active, true);
  }

  function close(focus) {
    if (!current) return;
    var state = current;
    current = null;
    panel.classList.add('hidden');
    panel.innerHTML = '';
    state.trigger.setAttribute('aria-expanded', 'false');
    state.trigger.removeAttribute('aria-activedescendant');
    state.trigger.classList.remove('ring-1', 'ring-ink/20');
    if (focus && state.trigger.isConnected) state.trigger.focus();
  }

  function choose(i) {
    var state = current, sel = state.sel;
    if (!usable(sel.options[i])) return;
    close(true);
    if (sel.selectedIndex === i) return;
    INDEX.set.call(sel, i);
    state.render();
    sel.dispatchEvent(new Event('input', {bubbles: true}));
    sel.dispatchEvent(new Event('change', {bubbles: true}));
  }

  function typeahead(state, key) {
    var list = options(state.sel), n = list.length, from = current ? current.active : state.sel.selectedIndex;
    for (var k = 1; k <= n; k++) {
      var i = (from + k + n) % n;
      if (usable(list[i]) && list[i].textContent.trim().toLowerCase().indexOf(key.toLowerCase()) === 0) return i;
    }
    return -1;
  }

  function enhance(sel) {
    if (sel.dataset.enhanced || sel.multiple || sel.hasAttribute('data-native') || sel.hidden || sel.classList.contains('hidden')) return;
    sel.dataset.enhanced = '1';
    var trigger = document.createElement('button');
    trigger.type = 'button';
    var cls = sel.className.split(/\s+/).filter(function (c) { return c && c !== 'block' && c !== 'inline-block'; });
    trigger.className = cls.join(' ') + ' ' + (/\bblock\b/.test(sel.className) ? 'flex' : 'inline-flex') + ' items-center justify-between gap-2 text-left outline-none focus-visible:ring-1 focus-visible:ring-ink/20 disabled:cursor-not-allowed disabled:opacity-40';
    trigger.innerHTML = '<span data-label class="min-w-0 truncate"></span>' + CHEVRON;
    trigger.setAttribute('role', 'combobox');
    trigger.setAttribute('aria-haspopup', 'listbox');
    trigger.setAttribute('aria-expanded', 'false');
    trigger.setAttribute('aria-controls', 'select-listbox');
    if (sel.id) trigger.dataset.selectFor = sel.id;
    var label = sel.labels && sel.labels[0];
    if (sel.getAttribute('aria-label')) trigger.setAttribute('aria-label', sel.getAttribute('aria-label'));
    else if (label) { if (!label.id) label.id = 'select-label-' + (++uid); trigger.setAttribute('aria-labelledby', label.id); }

    var state = {sel: sel, trigger: trigger, active: -1};
    state.render = function () {
      var o = sel.options[sel.selectedIndex];
      trigger.querySelector('[data-label]').textContent = o ? o.textContent : '';
      trigger.disabled = sel.disabled;
      if (current === state) { if (sel.disabled) close(false); else renderPanel(); }
    };

    sel.className = 'sr-only';
    sel.tabIndex = -1;
    sel.setAttribute('aria-hidden', 'true');
    sel.insertAdjacentElement('afterend', trigger);
    Object.defineProperty(sel, 'value', {configurable: true, get: function () { return VALUE.get.call(this); }, set: function (v) { VALUE.set.call(this, v); state.render(); }});
    Object.defineProperty(sel, 'selectedIndex', {configurable: true, get: function () { return INDEX.get.call(this); }, set: function (v) { INDEX.set.call(this, v); state.render(); }});
    sel.addEventListener('change', state.render);
    sel.addEventListener('input', state.render);
    sel.addEventListener('focus', function () { trigger.focus(); });
    if (sel.form) sel.form.addEventListener('reset', function () { setTimeout(state.render, 0); });
    new MutationObserver(state.render).observe(sel, {childList: true, subtree: true, characterData: true, attributes: true, attributeFilter: ['disabled', 'selected', 'label']});

    trigger.addEventListener('click', function () { if (current === state) close(true); else openFor(state); });
    trigger.addEventListener('keydown', function (e) {
      var open = current === state, k = e.key;
      if (!open) {
        if (k === 'ArrowDown' || k === 'ArrowUp' || k === 'Enter' || k === ' ') { e.preventDefault(); openFor(state); }
        else if (k.length === 1 && /\S/.test(k)) { var j = typeahead(state, k); if (j >= 0) { INDEX.set.call(sel, j); state.render(); sel.dispatchEvent(new Event('input', {bubbles: true})); sel.dispatchEvent(new Event('change', {bubbles: true})); } }
        return;
      }
      if (k === 'ArrowDown') { e.preventDefault(); setActive(step(state.active, 1), true); }
      else if (k === 'ArrowUp') { e.preventDefault(); setActive(step(state.active, -1), true); }
      else if (k === 'Home') { e.preventDefault(); setActive(step(-1, 1), true); }
      else if (k === 'End') { e.preventDefault(); setActive(step(sel.options.length, -1), true); }
      else if (k === 'Enter' || k === ' ') { e.preventDefault(); choose(state.active); }
      else if (k === 'Escape') { e.preventDefault(); e.stopPropagation(); close(true); }
      else if (k === 'Tab') close(false);
      else if (k.length === 1 && /\S/.test(k)) { var i = typeahead(state, k); if (i >= 0) setActive(i, true); }
    });
    trigger.addEventListener('blur', function () { setTimeout(function () { if (current === state && document.activeElement !== trigger) close(false); }, 0); });
    state.render();
  }

  function scan(root) {
    if (root.nodeType !== 1) return;
    if (root.tagName === 'SELECT') enhance(root);
    else root.querySelectorAll('select').forEach(enhance);
  }

  document.addEventListener('mousedown', function (e) {
    if (current && !panel.contains(e.target) && !current.trigger.contains(e.target)) close(false);
  }, true);
  window.addEventListener('scroll', function (e) { if (current && e.target !== panel) close(false); }, true);
  window.addEventListener('resize', function () { close(false); });

  function start() {
    scan(document.body);
    new MutationObserver(function (records) {
      records.forEach(function (r) { r.addedNodes.forEach(scan); });
      if (current && !current.sel.isConnected) close(false);
    }).observe(document.body, {childList: true, subtree: true});
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start);
  else start();

  window.Select = {enhance: enhance};
})();
