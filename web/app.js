'use strict';
/* Signet — fill, sign, initial. Everything runs in the browser; no file leaves the machine.
   Coordinates everywhere: PDF points, origin at the TOP-LEFT of the page as displayed. */

pdfjsLib.GlobalWorkerOptions.workerSrc = 'vendor/pdf.worker.min.js';

const $ = s => document.querySelector(s);
const view = $('#view');
const INKS = { black: '#16161a', blue: '#1f3fae', red: '#c9211e' };
const HL = { yellow: ['#ffe500', '#c79a00'], green: ['#86e57f', '#1e9e3a'], pink: ['#ff9ad5', '#d6249f'], red: ['#ff8a80', '#d32f2f'] };
const FONTS = {
  helv: { label: 'Helvetica', css: 'Helvetica, Arial, sans-serif', std: 'Helvetica' },
  times: { label: 'Times', css: '"Times New Roman", Times, serif', std: 'TimesRoman' },
  courier: { label: 'Courier', css: '"Courier New", Courier, monospace', std: 'Courier' },
  hand: { label: 'Handwriting', css: '"Bradley Hand", "Segoe Print", "Comic Sans MS", cursive', std: null },
};
const p2 = n => String(n).padStart(2, '0');
const DATE_FMTS = {
  long: d => d.toLocaleDateString('en-GB', { day: 'numeric', month: 'long', year: 'numeric' }),
  slash: d => `${p2(d.getDate())}/${p2(d.getMonth() + 1)}/${d.getFullYear()}`,
  iso: d => `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())}`,
  short: d => d.toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric' }),
};
const fmtDate = (v, k) => { const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(v || ''); return DATE_FMTS[k](m ? new Date(+m[1], m[2] - 1, +m[3]) : new Date()); };

const uid = () => 'i' + Math.random().toString(36).slice(2, 10);
const clamp = (v, a, b) => Math.max(a, Math.min(b, v));
const jit = r => (Math.random() * 2 - 1) * r;
const mkCanvas = (w, h) => Object.assign(document.createElement('canvas'), { width: Math.max(1, Math.round(w)), height: Math.max(1, Math.round(h)) });
const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };

/* ---------- storage: this browser, plus the Signet app on this machine when it is running ---------- */
const store = {
  server: false,
  get(k, d) { try { const v = localStorage.getItem('signet.' + k); return v ? JSON.parse(v) : d; } catch { return d; } },
  set(k, v) {
    const s = JSON.stringify(v);
    try { localStorage.setItem('signet.' + k, s); } catch (e) { if (!this.server) alert('Could not save to this browser: ' + e.message); }
    if (this.server && (k === 'bank' || k === 'profile')) this.q[k] = (this.q[k] || Promise.resolve()).then(() => this.put(k, s));
  },
  tags: {}, q: {},   // the saved version each change is made on, so a change made elsewhere is never overwritten
  async put(k, s) {
    try {
      const headers = this.tags[k] ? { 'X-Signet-If-Version': this.tags[k] } : {};
      const r = await fetch('api/store/' + k, { method: 'PUT', body: s, headers });
      if (r.ok) { this.tags[k] = r.headers.get('X-Signet-Version') || ''; return; }
      const e = await r.json().catch(() => ({}));
      alert('Not saved: ' + (e.error || 'error ' + r.status));
    } catch (e) { alert('Not saved: Signet could not be reached (' + e.message + ')'); }
  },
  async init() {
    for (const k of ['bank', 'profile']) {
      try {
        const r = await fetch('api/store/' + k);
        if (!r.ok || !(r.headers.get('content-type') || '').includes('json')) return;
        this.server = true;
        this.tags[k] = r.headers.get('X-Signet-Version') || '';
        const v = await r.json(), local = this.get(k, null);
        if (v !== null) localStorage.setItem('signet.' + k, JSON.stringify(v));
        else if (local) this.set(k, local);
      } catch { return; }
    }
    if (this.server) setInterval(() => fetch('healthz').catch(() => {}), 60000);   // keeps the local app running while this window is open
  },
};
const G = Object.assign({ ink: 'black', tink: 'black', fs: 11, font: 'helv', datefmt: 'long', size: { sig: 150, ini: 46 }, hl: 'yellow', hlMode: 'hl' }, store.get('prefs', {}));
const savePrefs = () => store.set('prefs', G);
if (!INKS[G.ink]) G.ink = 'black';

let bank = [], profile = null, docs = [], S = null, armed = null;
const DEF_PROFILE = { groups: [{ name: 'Me', fields: [
  { key: 'first', label: 'First name', value: '' }, { key: 'pref', label: 'Preferred name', value: '' }, { key: 'second', label: 'Second name', value: '' }, { key: 'sur', label: 'Surname', value: '' },
  { key: 'dob', label: 'Date of birth', value: '', type: 'date' }, { key: 'id', label: 'ID number', value: '' },
  { key: 'cell', label: 'Cell number', value: '' }, { key: 'email', label: 'Email', value: '' },
  { key: 'unit', label: 'Apartment / building', value: '', addr: true }, { key: 'streetnr', label: 'Street number', value: '', addr: true }, { key: 'street', label: 'Street name', value: '', addr: true },
  { key: 'suburb', label: 'Suburb', value: '', addr: true }, { key: 'postal', label: 'Postal code', value: '', addr: true }, { key: 'city', label: 'City', value: '', addr: true },
  { key: 'province', label: 'Province', value: '', addr: true }, { key: 'country', label: 'Country', value: '', addr: true },
] }] };
function migrateProfile(p) {   // bring an older or imported profile up to the current set of standard details
  const def = DEF_PROFILE.groups[0].fields;
  if (!p.groups[0]) p.groups.unshift({ name: 'Me', fields: [] });
  const g = p.groups[0], old = g.fields.find(f => f.key === 'addr');
  if (old) { if ((old.value || '').trim()) { delete old.key; old.label = 'Address (old)'; } else g.fields.splice(g.fields.indexOf(old), 1); }
  const keyed = Object.fromEntries(g.fields.filter(f => f.key).map(f => [f.key, f]));
  g.fields = [...def.map(d => ({ ...d, value: keyed[d.key]?.value || '' })), ...g.fields.filter(f => !f.key || !def.some(d => d.key === f.key)).map(f => { delete f.key; return f; })];
  return p;
}

/* ---------- signature bank ---------- */
const masks = {};
function maskOf(id, src) {
  if (!masks[id]) { const img = new Image(); img.src = src; masks[id] = { src, img, tints: {} }; }
  return masks[id];
}
const saveBank = () => store.set('bank', bank);
async function tinted(id, ink) {
  const m = masks[id];
  if (!m) return null;
  if (!INKS[ink]) ink = 'blue';
  if (!m.tints[ink]) {
    await m.img.decode();
    const c = mkCanvas(m.img.naturalWidth, m.img.naturalHeight), x = c.getContext('2d');
    x.drawImage(m.img, 0, 0);
    x.globalCompositeOperation = 'source-in';
    x.fillStyle = INKS[ink];
    x.fillRect(0, 0, c.width, c.height);
    m.tints[ink] = { c, url: c.toDataURL() };
  }
  return m.tints[ink];
}

/* ---------- documents and tabs ---------- */
const io = new IntersectionObserver(es => {
  for (const e of es) { const P = e.target.__P; if (P) { P.visible = e.isIntersecting; renderPage(P); } }
}, { root: view, rootMargin: '500px' });

function touch(D = S) { if (D && !D.changed) { D.changed = true; renderTabs(); } }
function renderTabs() {
  const t = $('#tabs');
  t.innerHTML = '';
  for (const D of docs) {
    const tab = el('div', 'tab' + (D === S ? ' on' : '') + (D.changed ? ' dirty' : '')), nm = el('span', 'tname', D.name), x = el('button', 'tclose', '×');
    nm.title = D === S ? 'Click to rename' : D.name;
    x.title = 'Close';
    tab.onclick = () => { if (D !== S) activate(D); };
    nm.onclick = e => { if (D === S) { e.stopPropagation(); rename(nm, D); } };
    x.onclick = e => { e.stopPropagation(); closeDoc(D); };
    tab.append(nm, x); t.append(tab);
  }
  document.title = S ? S.name + ' — Signet' : 'Signet';
  document.body.classList.toggle('loaded', !!S);
}
function rename(nm, D) {
  if (nm.isContentEditable) return;
  try { nm.contentEditable = 'plaintext-only'; } catch { nm.contentEditable = 'true'; }
  nm.focus();
  getSelection().selectAllChildren(nm);
  nm.onkeydown = e => { if (e.key === 'Enter' || e.key === 'Escape') { e.preventDefault(); nm.blur(); } };
  nm.onblur = () => { D.name = nm.textContent.trim().replace(/\.pdf$/i, '') || D.name; renderTabs(); };
}
function activate(D) {
  if (S) { select(null); S.scroll = [view.scrollLeft, view.scrollTop]; S.el.hidden = true; }
  arm(null);
  S = D;
  if (D) {
    D.el.hidden = false;
    view.scrollLeft = D.scroll[0]; view.scrollTop = D.scroll[1];
    $('#zlabel').textContent = Math.round(D.zoom * 100) + '%';
    $('#q').value = D.q;
    $('#qcount').textContent = D.hits.length ? `${D.hit + 1}/${D.hits.length}` : '';
  }
  renderTabs();
}
function ask(msg, buttons) {
  return new Promise(res => {
    const d = $('#ask'), row = $('#askBtns');
    $('#askMsg').textContent = msg; row.innerHTML = '';
    let done = false;
    const fin = v => { if (!done) { done = true; d.close(); res(v); } };
    buttons.forEach((b, i) => { const btn = el('button', i === 0 ? 'primary' : '', b); btn.onclick = () => fin(i); row.append(btn); });
    d.onclose = () => fin(null);
    d.showModal();
  });
}
async function closeDoc(D) {
  if (D.changed) {
    if (D !== S) activate(D);
    const a = await ask(`Save changes to “${D.name}” before closing?`, ['Save', 'Don’t save', 'Cancel']);
    if (a === null || a === 2) return;
    if (a === 0 && !(await exportPdf(D))) return;
  }
  D.pages.forEach(P => { io.unobserve(P.el); P.task?.cancel(); });
  D.pdf?.destroy(); D.el.remove();
  const i = docs.indexOf(D);
  docs.splice(i, 1);
  if (S === D) { S = null; activate(docs[i] || docs[i - 1] || null); } else renderTabs();
}

