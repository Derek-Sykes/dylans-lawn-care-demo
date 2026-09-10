'use strict';

// Run the actual owner controller with an in-memory DOM and scheduling API.
// No browser dependencies, credentials, live requests, or saved data are used.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

class Element {
  constructor(tag = 'div') { this.tagName = tag; this.children = []; this.value = ''; this.hidden = false; this.disabled = false; this.open = false; this.textContent = ''; this.listeners = {}; this.attributes = {}; this.classList = { toggle() {} }; }
  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this.children = children; }
  setAttribute(name, value) { this.attributes[name] = value; }
  removeAttribute(name) { delete this.attributes[name]; }
  addEventListener(name, handler) { (this.listeners[name] ||= []).push(handler); }
  querySelector(selector) { return this.selected ||= new Element(selector); }
  querySelectorAll() { return []; }
  contains(element) { return this === element || this.children.some(child => child instanceof Element && child.contains(element)); }
  focus() { document.activeElement = this; }
  showModal() { this.open = true; }
  close() { this.open = false; }
}

const elements = new Map();
const document = new Element('document');
document.hidden = false;
document.querySelector = selector => { if (!elements.has(selector)) elements.set(selector, new Element(selector)); return elements.get(selector); };
document.createElement = tag => new Element(tag);
document.createDocumentFragment = () => new Element('fragment');
document.createTextNode = text => Object.assign(new Element('text'), { textContent: text });
const window = new Element('window');
window.location = { hash: '', search: '', pathname: '/admin/', href: 'https://example.test/admin/' };
const timers = new Map();
let timerID = 0;
let apiCalls = 0;
let reply = { bookings: [] };
let heldReply = null;
const session = { authenticated: true, csrfToken: 'fixture', google: { connected: true, configured: true, mode: 'web' } };
const context = vm.createContext({
  document, window, history: { replaceState() {} }, URL, URLSearchParams, Intl, Date, TextEncoder, AbortController,
  setTimeout(handler, delay) { const id = ++timerID; timers.set(id, { handler, delay }); return id; },
  clearTimeout(id) { timers.delete(id); },
  fetch: async url => {
    if (url === '/api/admin/session') return { ok: true, status: 200, json: async () => session };
    assert.equal(url, '/api/admin/bookings'); apiCalls++;
    if (heldReply) return heldReply;
    return { ok: true, status: 200, json: async () => reply };
  }
});
const controllerPath = path.join(__dirname, '..', 'booking', 'web', 'admin.js');
const source = fs.readFileSync(controllerPath, 'utf8');
const testSource = source.replace(/\r?\n  start\(\);\r?\n\}\)\(\);\s*$/, '\n  globalThis.ownerTest = { state, loadBookings, renderBookings, renderDetail, schedulePoll, refreshVisibleBookings, appointmentDuration, switchPanel };\n})();');
assert.notEqual(testSource, source, 'Controller bootstrap marker changed; update the fixture intentionally.');
vm.runInContext(testSource, context, { filename: controllerPath });
const { state, loadBookings, renderBookings, renderDetail, schedulePoll, refreshVisibleBookings, appointmentDuration, switchPanel } = context.ownerTest;
const $ = document.querySelector;
const text = element => [element.textContent, ...element.children.map(text)].join(' ');
const booking = { id: 'fixture-job', name: 'Sample Customer', serviceId: 'lawn-care', start: '2026-09-14T13:00:00Z', end: '2026-09-14T14:00:00Z', phone: '410-555-0100', email: 'customer@example.test', address: 'Example property', notes: 'Front lawn', adminNotes: 'Saved note', status: 'needs_followup', calendarStatus: 'synced', createdAt: '2026-09-10T12:00:00Z' };

