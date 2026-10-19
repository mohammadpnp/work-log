'use strict';

const $ = (selector) => document.querySelector(selector);
const fa = (number) => Number(number).toLocaleString('fa-IR');
const escapeHTML = (value) => String(value ?? '').replace(/[&<>"']/g, (char) => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[char]));
const day = (value) => new Intl.DateTimeFormat('fa-IR', {dateStyle:'medium', timeZone:'Asia/Tehran'}).format(new Date(value));
let state = {projects:[], hooksEnabled:false};
let view = 'inbox';
let period = 'week';
let inbox = [];
let report = [];
let reportLayout = 'cards';
let reportPage = 0;
let selectedCommit = null;
let toastTimer;
let loading = false;

async function api(path, body) {
  const options = body === undefined ? {} : {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(body)};
  const response = await fetch(`/api/${path}`, options);
  if (!response.ok) throw new Error(await response.text());
  return response.json();
}

function toast(message, error = false) {
  clearTimeout(toastTimer);
  $('#toast').textContent = message;
  $('#toast').classList.toggle('error', error);
  $('#toast').hidden = false;
  toastTimer = setTimeout(() => { $('#toast').hidden = true; }, error ? 9000 : 4500);
}

function showWarnings(items = []) {
  $('#warnings').hidden = !items.length;
  $('#warnings').textContent = items.length ? `این موارد نیاز به بررسی دارند:\n${items.join('\n')}` : '';
}

async function busy(button, action) {
  if (button.disabled) return;
  button.disabled = true;
  try { await action(); } catch (error) { toast(error.message, true); }
  finally { button.disabled = false; }
}

function projectOptions(select) {
  const previous = select.value;
  select.innerHTML = '<option value="">همهٔ پروژه‌ها</option>' + state.projects.map(p => `<option value="${p.id}">${escapeHTML(p.name)}</option>`).join('');
  if ([...select.options].some(option => option.value === previous)) select.value = previous;
}

function updateState() {
  $('#nav-count').textContent = fa(state.pending);
  $('#stat-pending').textContent = fa(state.pending);
  $('#stat-done').textContent = fa(state.documented);
  $('#stat-projects').textContent = fa(state.projects.length);
  $('#today').textContent = day(`${state.today}T12:00:00+03:30`);
  $('#setup-banner').hidden = state.hooksEnabled;
  $('#watch-status').textContent = state.hooksEnabled ? '● گوش‌به‌زنگِ کامیت‌های تازه' : '○ ثبت خودکار غیرفعال';
  $('#watch-status').classList.toggle('enabled', state.hooksEnabled);
  $('#toggle-hooks').textContent = state.hooksEnabled ? 'غیرفعال کردن ثبت خودکار' : 'فعال کردن ثبت خودکار';
  projectOptions($('#inbox-project'));
  projectOptions($('#report-project'));
  renderProjects();
}

async function refresh() {
  if (loading) return;
  loading = true;
  try {
    state = await api('state');
    updateState();
    if (view === 'inbox') await loadInbox();
    if (view === 'reports') await loadReport();
  } finally { loading = false; }
}

function emptyState(title, text, action = '') {
  return `<div class="empty-state"><img class="meme-gif typing-gif" src="/typing-cat.gif" alt="میم گربهٔ سیاه در حال تایپ روی لپ‌تاپ" width="200" height="126"><h3>${escapeHTML(title)}</h3><p>${escapeHTML(text)}</p>${action}</div>`;
}

function commitCard(c, documented = false) {
  return `<article class="commit-card"><span class="commit-icon" aria-hidden="true">${documented ? '✓' : '⑂'}</span><div class="commit-content"><div class="commit-topline"><span class="project-tag">${escapeHTML(c.project)}</span><time datetime="${escapeHTML(c.date)}">${day(c.date)}</time><code>${escapeHTML(c.hash.slice(0,8))}</code></div><h3 dir="auto">${escapeHTML(c.subject)}</h3><p class="commit-meta"><span class="branch-name">${escapeHTML(c.branch || 'تاریخچه')}</span> · ${escapeHTML(c.author)}</p>${documented ? `<p class="note-preview" dir="auto">${escapeHTML(c.note)}</p>` : ''}</div><button class="button ${documented ? 'secondary' : 'primary'}" data-note="${c.id}">${documented ? 'ویرایش توضیح' : 'نوشتن توضیح ✎'}</button></article>`;
}

async function loadInbox() {
  const query = new URLSearchParams({status:'pending'});
  if ($('#inbox-project').value) query.set('project', $('#inbox-project').value);
  inbox = await api(`commits?${query}`);
  renderInbox();
}

function renderInbox() {
  const search = $('#inbox-search').value.trim().toLocaleLowerCase();
  const filtered = inbox.filter(c => `${c.subject} ${c.body} ${c.project} ${c.hash}`.toLocaleLowerCase().includes(search));
  $('#inbox-count').textContent = fa(filtered.length);
  if (filtered.length) { $('#inbox-list').innerHTML = filtered.map(c => commitCard(c)).join(''); return; }
  if (!state.projects.length) {
    $('#inbox-list').innerHTML = emptyState('مخزن کو؟', 'پروژه را اضافه کن یا ثبت خودکار را فعال کن. این گربه خودش کد نمی‌زنه.', '<button class="button primary" data-view="projects">افزودن پروژه +</button>');
  } else if (search || $('#inbox-project').value) {
    $('#inbox-list').innerHTML = emptyState('گربه هم چیزی پیدا نکرد.', 'جست‌وجو یا فیلتر پروژه را تغییر بده.');
  } else {
    $('#inbox-list').innerHTML = emptyState('پرونده بسته شد. فعلاً.', 'همهٔ کامیت‌ها توضیح دارند. برو گزارش رو ببین و وانمود کن همیشه همین‌قدر منظمی.', '<button class="button secondary" data-view="reports">دیدن گزارش‌ها</button>');
  }
}

function isoDate(date) { return date.toISOString().slice(0,10); }

function reportQuery() {
  const query = new URLSearchParams({status:'done'});
  if ($('#report-project').value) query.set('project', $('#report-project').value);
  const today = new Date(`${state.today}T12:00:00Z`);
  if (period === 'today') {
    query.set('from', state.today); query.set('to', state.today);
  } else if (period === 'yesterday') {
    const yesterday = new Date(today);
    yesterday.setUTCDate(yesterday.getUTCDate() - 1);
    query.set('from', isoDate(yesterday)); query.set('to', isoDate(yesterday));
  } else if (period === 'week') {
    const start = new Date(today);
    start.setUTCDate(start.getUTCDate() - (start.getUTCDay() + 1) % 7);
    query.set('from', isoDate(start)); query.set('to', state.today);
  } else if (period === 'month') {
    const parts = new Intl.DateTimeFormat('en-US-u-ca-persian', {day:'numeric', timeZone:'UTC'}).formatToParts(today);
    const persianDay = Number(parts.find(part => part.type === 'day').value);
    const start = new Date(today);
    start.setUTCDate(start.getUTCDate() - persianDay + 1);
    query.set('from', isoDate(start)); query.set('to', state.today);
  } else if (period === 'custom') {
    if (!$('#date-from').value || !$('#date-to').value) throw new Error('شروع و پایان بازه را انتخاب کن.');
    if ($('#date-from').value > $('#date-to').value) throw new Error('شروع بازه باید قبل از پایان باشد.');
    query.set('from', $('#date-from').value); query.set('to', $('#date-to').value);
  }
  return query;
}

async function loadReport(resetPage = false) {
  const query = reportQuery();
  report = await api(`commits?${query}`);
  if (resetPage) reportPage = 0;
  renderReport();
}

function renderReport() {
  const query = reportQuery();
  const groups = new Map();
  for (const c of report) {
    if (!groups.has(c.projectId)) groups.set(c.projectId, []);
    groups.get(c.projectId).push(c);
  }
  let range = 'تمام بازه‌ها';
  if (query.get('from')) range = `${day(`${query.get('from')}T12:00:00Z`)} تا ${day(`${query.get('to')}T12:00:00Z`)}`;
  $('#report-summary').textContent = `${fa(report.length)} کار ثبت‌شده در ${fa(groups.size)} پروژه · ${range}`;
  $('#report-list').classList.toggle('report-board', reportLayout === 'kanban' && report.length > 0);
  $('#report-pages').hidden = reportLayout !== 'table' || report.length <= 10;
  if (!report.length) {
    $('#report-list').innerHTML = emptyState('گزارش: فعلاً «یه چیزایی».', 'برای کامیت‌ها توضیح بنویس یا بازهٔ گزارش را تغییر بده.', '<button class="button secondary" data-view="inbox">رفتن به کامیت‌های تازه</button>');
    return;
  }
  if (reportLayout === 'notes') {
    $('#report-list').innerHTML = `<div class="report-notes">${report.map(c => `<article class="report-note"><span class="project-tag" dir="auto">${escapeHTML(c.project)}</span><p dir="auto">${escapeHTML(c.note)}</p></article>`).join('')}</div>`;
    return;
  }
  if (reportLayout === 'table') {
    const pages = Math.ceil(report.length / 10);
    reportPage = Math.max(0, Math.min(reportPage, pages - 1));
    const start = reportPage * 10;
    const rows = report.slice(start, start + 10).map((c, index) => `<tr><td class="row-number">${fa(start + index + 1)}</td><td><span class="table-text" title="${escapeHTML(c.project)}">${escapeHTML(c.project)}</span></td><td><time datetime="${escapeHTML(c.date)}">${day(c.date)}</time></td><td><span class="table-text" dir="auto" title="${escapeHTML(c.subject)}">${escapeHTML(c.subject)}</span></td><td><span class="table-text" dir="auto" title="${escapeHTML(c.note)}">${escapeHTML(c.note)}</span></td><td><button class="button secondary" data-note="${c.id}" aria-label="مشاهده و ویرایش توضیح ${escapeHTML(c.subject)}">ویرایش</button></td></tr>`).join('');
    $('#report-list').innerHTML = `<div class="report-table-wrap" tabindex="0" role="region" aria-label="جدول گزارش؛ برای متن کامل، ویرایش را بزن"><table class="report-table"><caption>گزارش کارها؛ هر صفحه تا ۱۰ کار. متن کامل با دکمهٔ ویرایش نمایش داده می‌شود.</caption><colgroup><col class="table-number-col"><col class="table-project-col"><col class="table-date-col"><col class="table-subject-col"><col><col class="table-action-col"></colgroup><thead><tr><th scope="col">#</th><th scope="col">پروژه</th><th scope="col">تاریخ</th><th scope="col">کامیت</th><th scope="col">توضیح کار</th><th scope="col">جزئیات</th></tr></thead><tbody>${rows}</tbody></table></div>`;
    $('#report-page-label').textContent = `${fa(start + 1)} تا ${fa(Math.min(start + 10, report.length))} از ${fa(report.length)} کار · صفحهٔ ${fa(reportPage + 1)} از ${fa(pages)}`;
    $('#report-prev').disabled = reportPage === 0;
    $('#report-next').disabled = reportPage === pages - 1;
    return;
  }
  $('#report-list').innerHTML = [...groups.values()].map(items => `<section class="report-group"><h3 class="report-group-heading">▱ ${escapeHTML(items[0].project)} <small>${fa(items.length)} کار</small></h3>${items.map(c => commitCard(c, true)).join('')}</section>`).join('');
}

function renderProjects() {
  $('#project-list').innerHTML = state.projects.length ? state.projects.map(p => `<article class="project-row"><span aria-hidden="true">▱</span><div><h3>${escapeHTML(p.name)}</h3><p>${escapeHTML(p.path)}</p></div><small dir="ltr">${escapeHTML(p.email)}</small></article>`).join('') : emptyState('هنوز صحنهٔ جرمی نداریم.', 'از فرم بالا یک پروژه اضافه کن یا پوشهٔ پروژه‌ها را جست‌وجو کن.');
}

async function switchView(next) {
  if (!['inbox','reports','projects'].includes(next)) return;
  view = next;
  document.body.classList.toggle('reports-focused', view === 'reports');
  for (const name of ['inbox','reports','projects']) $(`#${name}-view`).hidden = name !== view;
  document.querySelectorAll('.nav-item').forEach(button => button.classList.toggle('active', button.dataset.view === view));
  $('#breadcrumb').textContent = {inbox:'منتظر توضیح', reports:'گزارش کارها', projects:'پروژه‌های من'}[view];
  if (state.today) { if (view === 'inbox') await loadInbox(); if (view === 'reports') await loadReport(); }
}

function editNote(id) {
  selectedCommit = [...inbox, ...report].find(c => c.id === Number(id));
  if (!selectedCommit) return;
  $('#note-title').textContent = selectedCommit.subject;
  $('#note-title').dir = 'auto';
  $('#note-meta').textContent = `${selectedCommit.project} · ${day(selectedCommit.date)} · ${selectedCommit.hash.slice(0,8)}`;
  $('#note-text').value = selectedCommit.note;
  $('#commit-details').open = false;
  $('#commit-message').textContent = selectedCommit.subject + (selectedCommit.body ? '\n\n' + selectedCommit.body : '');
  $('#note-dialog').showModal();
  $('#note-text').focus();
}

function closeNote() {
  if ($('#note-text').value !== selectedCommit?.note && !confirm('توضیح ذخیره نشده است. بدون ذخیره ببندم؟')) return;
  $('#note-dialog').close();
}

document.addEventListener('click', async event => {
  const target = event.target.closest('button');
  if (!target) return;
  try {
    if (target.dataset.view) await switchView(target.dataset.view);
    if (target.dataset.note) editNote(target.dataset.note);
    if (target.dataset.period) {
      period = target.dataset.period;
      document.querySelectorAll('[data-period]').forEach(button => button.classList.toggle('selected',button === target));
      $('#date-range').hidden = period !== 'custom';
      if (period === 'custom') {
        $('#date-from').value ||= state.today;
        $('#date-to').value ||= state.today;
      }
      await loadReport(true);
    }
    if (target.dataset.reportLayout) {
      reportLayout = target.dataset.reportLayout;
      document.querySelectorAll('[data-report-layout]').forEach(button => {
        const selected = button === target;
        button.classList.toggle('selected', selected);
        button.setAttribute('aria-pressed', String(selected));
      });
      if (state.today) renderReport();
    }
    if (target.dataset.reportPage) {
      reportPage += target.dataset.reportPage === 'next' ? 1 : -1;
      renderReport();
    }
  } catch (error) { toast(error.message, true); }
});

$('#inbox-search').addEventListener('input', renderInbox);
$('#inbox-project').addEventListener('change', () => loadInbox().catch(error => toast(error.message, true)));
$('#report-project').addEventListener('change', () => loadReport(true).catch(error => toast(error.message, true)));
$('#refresh').addEventListener('click', event => busy(event.currentTarget, refresh));
$('#toggle-gifs').addEventListener('click', () => {
  const paused = document.body.classList.toggle('gifs-paused');
  const button = $('#toggle-gifs');
  button.textContent = paused ? '▶' : '⏸';
  button.setAttribute('aria-pressed', String(paused));
  button.title = paused ? 'پخش GIFها' : 'توقف GIFها';
  button.setAttribute('aria-label', button.title);
});
if (matchMedia('(prefers-reduced-motion: reduce)').matches) {
  $('#toggle-gifs').disabled = true;
  $('#toggle-gifs').title = 'انیمیشن طبق تنظیمات سیستم متوقف است';
  $('#toggle-gifs').setAttribute('aria-label', $('#toggle-gifs').title);
}
$('#apply-range').addEventListener('click', event => busy(event.currentTarget, () => loadReport(true)));
$('#close-dialog').addEventListener('click', closeNote);
$('#cancel-note').addEventListener('click', closeNote);
$('#note-dialog').addEventListener('cancel', event => { event.preventDefault(); closeNote(); });

$('#note-form').addEventListener('submit', event => {
  event.preventDefault();
  busy(event.submitter, async () => {
    await api('notes', {id:selectedCommit.id, note:$('#note-text').value});
    $('#note-dialog').close();
    toast('ثبت شد. این یکی دیگه «یه چیزایی» نیست.');
    await refresh();
  });
});

$('#project-form').addEventListener('submit', event => {
  event.preventDefault();
  busy(event.submitter, async () => {
    await api('projects', {path:$('#project-path').value.trim(), import:$('#import-history').checked});
    $('#project-path').value = '';
    toast('یک جبههٔ جدید باز شد. پروژه اضافه شد.');
    await refresh();
  });
});

$('#discover-form').addEventListener('submit', event => {
  event.preventDefault();
  busy(event.submitter, async () => {
    const result = await api('discover', {root:$('#discover-root').value.trim()});
    showWarnings(result.warnings);
    toast(`${fa(result.count)} پروژه پیدا شد؛ کامیت‌های خودت وارد شدند.`);
    await refresh();
  });
});

for (const selector of ['#enable-hooks','#toggle-hooks']) {
  $(selector).addEventListener('click', event => busy(event.currentTarget, async () => {
    const enabled = selector === '#enable-hooks' || !state.hooksEnabled;
    const result = await api('hooks', {enabled});
    showWarnings(result.warnings);
    toast(enabled ? 'گربه گوش‌به‌زنگ شد! کامیت‌های تازه خودکار ثبت می‌شوند.' : 'ثبت خودکار غیرفعال شد؛ یادداشت‌هایت محفوظ‌اند.');
    await refresh();
  }));
}

$('#export-report').addEventListener('click', () => {
  try { window.location.href = `/api/export?${reportQuery()}`; }
  catch (error) { toast(error.message,true); }
});

refresh().catch(error => toast(error.message, true));
setInterval(() => { if (!document.hidden && !$('#note-dialog').open) refresh().catch(error => toast(error.message, true)); }, 15000);