async function openBytes(buf, name, src) {   // src: the server's id for this file, when it came from `signet open`
  const bytes = new Uint8Array(buf);
  let cancelled = false;
  const task = pdfjsLib.getDocument({ data: bytes.slice(), isEvalSupported: false });   // no eval: the page runs under a strict security policy
  task.onPassword = (cb, reason) => {
    const p = prompt(reason === 1 ? 'This PDF is locked. Password:' : 'Wrong password — try again:');
    if (p === null) { cancelled = true; task.destroy(); } else cb(p);
  };
  let pdf;
  try { pdf = await task.promise; }
  catch (e) { if (!cancelled) alert('Could not open that PDF: ' + (e.message || e)); return; }

  const D = { id: uid(), el: el('div', 'pages'), pdf, bytes, name: (name || 'document').replace(/\.pdf$/i, ''), zoom: 0, pages: [], items: [], sel: null,
    dirty: new Set(), undo: [], redo: [], hits: [], hit: -1, changed: false, scroll: [0, 0], q: '', searchToken: 0, src };
  view.append(D.el);
  docs.push(D);
  activate(D);
  for (let n = 1; n <= pdf.numPages; n++) {
    const page = await pdf.getPage(n), vp = page.getViewport({ scale: 1 });
    if (!docs.includes(D)) return;
    const pe = el('div', 'page');
    pe.style.setProperty('--w', vp.width); pe.style.setProperty('--h', vp.height);
    pe.innerHTML = '<canvas></canvas><div class="hl"></div><div class="fields"></div><div class="items"></div>';
    D.el.append(pe);
    const P = pe.__P = { n, page, vp, el: pe, doc: D, fields: [], visible: false, z: 0 };
    D.pages.push(P);
    if (n === 1) { setZoom(Math.min(1.5, fitZoom(D)), undefined, 0, D); if (D === S) view.scrollTop = 0; }
    io.observe(pe);
    buildFields(P);
  }
  return D;
}

async function renderPage(P) {
  const D = P.doc;
  if (!P.visible || P.z === D.zoom) return;
  const z = P.z = D.zoom;
  P.task?.cancel();
  const scale = Math.min(z * devicePixelRatio, Math.sqrt(2e7 / (P.vp.width * P.vp.height)));
  const vp = P.page.getViewport({ scale }), c = mkCanvas(vp.width, vp.height);
  const task = P.task = P.page.render({ canvasContext: c.getContext('2d'), viewport: vp, annotationMode: pdfjsLib.AnnotationMode.ENABLE_FORMS });
  try { await task.promise; if (P.z === z) P.el.querySelector('canvas').replaceWith(c); }
  catch (e) { if (e?.name !== 'RenderingCancelledException') console.error(e); }
}

/* ---------- real form fields (AcroForm) ---------- */
async function buildFields(P) {
  const D = P.doc, box = P.el.querySelector('.fields');
  for (const a of await P.page.getAnnotations()) {
    if (a.subtype !== 'Widget' || a.readOnly || a.hidden || !a.fieldName) continue;
    const r = pdfjsLib.Util.normalizeRect(P.vp.convertToViewportRectangle(a.rect));
    const w = r[2] - r[0], h = r[3] - r[1];
    let f, type;
    if (a.fieldType === 'Tx') {
      type = 'text';
      f = document.createElement(a.multiLine ? 'textarea' : 'input');
      f.value = a.fieldValue || '';
      if (a.maxLen) f.maxLength = a.maxLen;
      if (a.textAlignment) f.style.textAlign = ['left', 'center', 'right'][a.textAlignment];
      f.style.setProperty('--fs', a.multiLine ? 10 : clamp(h * 0.62, 6, 12));
    } else if (a.fieldType === 'Btn' && (a.checkBox || a.radioButton)) {
      type = a.checkBox ? 'checkbox' : 'radio';
      f = document.createElement('input');
      f.type = type;
      if (a.checkBox) f.checked = !!a.fieldValue && a.fieldValue !== 'Off' && a.fieldValue === a.exportValue;
      else { f.name = D.id + a.fieldName; f.value = a.buttonValue; f.checked = a.fieldValue === a.buttonValue; }
    } else if (a.fieldType === 'Ch') {
      type = 'select';
      f = document.createElement('select');
      f.add(new Option('', ''));
      for (const o of a.options || []) f.add(new Option(o.displayValue, o.exportValue));
      f.value = Array.isArray(a.fieldValue) ? a.fieldValue[0] ?? '' : a.fieldValue ?? '';
      f.style.setProperty('--fs', clamp(h * 0.62, 6, 12));
    } else continue;
    f.className = 'fld';
    f.dataset.name = a.fieldName; f.dataset.aid = a.id;
    f.title = a.alternativeText || a.fieldName;
    f.style.setProperty('--x', r[0]); f.style.setProperty('--y', r[1]);
    f.style.setProperty('--iw', w); f.style.setProperty('--ih', h);
    const changed = () => {
      D.dirty.add(a.fieldName); touch(D);
      for (const o of fieldEls(a.fieldName, D)) {
        if (type === 'text' && o !== f) o.value = f.value;
        const isBox = o.type === 'checkbox' || o.type === 'radio';
        D.pdf.annotationStorage.setValue(o.dataset.aid, { value: isBox ? o.checked : o.value });
      }
    };
    f.addEventListener('input', changed); f.addEventListener('change', changed);
    box.append(f);
    P.fields.push({ name: a.fieldName, type, x: r[0], y: r[1], w, h, el: f });
  }
}
const fieldEls = (name, D = S) => [...D.el.querySelectorAll('.fld')].filter(e => e.dataset.name === name);
function setField(name, value) {
  const els = fieldEls(name);
  if (!els.length) return false;
  for (const f of els) {
    if (f.type === 'checkbox') f.checked = !!value;
    else if (f.type === 'radio') f.checked = f.value === value;
    else f.value = value;
  }
  els[0].dispatchEvent(new Event('input'));
  return true;
}

/* ---------- zoom ---------- */
let zt;
const fitZoom = (D = S) => view.clientWidth < 200 ? 1 : (view.clientWidth - 48) / Math.max(1, ...D.pages.map(p => p.vp.width));
function setZoom(z, cx = view.clientWidth / 2, cy = 0, D = S) {
  if (!D) return;
  z = clamp(z, 0.25, 5);
  const r = D.zoom ? z / D.zoom : 1;
  const sl = (view.scrollLeft + cx) * r - cx, st = (view.scrollTop + cy) * r - cy;
  D.zoom = z;
  D.el.style.setProperty('--z', z);
  if (D === S) { view.scrollLeft = sl; view.scrollTop = st; $('#zlabel').textContent = Math.round(z * 100) + '%'; }
  clearTimeout(zt);
  zt = setTimeout(() => D.pages.forEach(renderPage), 140);
}
view.addEventListener('wheel', e => {
  if (!S || !(e.ctrlKey || e.metaKey)) return;
  e.preventDefault();
  const r = view.getBoundingClientRect();
  setZoom(S.zoom * Math.exp(-e.deltaY * 0.01), e.clientX - r.left, e.clientY - r.top);
}, { passive: false });
$('#zin').onclick = () => S && setZoom(S.zoom * 1.2, undefined, view.clientHeight / 2);
$('#zout').onclick = () => S && setZoom(S.zoom / 1.2, undefined, view.clientHeight / 2);
$('#zlabel').onclick = () => S && setZoom(fitZoom());

/* ---------- items: text, ticks, signatures, highlights ---------- */
function pushUndo() {
  S.undo.push(JSON.stringify(S.items));
  if (S.undo.length > 200) S.undo.shift();
  S.redo = [];
  touch();
}
function timeTravel(from, to) {
  const s = S?.[from].pop();
  if (!s) return;
  document.activeElement?.blur?.();
  S[to].push(JSON.stringify(S.items));
  select(null);
  S.el.querySelectorAll('.item').forEach(e => e.remove());
  S.items = JSON.parse(s);
  S.items.forEach(drawItem);
  touch();
}
const undo = () => timeTravel('undo', 'redo'), redo = () => timeTravel('redo', 'undo');
$('#undoBtn').onclick = undo;
$('#redoBtn').onclick = redo;

/* ---------- copy / paste of things you added ---------- */
let clip = null, mouse = { x: -1, y: -1 };
addEventListener('pointermove', e => { mouse = { x: e.clientX, y: e.clientY }; });
function copyItem(it = S?.sel) { if (it && it.type !== 'hl') clip = JSON.stringify(it); return !!clip; }
function pasteItem(P, x, y) {   // at a given spot, else under the mouse, else just beside the original
  if (!clip || !S) return;
  const it = JSON.parse(clip), w = it.w || 0, h = it.h || (it.fs || 0) * 1.2;
  if (!P) { const pe = document.elementFromPoint(mouse.x, mouse.y)?.closest?.('.page'); if (pe && S.el.contains(pe)) { P = pe.__P; ({ x, y } = pagePt(P, { clientX: mouse.x, clientY: mouse.y })); } }
  if (P) { it.page = P.n; it.x = x - w / 2; it.y = y - h / 2; }
  else { it.page = Math.min(it.page, S.pages.length); it.x += 12; it.y += 12; }
  it.id = null;
  if (it.type === 'sig') it.rot = jit(it.kind === 'ini' ? 3 : 1.5);
  pushUndo();
  select(addItem(it));
  clip = JSON.stringify({ ...it, id: null });   // pasting again without moving the mouse steps along
}