(async () => {
  state.session = session; state.settings = { timeZone: 'America/New_York' }; state.bookings = [booking]; state.selectedId = booking.id;
  $('#booking-filter').value = 'all'; $('#booking-dialog').open = true;
  renderDetail(booking);
  $('#detail-notes').value = 'Saved note plus an unsaved edit';
  $('#detail-status').value = 'confirmed';
  $('#detail-notes').focus();
  reply = { bookings: [{ ...booking, start: '2026-09-14T15:00:00Z', end: '2026-09-14T16:30:00Z' }] };
  assert.equal(await loadBookings(true), true);
  assert.match(text($('#booking-detail')), /1 hour 30 minutes/);
  assert.match(text($('#booking-detail')), /11:00 AM/);
  assert.equal($('#detail-notes').value, 'Saved note plus an unsaved edit');
  assert.equal($('#detail-status').value, 'confirmed');
  assert.equal(document.activeElement, $('#detail-notes'));

  reply = { bookings: state.bookings, calendarSyncError: 'fixture provider unavailable' };
  await loadBookings(true);
  assert.equal($('#calendar-refresh-message').hidden, false);
  assert.match($('#calendar-refresh-message').textContent, /last saved times/);
  assert.equal($('#detail-notes').value, 'Saved note plus an unsaved edit');
  assert.equal($('#detail-status').value, 'confirmed');

  let release;
  heldReply = new Promise(resolve => { release = resolve; });
  const beforeHeld = apiCalls;
  const pending = loadBookings(true);
  assert.equal(await loadBookings(true), false, 'Refresh requests must not overlap.');
  assert.equal(apiCalls, beforeHeld + 1);
  state.bookingRevision++;
  release({ ok: true, status: 200, json: async () => ({ bookings: [booking] }) });
  assert.equal(await pending, false, 'A response started before a mutation must not replace newer appointment state.');
  assert.equal(state.bookings[0].end, '2026-09-14T16:30:00Z');
  heldReply = null;

  schedulePoll();
  assert.equal([...timers.values()].filter(timer => timer.delay === 60000).length, 1);
  document.hidden = true;
  const beforeHidden = apiCalls;
  refreshVisibleBookings();
  assert.equal(apiCalls, beforeHidden, 'Hidden pages must not fetch appointment updates.');
  document.listeners.visibilitychange[0]();
  assert.equal([...timers.values()].filter(timer => timer.delay === 60000).length, 0);
  document.hidden = false;
  reply = { bookings: state.bookings };
  document.listeners.visibilitychange[0]();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(apiCalls, beforeHidden + 1, 'Returning to the portal must refresh calendar times.');
  assert.equal($('#calendar-refresh-message').hidden, true);
  assert.equal($('#detail-notes').value, 'Saved note plus an unsaved edit');
  assert.equal(appointmentDuration({ start: booking.start, end: booking.start }), 'Duration unavailable');
  state.bookings[0].status = 'contacted';
  state.bookings.push({ ...booking, id: 'estimate-fixture', kind: 'estimate', status: 'confirmed', name: 'Estimate Customer', end: '2026-09-14T13:15:00Z' });
  state.bookings.push({ ...booking, id: 'cancelled-service', status: 'cancelled', name: 'Cancelled Service' }, { ...booking, id: 'cancelled-estimate', kind: 'estimate', status: 'cancelled', name: 'Cancelled Estimate' });
  state.activeKind = 'service'; renderBookings();
  assert.match(text($('#booking-list')), /Sample Customer/);
  assert.doesNotMatch(text($('#booking-list')), /Estimate Customer/);
  state.activeKind = 'estimate'; renderBookings();
  assert.match(text($('#booking-list')), /Estimate Customer/);
  assert.doesNotMatch(text($('#booking-list')), /Sample Customer/);
  assert.match(text($('#bookings-heading')), /Estimates & callbacks/);
  assert.equal($('#request-count').textContent, 1);
  assert.equal($('#estimate-count').textContent, 1);
  assert.equal($('#stat-followup').textContent, 0, 'Needs-contact totals remain separate from active appointment totals.');
  assert.equal($('#stat-confirmed').textContent, 1);
  state.loadingBookings = true;
  switchPanel('bookings', true, 'service');
  assert.equal(document.activeElement, $('#bookings-heading'));
  assert.match(text($('#bookings-heading')), /Appointments/);
  switchPanel('bookings', true, 'estimate');
  assert.equal(document.activeElement, $('#bookings-heading'));
  assert.match(text($('#bookings-heading')), /Estimates & callbacks/);
  state.loadingBookings = false;
  console.log('Owner refresh checks passed: changed times/duration, notes/status drafts, focus, stale responses, one request at a time, provider warning, visible-only polling, separate service/estimate views.');
})().catch(error => { console.error(error); process.exitCode = 1; });