function addItem(it) {
  it.id = it.id || uid();
  it.ink = it.ink || 'black';
  S.items.push(it);
  drawItem(it);
  return it;
}
function removeItem(it) {
  if (S.sel === it) select(null);
  S.items = S.items.filter(i => i !== it);
  document.getElementById(it.id)?.remove();
}
function drawItem(it) {
  let e = document.getElementById(it.id);
  const host = S.pages[it.page - 1].el.querySelector('.items');
  if (!e) {
    e = el('div', 'item ' + it.type);
    e.id = it.id;
    if (it.type === 'text') { e.innerHTML = '<div class="txt"></div>'; e.firstChild.textContent = it.text; }
    else if (it.type === 'sig') e.innerHTML = '<img alt="" draggable="false">';
    else if (it.type === 'hl') e.innerHTML = '<span class="marks"></span>';
    else e.innerHTML = '<svg viewBox="0 0 1 1" preserveAspectRatio="none"><path fill="none" stroke="currentColor" stroke-width=".11" stroke-linecap="round" stroke-linejoin="round"/></svg>';
    if (it.type !== 'hl') e.insertAdjacentHTML('beforeend', '<span class="handle"></span>');
  }
  if (e.parentNode !== host) it.type === 'hl' ? host.prepend(e) : host.append(e);
  const st = e.style;
  st.setProperty('--x', it.x); st.setProperty('--y', it.y);
  if (it.type === 'hl') {
    const m = e.querySelector('.marks');
    m.className = 'marks ' + it.mode; m.innerHTML = '';
    st.setProperty('--c', HL[it.color][0]); st.setProperty('--c2', HL[it.color][1]);
    for (const r of it.rects) {
      const i = el('i');
      i.style.cssText = `left:calc(${r.x - it.x}*var(--z)*1px);top:calc(${r.y - it.y}*var(--z)*1px);width:calc(${r.w}*var(--z)*1px);height:calc(${r.h * (it.mode === 'ul' ? 0.9 : 1)}*var(--z)*1px)`;
      m.append(i);
    }
    return e;
  }
  st.color = INKS[it.ink] || INKS.blue;
  if (it.type === 'text') { st.setProperty('--fs', it.fs); e.querySelector('.txt').style.fontFamily = FONTS[it.font || 'helv'].css; }
  else { st.setProperty('--iw', it.w); st.setProperty('--ih', it.h); }
  if (it.type === 'sig') {
    st.setProperty('--rot', it.rot + 'deg');
    tinted(it.mask, it.ink).then(t => { if (t) e.querySelector('img').src = t.url; });
  }
  if (it.type === 'check') e.querySelector('path').setAttribute('d', it.mark === 'x' ? 'M.15 .15L.85 .85M.85 .15L.15 .85' : 'M.08 .55L.36 .88L.92 .12');
  return e;
}
function select(it) {
  if (!S || S.sel === it) return;
  S.el.querySelector('.item.sel')?.classList.remove('sel');
  S.el.querySelector('.tb')?.remove();
  S.sel = it;
  if (!it) return;
  const e = document.getElementById(it.id);
  e.classList.add('sel');
  e.append(toolbar(it));
}
function toolbar(it) {
  const tb = el('div', 'tb');
  const act = fn => { pushUndo(); fn(); savePrefs(); if (S.items.includes(it)) { drawItem(it); tb.replaceWith(toolbar(it)); } };
  const btn = (label, title, fn, cls = '') => {
    const b = el('button', cls, label);
    b.title = title;
    b.onclick = e => { e.stopPropagation(); act(fn); };
    tb.append(b);
    return b;
  };
  if (it.type === 'hl') {
    for (const k in HL) btn('', k, () => { G.hl = it.color = k; }, 'dot' + (it.color === k ? ' on' : '')).style.background = HL[k][0];
    btn('U', 'Underline instead of highlight', () => { G.hlMode = it.mode = it.mode === 'ul' ? 'hl' : 'ul'; }, 'ul' + (it.mode === 'ul' ? ' act' : ''));
  } else {
    if (it.type === 'text') {
      const s = el('select');
      s.title = 'Font';
      for (const k in FONTS) s.add(new Option(FONTS[k].label, k));
      s.value = it.font || 'helv';
      s.onchange = () => act(() => { G.font = it.font = s.value; });
      tb.append(s);
      btn('−', 'Smaller (↓)', () => scaleItem(it, -1));
      tb.append(el('span', 'sz', String(it.fs)));
      btn('+', 'Bigger (↑)', () => scaleItem(it, 1));
    }
    for (const k in INKS) btn('', k[0].toUpperCase() + k.slice(1) + ' pen', () => { it.ink = k; if (it.type === 'sig') G.ink = k; else if (it.type === 'text') G.tink = k; }, 'dot' + (it.ink === k ? ' on' : '')).style.background = INKS[k];
    if (it.type === 'sig' && it.kind === 'ini' && S.pages.length > 1) btn('All pages', 'Initial every other page in the same spot', () => copyToAll(it));
  }
  btn('✕', 'Delete (⌫)', () => removeItem(it));
  return tb;
}
function scaleItem(it, dir) {
  if (it.type === 'hl') return;
  if (it.type === 'text') { G.fs = it.fs = clamp(it.fs + dir, 5, 72); return; }
  const k = dir > 0 ? 1.06 : 1 / 1.06, cx = it.x + it.w / 2, cy = it.y + it.h / 2;
  it.w *= k; it.h *= k; it.x = cx - it.w / 2; it.y = cy - it.h / 2;
  if (it.type === 'sig') G.size[it.kind] = it.w;
}
function copyToAll(it) {
  for (const P of S.pages) {
    if (P.n === it.page) continue;
    const k = 1 + jit(0.04), w = it.w * k, h = it.h * k;
    addItem({ ...it, id: null, page: P.n, w, h, rot: jit(3), x: clamp(it.x + jit(1.5), 0, P.vp.width - w), y: clamp(it.y + jit(1.5), 0, P.vp.height - h) });
  }
}
function editText(it, isNew) {
  const e = document.getElementById(it.id), t = e?.querySelector('.txt');
  if (!t || e.classList.contains('editing')) return;
  if (!isNew) pushUndo();
  e.classList.add('editing');
  try { t.contentEditable = 'plaintext-only'; } catch { t.contentEditable = 'true'; }
  t.focus();
  const r = document.createRange(); r.selectNodeContents(t); r.collapse(false);
  const s = getSelection(); s.removeAllRanges(); s.addRange(r);
  const read = () => t.innerText.replace(/\r/g, '').replace(/\n+$/, '');
  t.oninput = () => { it.text = read(); touch(); };
  t.onkeydown = ev => {
    if ((ev.key === 'Enter' && !ev.shiftKey) || ev.key === 'Escape') { ev.preventDefault(); ev.stopPropagation(); t.blur(); }
    else if (ev.altKey && (ev.key === 'ArrowUp' || ev.key === 'ArrowDown')) { ev.preventDefault(); scaleItem(it, ev.key === 'ArrowUp' ? 1 : -1); drawItem(it); }
  };
  t.onblur = () => {
    t.contentEditable = 'false'; e.classList.remove('editing');
    it.text = read();
    if (!it.text.trim()) removeItem(it);
    else if (S?.sel === it) e.querySelector('.tb')?.replaceWith(toolbar(it));
  };
}

/* ---------- inserting ---------- */
const ghost = $('#ghost');
function arm(a) {
  armed = a;
  view.classList.toggle('arming', !!a);
  document.querySelectorAll('#tools button').forEach(b => b.classList.toggle('on',
    !!a && (b.dataset.tool === a.type || (a.type === 'sig' && b.id === (a.kind === 'ini' ? 'iniBtn' : 'sigBtn')) || (a.type === 'text' && a.text && b.id === 'profBtn' && b.dataset.tool !== 'text'))
    && !(a.type === 'text' && a.text && b.dataset.tool === 'text')));
  $('#hint').textContent = !a ? '' : a.type === 'hl' ? 'Drag across text · Esc when done' : a.every ? 'Click where the initials go — they are added to every page' : a.type === 'check' || a.type === 'cross' || a.kind === 'ini' ? 'Click to place · Esc when done' : 'Click where it goes';
  ghost.hidden = true;
  if (a?.type === 'sig') tinted(a.mask, G.ink).then(t => {
    if (armed !== a || !t || !S) return;
    ghost.src = t.url; ghost.style.width = G.size[a.kind] * S.zoom + 'px'; ghost.style.left = '-999px'; ghost.hidden = false;
  });
}
addEventListener('pointermove', e => { if (!ghost.hidden) { ghost.style.left = e.clientX + 'px'; ghost.style.top = e.clientY + 'px'; } });

function placeText(P, x, y, text, edit) {
  document.activeElement?.blur?.();
  pushUndo();
  const it = addItem({ type: 'text', page: P.n, x, y: y - G.fs * 0.6, fs: G.fs, font: G.font, ink: G.tink, text: text || '' });
  select(it);
  if (edit) setTimeout(() => editText(it, true), 0);
  return it;
}
function placeCheck(P, x, y, mark) {
  pushUndo();
  return addItem({ type: 'check', mark, page: P.n, x: x - 7, y: y - 7, w: 14, h: 14, ink: G.tink });
}
function placeSig(P, x, y, b) {
  pushUndo();
  const w = G.size[b.kind], h = w / b.ar;
  return addItem({ type: 'sig', kind: b.kind, mask: b.id, page: P.n, x: x - w / 2, y: y - h / 2, w, h, rot: jit(b.kind === 'ini' ? 3 : 1.5), ink: G.ink });
}
function placeAt(P, x, y) {
  const a = armed;
  if (a.type === 'text' || a.type === 'date') { arm(null); placeText(P, x, y, a.type === 'date' ? fmtDate(null, G.datefmt) : a.text, a.type === 'text' && !a.text); }
  else if (a.type === 'check' || a.type === 'cross') placeCheck(P, x, y, a.type === 'cross' ? 'x' : 'tick');
  else {
    const b = bank.find(b => b.id === a.mask);
    if (a.every) { arm(null); initialEveryPage(P, x, y, b); return; }
    const it = placeSig(P, x, y, b);
    if (a.kind === 'sig') { arm(null); select(it); }
  }
}

const pagePt = (P, e) => { const r = P.el.getBoundingClientRect(); return { x: (e.clientX - r.left) / S.zoom, y: (e.clientY - r.top) / S.zoom }; };
view.addEventListener('pointerdown', e => {
  if (!S || e.button !== 0 || e.target.closest('.tb')) return;
  const pe = e.target.closest('.page');
  if (!pe) { select(null); return; }
  let P = pe.__P;
  if (armed) { e.preventDefault(); const p = pagePt(P, e); armed.type === 'hl' ? hlDrag(e, P) : placeAt(P, p.x, p.y); return; }
  const ie = e.target.closest('.item');
  if (!ie) { if (!e.target.closest('.fld')) select(null); return; }
  if (ie.classList.contains('editing')) return;
  const it = S.items.find(i => i.id === ie.id);
  document.activeElement?.blur?.();
  if (!S.items.includes(it)) return;
  const wasSel = S.sel === it;
  select(it);
  e.preventDefault();
  if (it.type === 'hl') return;

  // drag to move, or drag the corner handle to resize
  const resize = e.target.classList.contains('handle'), start = pagePt(P, e), o = { ...it }, tw = ie.offsetWidth / S.zoom;
  let moved = false;
  const mv = ev => {
    if (!moved) { if (Math.hypot(ev.clientX - e.clientX, ev.clientY - e.clientY) < 3) return; moved = true; pushUndo(); }
    if (resize) {
      const dx = pagePt(P, ev).x - start.x;
      if (it.type === 'text') G.fs = it.fs = clamp(Math.round(o.fs * (tw + dx) / tw * 2) / 2, 5, 72);
      else { it.w = Math.max(6, o.w + dx); it.h = it.w * o.h / o.w; if (it.type === 'sig') G.size[it.kind] = it.w; }
    } else {
      const r = P.el.getBoundingClientRect();
      if (ev.clientY < r.top && P.n > 1) P = S.pages[P.n - 2];
      else if (ev.clientY > r.bottom && P.n < S.pages.length) P = S.pages[P.n];
      const p = pagePt(P, ev);
      it.page = P.n; it.x = p.x - (start.x - o.x); it.y = p.y - (start.y - o.y);
    }
    drawItem(it);
  };
  const up = () => {
    removeEventListener('pointermove', mv); removeEventListener('pointerup', up);
    if (moved) { savePrefs(); if (resize && it.type === 'text') ie.querySelector('.tb')?.replaceWith(toolbar(it)); }
    else if (wasSel && it.type === 'text' && !resize) editText(it);   // second click on a text box edits it
  };
  addEventListener('pointermove', mv); addEventListener('pointerup', up);
});
view.addEventListener('dblclick', e => {
  if (!S) return;
  const ie = e.target.closest('.item');
  if (ie) { const it = S.items.find(i => i.id === ie.id); if (it?.type === 'text') editText(it); return; }
  const pe = e.target.closest('.page');
  if (!pe || armed || e.target.closest('.fld')) return;
  const p = pagePt(pe.__P, e);
  placeText(pe.__P, p.x, p.y, '', true);
});
document.querySelectorAll('#tools [data-tool]').forEach(b => b.onclick = () => { if (S) arm(armed?.type === b.dataset.tool && !armed.text ? null : { type: b.dataset.tool }); });

/* ---------- highlight / underline by dragging across text ---------- */
function rangeRects(idx, a, b) {
  const out = [];
  for (const s of idx.segs) {
    const i = Math.max(a, s.start), j = Math.min(b, s.start + s.len);
    if (i >= j) continue;
    const f0 = (i - s.start) / s.len, f1 = (j - s.start) / s.len;
    out.push({ x: s.x + s.w * f0, y: s.y, w: s.w * (f1 - f0), h: s.h });
  }
  return out;
}
function mergeRects(rs) {
  const out = [];
  for (const r of rs) {
    const p = out[out.length - 1];
    if (p && Math.abs(p.y - r.y) < r.h * 0.5 && r.x <= p.x + p.w + r.h * 0.7 && r.x >= p.x - 1) {
      const x1 = Math.max(p.x + p.w, r.x + r.w), y0 = Math.min(p.y, r.y), y1 = Math.max(p.y + p.h, r.y + r.h);
      p.w = x1 - p.x; p.y = y0; p.h = y1 - y0;
    } else out.push({ ...r });
  }
  return out;
}
async function hlDrag(e, P) {
  let last = e, live = true;
  const mv = ev => { last = ev; update(); };
  const up = () => { live = false; removeEventListener('pointermove', mv); removeEventListener('pointerup', up); finish(); };
  addEventListener('pointermove', mv); addEventListener('pointerup', up);
  const start = pagePt(P, e);
  let idx = null, it = null;
  const charAt = p => {
    let best = null, bd = 1e9;
    for (const s of idx.segs) {
      const dx = Math.max(s.x - p.x, 0, p.x - (s.x + s.w)), dy = Math.max(s.y - p.y, 0, p.y - (s.y + s.h)), d = dx + dy * 4;
      if (d < bd) { bd = d; best = s; }
    }
    return best ? best.start + (best.w ? clamp(Math.round((p.x - best.x) / best.w * best.len), 0, best.len) : 0) : null;
  };
  const update = () => {
    if (!idx) return;
    const a = charAt(start), b = charAt(pagePt(P, last));
    if (a === null || b === null) return;
    const rects = mergeRects(rangeRects(idx, Math.min(a, b), Math.max(a, b)));
    if (!rects.length) { if (it) { removeItem(it); it = null; } return; }
    const x = Math.min(...rects.map(r => r.x)), y = Math.min(...rects.map(r => r.y));
    if (!it) { pushUndo(); it = addItem({ type: 'hl', page: P.n, x, y, rects, color: G.hl, mode: G.hlMode }); }
    else { Object.assign(it, { x, y, rects }); drawItem(it); }
  };
  const finish = () => { if (idx) update(); };
  idx = await pageIndex(P);
  update();
}

/* ---------- right-click menu ---------- */
const menu = $('#menu');
const hideMenu = () => { menu.hidden = true; };
function buildMenu(entries, sub) {
  const m = el('div', 'menu' + (sub ? ' sub' : ''));
  for (const e of entries) {
    if (e === 'sep') { m.append(el('hr')); continue; }
    if (e.row) {
      const row = el('div', 'mrow');
      for (const c of e.row) { const b = el('div', 'mi big', c.label); b.title = c.title || ''; b.onclick = ev => { ev.stopPropagation(); hideMenu(); c.action(); }; row.append(b); }
      m.append(row); continue;
    }
    const d = el('div', 'mi');
    if (e.img) { const i = new Image(); e.img.then(t => { if (t) i.src = t.url; }); d.classList.add('pic'); d.append(i); }
    else d.append(el('span', 'ml', e.label));
    if (e.hint) d.append(el('span', 'mh', e.hint));
    if (e.sub) { d.classList.add('has'); d.append(el('span', 'arr', '›'), buildMenu(e.sub, true)); }
    if (!e.action) d.classList.add('noact');
    d.onclick = ev => { ev.stopPropagation(); if (e.action) { hideMenu(); e.action(); } };
    m.append(d);
  }
  return m;
}
function showMenu(entries, x, y) {
  hideBank();
  menu.innerHTML = '';
  const m = buildMenu(entries);
  menu.append(m);
  menu.hidden = false;
  const w = m.offsetWidth, h = m.offsetHeight;
  menu.style.left = Math.max(6, Math.min(x, innerWidth - w - 8)) + 'px';
  menu.style.top = Math.max(6, Math.min(y, innerHeight - h - 8)) + 'px';
  menu.classList.toggle('flip', x + w + 250 > innerWidth);
  menu.classList.toggle('up', y > innerHeight * 0.55);
}
function profileEntries(put) {
  const out = [], g0 = profile.groups[0] || { fields: [] }, v = k => (g0.fields.find(f => f.key === k)?.value || '').trim();
  const plain = (label, value) => ({ label, hint: value, action: () => put(value) });
  const has = f => (f.value || '').trim() && (f.label || '').trim();
  const ent = f => /^\d{4}-\d{2}-\d{2}$/.test(f.value)
    ? { label: f.label, hint: fmtDate(f.value, G.datefmt), action: () => put(fmtDate(f.value, G.datefmt)), sub: Object.keys(DATE_FMTS).map(k => ({ label: fmtDate(f.value, k), action: () => put(fmtDate(f.value, k)) })) }
    : plain(f.label, f.value);
  if (v('first') && v('sur')) {
    out.push(plain('Full name', `${v('first')} ${v('sur')}`));
    if (v('second')) out.push(plain('Full names', `${v('first')} ${v('second')} ${v('sur')}`));
    out.push(plain('Initials & surname', `${[v('first'), v('second')].filter(Boolean).map(s => s[0].toUpperCase()).join('')} ${v('sur')}`));
  }
  g0.fields.filter(f => has(f) && !f.addr).forEach(f => out.push(ent(f)));
  const unit = v('unit'), street = [v('streetnr'), v('street')].filter(Boolean).join(' ');
  const local = [unit, street, v('suburb'), v('city'), v('postal')].filter(Boolean), full = [...local, v('province'), v('country')].filter(Boolean);
  if (full.length) {
    const one = full.join(', '), line = t => ({ label: t, action: () => put(t) }), sub = [line(one)];
    if (local.length && local.length < full.length) sub.push(line(local.join(', ')));
    if (full.length > 1) sub.push({ label: 'On separate lines', action: () => put(full.join('\n')) });
    if (street) sub.push(plain('Street address', street));
    sub.push('sep', ...g0.fields.filter(f => f.addr && has(f)).map(ent));
    out.push({ label: 'Address', hint: one, action: () => put(one), sub });
  }
  const others = profile.groups.slice(1).filter(g => g.fields.some(has));
  if (others.length) { if (out.length) out.push('sep'); others.forEach(g => out.push({ label: g.name || 'Group', sub: g.fields.filter(has).map(ent) })); }
  if (out.length) out.push('sep');
  out.push({ label: out.length ? 'Edit profile…' : 'Set up your profile…', action: openProfile });
  return out;
}
function sigEntry(kind, label, place, every) {
  const list = bank.filter(b => b.kind === kind);
  if (!list.length) return { label, hint: 'create…', action: () => openMaker(kind, place) };
  const sub = list.map(b => ({ img: tinted(b.id, G.ink), action: () => place(b) }));
  if (every && S.pages.length > 1) sub.push('sep', { label: 'Every page', hint: `${S.pages.length} pages`, action: () => every(list[list.length - 1]) });
  sub.push('sep', { label: 'New ' + label.toLowerCase() + '…', action: () => openMaker(kind, place) });
  return { label, action: () => place(list[list.length - 1]), sub };
}
function initialEveryPage(P, x, y, b) {   // one set of initials in the same spot on every page
  const it = placeSig(P, x, y, b);
  copyToAll(it);
  select(it);
}
view.addEventListener('contextmenu', e => {
  const pe = e.target.closest('.page');
  if (!S || !pe || e.target.closest('.fld')) return;
  e.preventDefault();
  arm(null);
  document.activeElement?.blur?.();
  const P = pe.__P, p = pagePt(P, e), ie = e.target.closest('.item'), it = ie && S.items.find(i => i.id === ie.id), entries = [];
  if (it) {
    select(it);
    if (it.type === 'text') entries.push({ label: 'Edit text', action: () => editText(it) });
    if (it.type === 'sig' && it.kind === 'ini' && S.pages.length > 1) entries.push({ label: 'Initial all pages', action: () => { pushUndo(); copyToAll(it); } });
    if (it.type !== 'hl') entries.push({ label: 'Copy', hint: '⌘C', action: () => copyItem(it) });
    entries.push({ label: 'Delete', hint: '⌫', action: () => { pushUndo(); removeItem(it); } }, 'sep');
  }
  const date = k => () => { G.datefmt = k; savePrefs(); placeText(P, p.x, p.y, fmtDate(null, k)); };
  entries.push(
    { label: 'Text', action: () => placeText(P, p.x, p.y, '', true) },
    { label: 'Profile', sub: profileEntries(t => placeText(P, p.x, p.y, t)) },
    sigEntry('sig', 'Signature', b => select(placeSig(P, p.x, p.y, b))),
    sigEntry('ini', 'Initials', b => select(placeSig(P, p.x, p.y, b)), b => initialEveryPage(P, p.x, p.y, b)),
    { label: 'Date', hint: fmtDate(null, G.datefmt), action: date(G.datefmt), sub: Object.keys(DATE_FMTS).map(k => ({ label: fmtDate(null, k), action: date(k) })) },
    { row: [{ label: '✓', title: 'Check mark', action: () => select(placeCheck(P, p.x, p.y, 'tick')) }, { label: '✗', title: 'Cross', action: () => select(placeCheck(P, p.x, p.y, 'x')) }] },
  );
  if (clip) entries.push('sep', { label: 'Paste', hint: '⌘V', action: () => pasteItem(P, p.x, p.y) });
  showMenu(entries, e.clientX, e.clientY);
});
$('#profBtn').onclick = e => {
  if (!S) return;
  if (!menu.hidden) return hideMenu();
  const r = e.currentTarget.getBoundingClientRect();
  showMenu(profileEntries(t => arm({ type: 'text', text: t })), r.left, r.bottom + 6);
};
document.addEventListener('pointerdown', e => {
  if (!e.target.closest('#menu,#profBtn')) hideMenu();
  if (!e.target.closest('#bank,#sigBtn,#iniBtn')) hideBank();
});
view.addEventListener('scroll', hideMenu);

/* ---------- profile ---------- */
let pft;
const saveProfile = () => { clearTimeout(pft); pft = setTimeout(() => store.set('profile', profile), 250); };
function openProfile() { renderProfile(); $('#profile').showModal(); }
function renderProfile() {
  const box = $('#profBody');
  box.innerHTML = '';
  profile.groups.forEach((g, gi) => {
    const sec = el('div', 'pgroup'), head = el('div', 'phead'), nm = el('input', 'pname');
    nm.value = g.name; nm.placeholder = 'Group name, e.g. Car'; nm.disabled = gi === 0;
    nm.oninput = () => { g.name = nm.value; saveProfile(); };
    head.append(nm);
    if (gi > 0) { const del = el('button', 'x', 'Remove group'); del.onclick = () => { if (confirm(`Remove “${g.name || 'this group'}” and its details?`)) { profile.groups.splice(gi, 1); saveProfile(); renderProfile(); } }; head.append(del); }
    sec.append(head);
    g.fields.forEach((f, fi) => {
      const row = el('div', 'prow'), lab = el('input', 'plab'), val = el('input', 'pval');
      lab.value = f.label; lab.placeholder = 'Label, e.g. Registration'; lab.readOnly = !!f.key;
      val.value = f.value; if (f.type === 'date') val.type = 'date';
      lab.oninput = () => { f.label = lab.value; saveProfile(); };
      val.oninput = () => { f.value = val.value; saveProfile(); };
      row.append(lab, val);
      const del = el('button', 'x', '✕');
      if (f.key) del.style.visibility = 'hidden';
      else del.onclick = () => { g.fields.splice(fi, 1); saveProfile(); renderProfile(); };
      row.append(del); sec.append(row);
    });
    const add = el('button', 'padd', '+ Add detail');
    add.onclick = () => { g.fields.push({ label: '', value: '' }); renderProfile(); [...box.querySelectorAll('.pgroup')][gi].querySelector('.prow:last-of-type .plab').focus(); };
    sec.append(add); box.append(sec);
  });
}
$('#profAddGroup').onclick = () => { profile.groups.push({ name: '', fields: [{ label: '', value: '' }] }); saveProfile(); renderProfile(); const n = $('#profBody').querySelectorAll('.pname'); n[n.length - 1].focus(); };
$('#profDone').onclick = () => $('#profile').close();
$('#profile').addEventListener('close', () => { clearTimeout(pft); store.set('profile', profile); });
$('#profExport').onclick = () => { const a = el('a'); a.href = URL.createObjectURL(new Blob([JSON.stringify(profile, null, 2)], { type: 'application/json' })); a.download = 'profile.json'; a.click(); };
$('#profImport').onclick = () => $('#jsonFile').click();
function setProfile(p) {
  if (!p || !Array.isArray(p.groups) || !p.groups.every(g => Array.isArray(g.fields))) throw new Error('not a profile file');
  p.groups.forEach(g => { g.name = String(g.name ?? ''); g.fields.forEach(f => { f.label = String(f.label ?? ''); f.value = String(f.value ?? ''); }); });
  profile = migrateProfile(p); store.set('profile', profile);
}
$('#jsonFile').onchange = async e => {
  const f = e.target.files[0]; e.target.value = '';
  if (!f) return;
  try { setProfile(JSON.parse(await f.text())); renderProfile(); } catch (err) { alert('Could not import that file: ' + err.message); }
};

/* ---------- bank popover ---------- */
const bankEl = $('#bank');
let bankKind = 'sig';
const hideBank = () => { bankEl.hidden = true; };
function openBank(kind) {
  if (!S) return;
  hideMenu();
  bankKind = kind;
  const list = bank.filter(b => b.kind === kind), label = kind === 'ini' ? 'initials' : 'signature';
  if (!list.length) return openMaker(kind);
  bankEl.innerHTML = '';
  for (const b of list) {
    const row = el('div', 'sigrow'), img = new Image(), del = el('button', 'del', '✕');
    row.title = 'Use this ' + label; del.title = 'Delete from bank';
    tinted(b.id, G.ink).then(t => { img.src = t.url; });
    del.onclick = e => {
      e.stopPropagation();
      if (!confirm('Delete this saved ' + label + '?')) return;
      bank = bank.filter(x => x !== b); saveBank();
      bank.some(x => x.kind === kind) ? openBank(kind) : hideBank();
    };
    row.onclick = () => { hideBank(); arm({ type: 'sig', kind, mask: b.id }); };
    row.append(img, del); bankEl.append(row);
  }
  const foot = el('div', 'foot');
  foot.append(el('span', 'lbl', 'Pen'));
  for (const k in INKS) {
    const d = el('button', 'dot' + (G.ink === k ? ' on' : ''));
    d.style.background = INKS[k]; d.title = k[0].toUpperCase() + k.slice(1);
    d.onclick = () => { G.ink = k; savePrefs(); openBank(kind); };
    foot.append(d);
  }
  const nw = el('button', '', '+ New');
  nw.onclick = () => openMaker(kind);
  if (kind === 'ini' && S.pages.length > 1) {
    const ev = el('button', '', 'Every page');
    ev.title = 'Click where they go: added in that spot on every page';
    ev.onclick = () => { hideBank(); arm({ type: 'sig', kind, mask: list[list.length - 1].id, every: true }); };
    foot.append(el('span', 'spacer'), ev);
  }
  if (!foot.querySelector('.spacer')) foot.append(el('span', 'spacer'));
  foot.append(nw); bankEl.append(foot);
  const r = $(kind === 'ini' ? '#iniBtn' : '#sigBtn').getBoundingClientRect();
  bankEl.style.left = Math.max(8, Math.min(r.left, innerWidth - 312)) + 'px';
  bankEl.style.top = r.bottom + 6 + 'px';
  bankEl.hidden = false;
}
$('#sigBtn').onclick = () => bankEl.hidden || bankKind !== 'sig' ? openBank('sig') : hideBank();
$('#iniBtn').onclick = () => bankEl.hidden || bankKind !== 'ini' ? openBank('ini') : hideBank();

/* ---------- signature maker: invent, draw or photo ---------- */
const pad = $('#pad'), pctx = pad.getContext('2d', { willReadFrequently: true }), maker = $('#maker'), BASE = 8;
let padKind = 'sig', padSrc = null, dr = null, padThen = null, padHist = [], padRaw = true;
pctx.lineCap = 'round'; pctx.strokeStyle = pctx.fillStyle = '#000';
function openMaker(kind, then) {
  hideBank(); padKind = kind; padThen = then || null; padHist = [];
  $('#makerTitle').textContent = kind === 'ini' ? 'New initials' : 'New signature';
  clearPad(); maker.showModal();
}
function clearPad() { pctx.clearRect(0, 0, pad.width, pad.height); padSrc = null; padRaw = true; $('#cleanWrap').hidden = true; $('#padPrev').disabled = !padHist.length; }
const padPt = e => { const r = pad.getBoundingClientRect(); return { x: (e.clientX - r.left) * pad.width / r.width, y: (e.clientY - r.top) * pad.height / r.height }; };
function curve(a, c, b, w0, w1) {   // quadratic segment with tapering width, like a pen speeding up
  const n = Math.max(2, Math.ceil(Math.hypot(b.x - a.x, b.y - a.y) / 3));
  let px = a.x, py = a.y;
  for (let i = 1; i <= n; i++) {
    const t = i / n, u = 1 - t;
    const x = u * u * a.x + 2 * u * t * c.x + t * t * b.x, y = u * u * a.y + 2 * u * t * c.y + t * t * b.y;
    pctx.lineWidth = w0 + (w1 - w0) * t;
    pctx.beginPath(); pctx.moveTo(px, py); pctx.lineTo(x, y); pctx.stroke();
    px = x; py = y;
  }
}
pad.onpointerdown = e => {
  if (padSrc || !padRaw) clearPad();
  pad.setPointerCapture(e.pointerId);
  const p = padPt(e);
  dr = { p, mid: p, w: BASE * 0.7, t: e.timeStamp, moved: false };
};
pad.onpointermove = e => {
  if (!dr) return;
  const evs = e.getCoalescedEvents?.() || [];
  for (const ev of evs.length ? evs : [e]) {
    const p = padPt(ev), d = Math.hypot(p.x - dr.p.x, p.y - dr.p.y);
    if (d < 2) continue;
    const dt = (ev.timeStamp - dr.t) || 8;
    const target = ev.pointerType === 'pen' && ev.pressure > 0 ? BASE * (0.35 + 1.5 * ev.pressure) : BASE * clamp(1.35 - (d / dt) * 0.22, 0.38, 1.35);
    const w = dr.w + (target - dr.w) * 0.35, mid = { x: (p.x + dr.p.x) / 2, y: (p.y + dr.p.y) / 2 };
    curve(dr.mid, dr.p, mid, dr.w, w);
    dr = { p, mid, w, t: ev.timeStamp, moved: true };
  }
};
pad.onpointerup = pad.onpointercancel = () => {
  if (!dr) return;
  if (dr.moved) curve(dr.mid, dr.p, dr.p, dr.w, dr.w * 0.5);
  else { pctx.beginPath(); pctx.arc(dr.p.x, dr.p.y, BASE * 0.5, 0, 7); pctx.fill(); }
  dr = null;
};

// Uneven ink: real pens never lay down a perfectly solid line.
function grain(ctx, W, H) {
  const d = ctx.getImageData(0, 0, W, H), a = d.data, C = 9, gw = Math.ceil(W / C) + 2, gh = Math.ceil(H / C) + 2, n = new Float32Array(gw * gh);
  for (let i = 0; i < n.length; i++) n[i] = Math.random();
  for (let y = 0; y < H; y++) for (let x = 0; x < W; x++) {
    const i = (y * W + x) * 4 + 3;
    if (!a[i]) continue;
    const gx = x / C, gy = y / C, x0 = gx | 0, y0 = gy | 0, fx = gx - x0, fy = gy - y0, o = y0 * gw + x0;
    const v = (n[o] * (1 - fx) + n[o + 1] * fx) * (1 - fy) + (n[o + gw] * (1 - fx) + n[o + gw + 1] * fx) * fy;
    a[i] *= 0.8 + 0.2 * v - Math.random() * 0.07;
  }
  ctx.putImageData(d, 0, 0);
}

// Invent a scribble signature: a tall opening letter, a run of humps and loops that die away, and a flick.
function invent(kind) {
  const r = (a, b) => a + Math.random() * (b - a), chance = p => Math.random() < p;
  const base = 380, cap = r(200, 270), slant = r(0.15, 0.45), wob = r(4, 11);
  const P = (x, up) => ({ x: x + up * slant + jit(wob), y: base - up + jit(wob) });
  const strokes = [];
  let x = 300;
  // first letter: a stem, then an arm that kicks in and out (a loose "K")
  strokes.push([P(x, cap * r(0.95, 1.1)), P(x + r(-8, 8), cap * 0.5), P(x + r(-14, 6), r(-30, 5))]);
  const s = [P(x + cap * r(0.4, 0.65), cap * r(0.7, 1)), P(x + r(6, 26), cap * r(0.34, 0.5)), P(x + cap * r(0.36, 0.5), r(-8, 14))];
  x += cap * 0.5;
  const hump = (step, amp) => { s.push(P(x + step * r(0.35, 0.55), amp * r(0.8, 1.15)), P(x + step, r(-10, 14))); x += step; };
  const loop = (step, h) => { s.push(P(x + step * r(0.7, 1), h), P(x + step * r(0.15, 0.4), h * r(0.72, 0.9)), P(x + step * r(0.6, 0.85), r(-10, 12))); x += step * 0.8; };
  let step = r(52, 84), amp = cap * r(0.28, 0.42);
  if (kind === 'ini') {
    // second letter joined on, then out
    s.push(P(x + cap * r(0.3, 0.45), cap * r(0.95, 1.15)), P(x + cap * r(0.28, 0.42), r(-12, 8)), P(x + cap * r(0.75, 0.95), cap * r(0.5, 0.68)), P(x + cap * r(0.5, 0.62), cap * r(0.28, 0.4)), P(x + cap * r(1, 1.2), r(-14, 6)));
    x += cap * 1.1;
    if (chance(0.7)) s.push(P(x + r(60, 150), r(10, 70)));
  } else {
    for (let i = Math.round(r(1, 3)); i--;) { hump(step, amp); amp *= r(0.8, 0.95); }
    if (chance(0.75)) loop(step, cap * r(0.8, 1.05));
    if (chance(0.5)) hump(step, amp);
    // second name: a tall stroke and a kick, then a zigzag that fades out
    s.push(P(x + step * r(0.2, 0.4), cap * r(0.85, 1.15)), P(x + step * r(0.15, 0.35), r(-12, 10)));
    if (chance(0.7)) s.push(P(x + step * r(0.9, 1.1), cap * r(0.4, 0.58)), P(x + step * r(0.5, 0.65), cap * r(0.26, 0.34)), P(x + step * r(1.15, 1.35), r(-8, 10)));
    x += step * 1.3;
    let s2 = r(34, 56), a2 = cap * r(0.16, 0.3);
    for (let i = Math.round(r(2, 5)); i--;) { hump(s2, a2); a2 *= r(0.7, 0.92); s2 *= r(0.9, 1.1); }
    const tail = Math.random();
    if (tail < 0.4) s.push(P(x + r(110, 230), r(15, 95)));                                   // flick up and away
    else if (tail < 0.7) s.push(P(x + r(170, 300), r(-12, 22)));                             // long flat exit
    else s.push(P(x + r(40, 80), -r(35, 70)), P(x - r(260, 520), -r(45, 85)));              // drop and sweep back under
    if (tail < 0.7 && chance(0.35)) strokes.push([P(x - r(380, 560), -r(40, 70)), P(x - r(100, 200), -r(48, 80)), P(x + r(60, 160), -r(30, 60))]);   // separate underline
  }
  strokes.splice(1, 0, s);

  // control points -> smooth pen path; long stretches are fast and thin, tight turns are slow and heavy
  const paths = strokes.map(c => {
    const out = [];
    for (let i = 0; i < c.length - 1; i++) {
      const a = c[i - 1] || c[i], b = c[i], d = c[i + 1], e = c[i + 2] || d, len = Math.hypot(d.x - b.x, d.y - b.y), n = Math.max(4, Math.ceil(len / 5));
      for (let j = 0; j < n; j++) {
        const t = j / n, t2 = t * t, t3 = t2 * t, f = k => 0.5 * (2 * b[k] + (-a[k] + d[k]) * t + (2 * a[k] - 5 * b[k] + 4 * d[k] - e[k]) * t2 + (-a[k] + 3 * b[k] - 3 * d[k] + e[k]) * t3);
        out.push({ x: f('x'), y: f('y'), v: len });
      }
    }
    out.push({ ...c[c.length - 1], v: 0 });
    const avg = out.reduce((s, p) => s + p.v, 0) / out.length || 1;
    let w = BASE * 0.9;
    out.forEach((p, i) => {
      const q = out[i - 1], n = out[i + 1];
      let turn = 0;
      if (q && n) { turn = Math.abs(Math.atan2(n.y - p.y, n.x - p.x) - Math.atan2(p.y - q.y, p.x - q.x)); if (turn > Math.PI) turn = 2 * Math.PI - turn; }
      const target = BASE * clamp(1.28 - 0.42 * p.v / avg + turn * 2.2, 0.36, 1.4);
      w += (target - w) * 0.16;
      const k = i / out.length;
      p.w = w * (k < 0.05 ? 0.6 + 8 * k : k > 0.86 ? 1 - (k - 0.86) / 0.14 * 0.78 : 1);
    });
    return out;
  });
  // fit to the pad
  const all = paths.flat(), W = pad.width, H = pad.height;
  const x0 = Math.min(...all.map(p => p.x)), x1 = Math.max(...all.map(p => p.x)), y0 = Math.min(...all.map(p => p.y)), y1 = Math.max(...all.map(p => p.y));
  const k = Math.min(1.15, (W - 160) / (x1 - x0), (H - 120) / (y1 - y0)), ox = (W - (x1 - x0) * k) / 2 - x0 * k, oy = (H - (y1 - y0) * k) / 2 - y0 * k;
  pctx.clearRect(0, 0, W, H);
  for (const path of paths) for (let i = 1; i < path.length; i++) {
    const a = path[i - 1], b = path[i];
    pctx.lineWidth = (a.w + b.w) / 2;
    pctx.beginPath(); pctx.moveTo(a.x * k + ox, a.y * k + oy); pctx.lineTo(b.x * k + ox, b.y * k + oy); pctx.stroke();
  }
  grain(pctx, W, H);
}
$('#padInvent').onclick = () => {
  const cur = pctx.getImageData(0, 0, pad.width, pad.height);
  if (cur.data.some((v, i) => i % 4 === 3 && v)) { padHist.push(cur); if (padHist.length > 12) padHist.shift(); }
  padSrc = null; $('#cleanWrap').hidden = true;
  invent(padKind);
  padRaw = false;
  $('#padPrev').disabled = !padHist.length;
};
$('#padPrev').onclick = () => { const h = padHist.pop(); if (h) { pctx.putImageData(h, 0, 0); padSrc = null; padRaw = false; } $('#padPrev').disabled = !padHist.length; };

function processUpload() {   // photo of ink on paper -> ink only, transparent background
  const W = pad.width, H = pad.height, s = Math.min(W / padSrc.width, H / padSrc.height);
  const w = Math.floor(padSrc.width * s), h = Math.floor(padSrc.height * s), x = (W - w) >> 1, y = (H - h) >> 1;
  pctx.clearRect(0, 0, W, H);
  pctx.fillStyle = '#fff'; pctx.fillRect(x, y, w, h); pctx.fillStyle = '#000';
  pctx.drawImage(padSrc, x, y, w, h);
  const d = pctx.getImageData(x, y, w, h), a = d.data, n = w * h, lum = new Uint8Array(n), hist = new Uint32Array(256);
  for (let i = 0; i < n; i++) hist[lum[i] = a[i * 4] * 0.299 + a[i * 4 + 1] * 0.587 + a[i * 4 + 2] * 0.114]++;
  const pct = p => { let c = 0; for (let v = 0; v < 256; v++) { c += hist[v]; if (c >= n * p) return v; } return 255; };
  const paper = pct(0.6), ink = pct(0.005), k = $('#clean').value / 100;
  const hi = paper - (paper - ink) * (0.08 + 0.55 * k), lo = ink + (hi - ink) * 0.35;
  for (let i = 0; i < n; i++) {
    a[i * 4] = a[i * 4 + 1] = a[i * 4 + 2] = 0;
    a[i * 4 + 3] = 255 * clamp((hi - lum[i]) / Math.max(1, hi - lo), 0, 1);
  }
  pctx.putImageData(d, x, y);
}
$('#padClear').onclick = clearPad;
$('#padCancel').onclick = () => maker.close();
$('#padUpload').onclick = () => $('#imgFile').click();
$('#clean').oninput = () => { if (padSrc) processUpload(); };
$('#imgFile').onchange = async e => {
  const f = e.target.files[0]; e.target.value = '';
  if (!f) return;
  try { padSrc = await createImageBitmap(f); } catch { return alert('Could not read that image.'); }
  $('#cleanWrap').hidden = false; padRaw = false;
  processUpload();
};
$('#padSave').onclick = () => {
  const W = pad.width, H = pad.height;
  if (padRaw) { grain(pctx, W, H); padRaw = false; }   // hand-drawn: add the same ink texture
  const d = pctx.getImageData(0, 0, W, H).data;
  let x0 = W, y0 = H, x1 = -1, y1 = -1;
  for (let y = 0; y < H; y++) for (let x = 0; x < W; x++) if (d[(y * W + x) * 4 + 3] > 24) {
    if (x < x0) x0 = x; if (x > x1) x1 = x; if (y < y0) y0 = y; if (y > y1) y1 = y;
  }
  if (x1 < 0) return alert('Nothing to save yet — invent one or draw first.');
  x0 = Math.max(0, x0 - 6); y0 = Math.max(0, y0 - 6); x1 = Math.min(W - 1, x1 + 6); y1 = Math.min(H - 1, y1 + 6);
  const c = mkCanvas(x1 - x0 + 1, y1 - y0 + 1);
  c.getContext('2d').drawImage(pad, -x0, -y0);
  const b = { id: uid(), kind: padKind, mask: c.toDataURL('image/png'), ar: c.width / c.height };
  bank.push(b); saveBank(); maskOf(b.id, b.mask);
  maker.close();
  if (padThen) padThen(b);
  else if (S) arm({ type: 'sig', kind: padKind, mask: b.id });
};

/* ---------- search ---------- */
async function pageIndex(P) {
  if (P.idx) return P.idx;
  const tc = await P.page.getTextContent(), segs = [];
  let str = '';
  for (const i of tc.items) {
    if (i.str) {
      const m = pdfjsLib.Util.transform(P.vp.transform, i.transform), fh = Math.hypot(m[2], m[3]);
      segs.push({ start: str.length, len: i.str.length, str: i.str, x: m[4], y: m[5] - fh * 0.85, w: i.width, h: fh * 1.1 });
      str += i.str;
    }
    if (i.hasEOL) str += ' ';
  }
  return P.idx = { str: str.toLowerCase(), segs };
}
async function runSearch() {
  const D = S;
  if (!D) return;
  const q = D.q = $('#q').value.trim().toLowerCase(), token = ++D.searchToken;
  D.el.querySelectorAll('.hl').forEach(h => h.innerHTML = '');
  D.hits = []; D.hit = -1; $('#qcount').textContent = '';
  D.q = $('#q').value;
  if (!q) return;
  const hits = [];
  for (const P of D.pages) {
    const t = await pageIndex(P);
    if (token !== D.searchToken) return;
    const layer = P.el.querySelector('.hl');
    for (let i = t.str.indexOf(q); i !== -1; i = t.str.indexOf(q, i + q.length)) {
      const els = rangeRects(t, i, i + q.length).map(r => {
        const e = el('i');
        e.style.cssText = `--x:${r.x};--y:${r.y};--iw:${r.w};--ih:${r.h}`;
        layer.append(e);
        return e;
      });
      if (els.length) hits.push(els);
    }
  }
  D.hits = hits;
  if (D === S) { $('#qcount').textContent = hits.length ? '' : '0'; if (hits.length) goHit(0); }
}
function goHit(i) {
  const n = S?.hits.length;
  if (!n) return;
  S.hits[S.hit]?.forEach(e => e.classList.remove('cur'));
  S.hit = (i + n) % n;
  S.hits[S.hit].forEach(e => e.classList.add('cur'));
  S.hits[S.hit][0].scrollIntoView({ block: 'center', inline: 'nearest' });
  $('#qcount').textContent = `${S.hit + 1}/${n}`;
}
let qt;
$('#q').addEventListener('input', () => { clearTimeout(qt); qt = setTimeout(runSearch, 150); });
$('#q').addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); goHit(S.hit + (e.shiftKey ? -1 : 1)); } });
$('#qnext').onclick = () => S && goHit(S.hit + 1);
$('#qprev').onclick = () => S && goHit(S.hit - 1);

/* ---------- export ---------- */
const mctx = mkCanvas(1, 1).getContext('2d'), blCache = {};
function baseline(font) {   // where the browser puts the first baseline, as a fraction of font size (line-height 1.2)
  if (blCache[font] == null) {
    mctx.font = `100px ${FONTS[font].css}`;
    const m = mctx.measureText('Hg'), a = m.fontBoundingBoxAscent ?? 90, d = m.fontBoundingBoxDescent ?? 22;
    blCache[font] = ((120 - (a + d)) / 2 + a) / 100;
  }
  return blCache[font];
}
async function buildPdf(D = S) {
  const { PDFDocument, StandardFonts, rgb, degrees, LineCapStyle, BlendMode, PDFTextField, PDFCheckBox, PDFRadioGroup, PDFDropdown, PDFOptionList } = PDFLib;
  const notes = [];
  let doc, raster = false;
  try { doc = await PDFDocument.load(D.bytes); }
  catch (e) {
    if (!/encrypt/i.test(e.message || '')) throw e;
    if (!confirm('This PDF is password-locked. I can save an unlocked copy, but its pages become images (the text will no longer be selectable). Continue?')) return null;
    raster = true;
    doc = await PDFDocument.create();
    for (const P of D.pages) {
      const vp = P.page.getViewport({ scale: 2.5 }), c = mkCanvas(vp.width, vp.height);
      await P.page.render({ canvasContext: c.getContext('2d'), viewport: vp, annotationMode: pdfjsLib.AnnotationMode.ENABLE_STORAGE }).promise;
      const jpg = await doc.embedJpg(c.toDataURL('image/jpeg', 0.9));
      doc.addPage([P.vp.width, P.vp.height]).drawImage(jpg, { x: 0, y: 0, width: P.vp.width, height: P.vp.height });
    }
  }

  if (!raster && D.dirty.size) {
    const form = doc.getForm();
    for (const name of D.dirty) {
      const els = fieldEls(name, D), f = form.getFieldMaybe(name);
      try {
        if (!f) throw new Error('not found');
        if (f instanceof PDFTextField) { if (f.getMaxLength() !== undefined && els[0].value.length > f.getMaxLength()) f.setMaxLength(undefined); f.setText(els[0].value || undefined); }
        else if (f instanceof PDFCheckBox) els[0].checked ? f.check() : f.uncheck();
        else if (f instanceof PDFRadioGroup) { const c = els.find(e => e.checked); if (c) f.select(c.value); }
        else if (f instanceof PDFDropdown || f instanceof PDFOptionList) els[0].value ? f.select(els[0].value) : f.clear();
      } catch (e) { notes.push(`Field "${name}" could not be saved (${e.message}).`); }
    }
  }

  const fonts = {};
  const hex = h => rgb(parseInt(h.slice(1, 3), 16) / 255, parseInt(h.slice(3, 5), 16) / 255, parseInt(h.slice(5, 7), 16) / 255);
  const ordered = [...D.items].sort((a, b) => (b.type === 'hl') - (a.type === 'hl'));   // highlights go underneath
  for (const it of ordered) {
    const P = D.pages[it.page - 1], page = doc.getPage(it.page - 1), rot = degrees(raster ? 0 : P.vp.rotation);
    const pt = (x, y) => { const [px, py] = raster ? [x, P.vp.height - y] : P.vp.convertToPdfPoint(x, y); return { x: px, y: py }; };
    const image = async (canvas, x, y, w, h) => page.drawImage(await doc.embedPng(canvas.toDataURL('image/png')), { ...pt(x, y + h), width: w, height: h, rotate: rot });
    if (it.type === 'hl') {
      const [fill, line] = HL[it.color];
      for (const r of it.rects) {
        if (it.mode === 'ul') page.drawLine({ start: pt(r.x, r.y + r.h * 0.9), end: pt(r.x + r.w, r.y + r.h * 0.9), thickness: Math.max(0.7, r.h * 0.07), color: hex(line) });
        else page.drawRectangle({ ...pt(r.x, r.y + r.h), width: r.w, height: r.h, rotate: rot, color: hex(fill), opacity: 0.45, blendMode: BlendMode.Multiply });
      }
      continue;
    }
    const color = hex(INKS[it.ink] || INKS.blue);
    if (it.type === 'sig') {
      const t = await tinted(it.mask, it.ink);
      if (!t) continue;
      const m = 0.2 * Math.max(it.w, it.h), k = clamp(t.c.width / it.w, 2, 6);   // px per point
      const c = mkCanvas((it.w + 2 * m) * k, (it.h + 2 * m) * k), x = c.getContext('2d');
      x.translate(c.width / 2, c.height / 2); x.rotate(it.rot * Math.PI / 180);
      x.imageSmoothingQuality = 'high';
      x.drawImage(t.c, -it.w * k / 2, -it.h * k / 2, it.w * k, it.h * k);
      await image(c, it.x - m, it.y - m, it.w + 2 * m, it.h + 2 * m);
    } else if (it.type === 'check') {
      const segs = it.mark === 'x' ? [[.15, .15, .85, .85], [.85, .15, .15, .85]] : [[.08, .55, .36, .88], [.36, .88, .92, .12]];
      for (const s of segs) page.drawLine({ start: pt(it.x + s[0] * it.w, it.y + s[1] * it.h), end: pt(it.x + s[2] * it.w, it.y + s[3] * it.h), thickness: it.w * 0.11, color, lineCap: LineCapStyle.Round });
    } else if (it.text) {
      const fk = FONTS[it.font] ? it.font : 'helv', F = FONTS[fk], bl = baseline(fk), lines = it.text.split('\n');
      let font = null;
      if (F.std) {
        font = fonts[fk] = fonts[fk] || await doc.embedFont(StandardFonts[F.std]);
        try { lines.forEach(l => font.encodeText(l)); } catch { font = null; }
      }
      if (font) lines.forEach((l, i) => page.drawText(l, { ...pt(it.x, it.y + it.fs * (bl + i * 1.2)), size: it.fs, font, color, rotate: rot }));
      else {   // handwriting font, or characters the standard fonts can't encode: draw as a crisp image
        const k = 6, f = `${it.fs * k}px ${F.css}`;
        mctx.font = f;
        const padX = it.fs * 0.3, w = Math.max(...lines.map(l => mctx.measureText(l).width)) / k + padX * 2, h = lines.length * it.fs * 1.2 + it.fs * 0.3;
        const c = mkCanvas(w * k, h * k), x = c.getContext('2d');
        x.font = f; x.fillStyle = INKS[it.ink] || INKS.blue;
        lines.forEach((l, i) => x.fillText(l, padX * k, it.fs * (bl + i * 1.2) * k));
        await image(c, it.x - padX, it.y, w, h);
      }
    }
  }
  let bytes;
  try { bytes = await doc.save(); }
  catch (e) {   // usually a form value with characters the field's font can't draw
    doc.getForm().acroForm.dict.set(PDFLib.PDFName.of('NeedAppearances'), PDFLib.PDFBool.True);
    bytes = await doc.save({ updateFieldAppearances: false });
    notes.push('Some form values use unusual characters; they are saved but may only show in some PDF viewers.');
  }
  return { bytes, notes };
}
async function exportPdf(D = S) {
  if (!D) return false;
  document.activeElement?.blur?.();
  if (D === S) select(null);
  const name = D.name + '-signed.pdf';
  if (D.src && store.server) {   // opened with `signet open`: save next to the original file
    let out;
    try { out = await buildPdf(D); } catch (e) { console.error(e); alert('Could not create the PDF: ' + (e.message || e)); return false; }
    if (!out) return false;
    const r = await fetch('api/save/' + encodeURIComponent(D.src), { method: 'POST', body: out.bytes }).catch(() => null);
    if (r?.ok) {
      const { path } = await r.json();
      D.changed = false; renderTabs();
      alert(['Saved: ' + path, ...out.notes].join('\n'));
      return true;
    }
  }
  let handle = null;
  if (window.showSaveFilePicker) {   // a real "Save as" dialog where the browser has one
    try { handle = await showSaveFilePicker({ suggestedName: name, types: [{ description: 'PDF', accept: { 'application/pdf': ['.pdf'] } }] }); }
    catch (e) { if (e.name === 'AbortError') return false; }
  }
  let out;
  try { out = await buildPdf(D); } catch (e) { console.error(e); alert('Could not create the PDF: ' + (e.message || e)); return false; }
  if (!out) return false;
  if (handle) { const w = await handle.createWritable(); await w.write(out.bytes); await w.close(); }
  else {
    const a = el('a');
    a.href = URL.createObjectURL(new Blob([out.bytes], { type: 'application/pdf' }));
    a.download = name;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 60000);
  }
  D.changed = false; renderTabs();
  if (out.notes.length) alert(out.notes.join('\n'));
  return true;
}
$('#save').onclick = () => exportPdf();

/* ---------- file open, drag-drop, keyboard ---------- */
const openFiles = async files => { for (const f of files) if (/pdf$/i.test(f.type) || /\.pdf$/i.test(f.name)) await openBytes(await f.arrayBuffer(), f.name); };
$('#open').onclick = () => $('#file').click();
$('#file').onchange = e => { const fs = [...e.target.files]; e.target.value = ''; openFiles(fs); };
addEventListener('dragover', e => { e.preventDefault(); document.body.classList.add('dragover'); });
addEventListener('dragleave', () => document.body.classList.remove('dragover'));
addEventListener('drop', e => { e.preventDefault(); document.body.classList.remove('dragover'); openFiles([...e.dataTransfer.files]); });
addEventListener('beforeunload', e => { if (docs.some(d => d.changed)) e.preventDefault(); });
addEventListener('keydown', e => {
  const mod = e.metaKey || e.ctrlKey, t = e.target, typing = /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName) || t.isContentEditable;
  if (mod && e.key === 'f') { e.preventDefault(); $('#q').focus(); $('#q').select(); return; }
  if (mod && e.key === 's') { e.preventDefault(); exportPdf(); return; }
  if (mod && e.key === 'o') { e.preventDefault(); $('#file').click(); return; }
  if (mod && (e.key === '=' || e.key === '+')) { e.preventDefault(); $('#zin').click(); return; }
  if (mod && e.key === '-') { e.preventDefault(); $('#zout').click(); return; }
  if (mod && e.key === '0') { e.preventDefault(); S && setZoom(fitZoom()); return; }
  if (document.querySelector('dialog[open]')) return;
  if (e.key === 'Escape') { if (typing) t.blur(); arm(null); hideBank(); hideMenu(); return; }
  if (typing) return;
  if (!S) { if (!mod && e.key.toLowerCase() === 'o') $('#file').click(); return; }
  if (mod && e.key.toLowerCase() === 'z') { e.preventDefault(); e.shiftKey ? redo() : undo(); return; }
  if (mod && e.key === 'y') { e.preventDefault(); redo(); return; }
  if (mod && e.key === 'c' && S.sel && !String(getSelection())) { if (copyItem()) e.preventDefault(); return; }
  if (mod && e.key === 'x' && S.sel) { if (copyItem()) { e.preventDefault(); pushUndo(); removeItem(S.sel); } return; }
  if (mod && e.key === 'v' && clip) { e.preventDefault(); pasteItem(); return; }
  if (mod && e.key === 'd' && S.sel) { e.preventDefault(); const keep = clip; if (copyItem()) { mouse = { x: -1, y: -1 }; pasteItem(); } clip = keep; return; }
  if (mod || e.altKey) return;
  const it = S.sel;
  if (it && (e.key === 'Delete' || e.key === 'Backspace')) { e.preventDefault(); pushUndo(); removeItem(it); }
  else if (it && it.type !== 'hl' && e.key.startsWith('Arrow')) {
    e.preventDefault(); pushUndo();
    const up = e.key === 'ArrowUp', down = e.key === 'ArrowDown';
    if ((up || down) && !e.shiftKey) { scaleItem(it, up ? 1 : -1); savePrefs(); }   // ↑ ↓ resize
    else { const d = e.shiftKey ? 5 : 1; if (e.key === 'ArrowLeft') it.x -= d; else if (e.key === 'ArrowRight') it.x += d; else if (up) it.y -= d; else it.y += d; }
    drawItem(it);
    document.getElementById(it.id).querySelector('.tb')?.replaceWith(toolbar(it));
  } else if (it && it.type === 'text' && e.key === 'Enter') { e.preventDefault(); editText(it); }
  else switch (e.key.toLowerCase()) {
    case 't': arm({ type: 'text' }); break;
    case 'c': arm({ type: 'check' }); break;
    case 'x': arm({ type: 'cross' }); break;
    case 'd': arm({ type: 'date' }); break;
    case 'h': arm({ type: 'hl' }); break;
    case 's': openBank('sig'); break;
    case 'i': openBank('ini'); break;
    case 'p': $('#profBtn').click(); break;
  }
});

/* ---------- scripting surface (what an AI or CLI will drive later) ---------- */
window.signet = {
  get state() { return S; },
  get docs() { return docs; },
  get profile() { return profile; },
  setProfile,
  open: openBytes,
  async inspect() {
    const r1 = v => Math.round(v * 10) / 10, pages = [];
    for (const P of S.pages) pages.push({
      page: P.n, width: r1(P.vp.width), height: r1(P.vp.height), rotation: P.vp.rotation,
      fields: P.fields.map(f => ({ name: f.name, type: f.type, x: r1(f.x), y: r1(f.y), w: r1(f.w), h: r1(f.h), value: f.type === 'checkbox' || f.type === 'radio' ? f.el.checked : f.el.value })),
      text: (await pageIndex(P)).segs.filter(s => s.str.trim()).map(s => ({ str: s.str, x: r1(s.x), y: r1(s.y), w: r1(s.w), h: r1(s.h) })),
    });
    return { name: S.name, units: 'pt, origin top-left', pages, items: S.items, bank: bank.map(b => ({ id: b.id, kind: b.kind, ar: b.ar })) };
  },
  setField,
  add(it) {
    pushUndo();
    if (it.type === 'sig') { const b = bank.find(b => b.id === it.mask); it = { kind: b.kind, rot: jit(1.5), ink: G.ink, w: G.size[b.kind], ...it }; it.h = it.h || it.w / b.ar; }
    if (it.type === 'check') it = { mark: 'tick', w: 14, h: 14, ...it };
    if (it.type === 'text') it = { fs: G.fs, font: G.font, ...it };
    return addItem(it);
  },
  remove: id => { const it = S.items.find(i => i.id === id); if (it) { pushUndo(); removeItem(it); } },
  build: buildPdf,
  download: exportPdf,
};

/* ---------- start ---------- */
(async () => {
  await store.init();
  bank = store.get('bank', []);
  bank.forEach(b => { if (!INKS[b.ink]) delete b.ink; maskOf(b.id, b.mask); });
  profile = migrateProfile(store.get('profile', null) || { groups: [] });
  renderTabs();
  const qs = new URLSearchParams(location.search);
  for (const id of (qs.get('doc') || '').split(',').filter(Boolean)) {
    try {
      const r = await fetch('api/doc/' + encodeURIComponent(id));
      if (!r.ok) throw new Error(r.status === 401 ? 'this window has expired — open the PDF with Signet again' : 'the file is no longer available — open it again');
      await openBytes(await r.arrayBuffer(), decodeURIComponent(r.headers.get('X-Name') || 'document.pdf'), id);
    } catch (e) { alert('Could not open the PDF: ' + e.message); }
  }
  const f = qs.get('file');
  if (f) fetch(f).then(r => r.arrayBuffer()).then(b => openBytes(b, f.split('/').pop())).catch(e => alert('Could not load ' + f + ': ' + e.message));
})();
