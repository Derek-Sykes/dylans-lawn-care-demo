'use strict';

// Exercise the actual public controller with a small DOM/API fixture.
// No packages, browser, credentials, network requests, or saved data are used.
// Native form validation and visual layout belong to the browser checks.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const controllerPath = path.join(__dirname, '..', 'dist', 'booking.js');
const source = fs.readFileSync(controllerPath, 'utf8');
const html = fs.readFileSync(path.join(__dirname, '..', 'dist', 'book.html'), 'utf8');
const testSource = source.replace(/\r?\n  loadConfig\(\);\r?\n\}\)\(\);\s*$/, '\n  globalThis.publicTest = { state, loadConfig };\n})();');
assert.notEqual(testSource, source, 'Controller bootstrap marker changed; update the fixture intentionally.');
const copy = value => JSON.parse(JSON.stringify(value));
const start = '2026-09-14T13:00:00Z';
const endAfter = minutes => new Date(Date.parse(start) + minutes * 60000).toISOString();
const receipt = (kind = 'service', overrides = {}) => ({ id: 'fixture-request', kind, status: 'needs_followup', calendarStatus: 'pending', start, end: endAfter(kind === 'estimate' ? 15 : 60), ...overrides });

function fixture(kind = 'service', configOverrides = {}) {
  const elements = new Map();
  const calls = [];
  const replies = [];
  const timers = new Map();
  let timerID = 0;
  let keyID = 0;
  const contact = { name: ' Sample Customer ', email: ' customer@example.test ', phone: ' 410-555-0100 ', address: ' Example property ', notes: '' };
  const config = { services: [{ id: 'lawn-care', name: 'Lawn care' }], bookingEnabled: true, timeZone: 'America/New_York', horizonDays: 30, minNoticeHours: 0, slotMinutes: 60, estimateMinutes: 15, ...configOverrides };
  const document = { hidden: false, activeElement: null, title: '' };
  class Element {
    constructor(tag = 'div') { this.tagName = tag; this.children = []; this.value = ''; this.hidden = false; this.disabled = false; this.textContent = ''; this.listeners = {}; this.attributes = {}; this.dataset = {}; this.focusCount = 0; this.valid = true; }
    append(...children) { children.forEach(child => this.children.push(...(child?.tagName === 'fragment' ? child.children : [child]))); }
    replaceChildren(...children) { this.children = []; this.append(...children); if (this.tagName === 'select') this.value = this.children[0]?.value || ''; }
    add(option) { this.append(option); }
    setAttribute(name, value) { this.attributes[name] = value; }
    removeAttribute(name) { delete this.attributes[name]; }
    addEventListener(name, handler) { (this.listeners[name] ||= []).push(handler); }
    querySelector(selector) { assert.equal(this, elements.get('#submit-booking')); assert.equal(selector, '.button-label'); return elements.get('#submit-booking .button-label'); }
    focus() { document.activeElement = this; this.focusCount++; }
    checkValidity() { return this.valid; }
    reportValidity() { return this.valid; }
    async emit(name) { for (const handler of this.listeners[name] || []) await handler({ preventDefault() {} }); }
  }
  for (const match of html.matchAll(/<([a-z][\w-]*)\b([^>]*\bid="([^"]+)"[^>]*)>/gi)) {
    const element = new Element(match[1]);
    element.hidden = /\bhidden(?:\s|=|$)/.test(match[2]);
    elements.set(`#${match[3]}`, element);
  }
  elements.set('#submit-booking .button-label', new Element('span'));
  elements.set('meta[name="description"]', new Element('meta'));
  const modeLinks = ['service', 'estimate'].map(mode => Object.assign(new Element('a'), { dataset: { bookingKind: mode } }));
  const $ = selector => { assert.ok(elements.has(selector), `Fixture selector must exist in book.html: ${selector}`); return elements.get(selector); };
  document.querySelector = $;
  document.querySelectorAll = selector => { assert.equal(selector, '[data-booking-kind]'); return modeLinks; };
  document.createElement = tag => new Element(tag);
  document.createDocumentFragment = () => new Element('fragment');
  const window = new Element('window');
  window.location = { search: kind === 'estimate' ? '?type=estimate' : '' };
  const response = (data, status = 200) => ({ ok: status >= 200 && status < 300, status, json: async () => copy(data) });
  const context = vm.createContext({
    document, window, URLSearchParams, Intl, Date, AbortController,
    crypto: { randomUUID: () => `fixture-idempotency-key-${++keyID}` },
    Option: function Option(text, value) { return Object.assign(new Element('option'), { textContent: text, value }); },
    FormData: function FormData(form) { assert.equal(form, $('#booking-form')); return Object.entries(contact); },
    setTimeout(handler, delay) { const id = ++timerID; timers.set(id, { handler, delay }); return id; },
    clearTimeout(id) { timers.delete(id); },
    fetch: async (url, options) => {
      calls.push({ url, method: options.method, serializedBody: options.body, body: options.body ? JSON.parse(options.body) : undefined });
      assert.equal(options.credentials, 'same-origin');
      assert.equal(options.cache, 'no-store');
      if (url === '/api/public/config') return response(config);
      if (url.startsWith('/api/public/slots?')) {
        const query = new URL(url, 'https://example.test').searchParams;
        return response({ date: query.get('date'), timeZone: config.timeZone, slots: [{ start, end: endAfter(query.get('kind') === 'estimate' ? config.estimateMinutes : config.slotMinutes) }] });
      }
      assert.equal(url, '/api/public/bookings');
      assert.equal(options.method, 'POST');
      assert.ok(replies.length, 'Unexpected booking request; add an explicit fixture response.');
      const reply = replies.shift();
      if (reply instanceof Error) throw reply;
      if (typeof reply === 'function') return reply();
      return response(reply.data, reply.status ?? 202);
    }
  });
  vm.runInContext(testSource, context, { filename: controllerPath });
  return {
    $, calls, contact, config, replies, timers, document, window, modeLinks,
    state: context.publicTest.state,
    load: context.publicTest.loadConfig,
    posts: () => calls.filter(call => call.method === 'POST'),
    async choose() {
      $('#service').value = 'lawn-care';
      await $('#service').emit('change');
      const radio = $('#time-slots').children[0]?.children[0];
      assert.equal(radio?.type, 'radio', 'Available times must render an actual radio choice.');
      await radio.emit('change');
    },
    submit: () => $('#booking-form').emit('submit'),
    respond: (data, status) => replies.push({ data, status }),
    async runTimer(delay) {
      const matching = [...timers.entries()].filter(([, timer]) => timer.delay === delay);
      assert.equal(matching.length, 1, `Expected one ${delay}ms timer.`);
      const [id, timer] = matching[0]; timers.delete(id); await timer.handler();
    }
  };
}

const tests = [];
const test = (name, run) => tests.push({ name, run });
for (const kind of ['service', 'estimate']) {
  test(`${kind}: explicit kind, configured duration, trimmed contact/address and optional notes`, async () => {
    const f = fixture(kind, { slotMinutes: 90, estimateMinutes: 30 });
    await f.load(); await f.choose();
    assert.equal(f.$('#booking-form').hidden, false);
    assert.match(f.$('#appointment-description').textContent, new RegExp(`^${kind === 'estimate' ? 30 : 90}-minute`));
    assert.equal(new URL(f.calls.find(call => call.url.startsWith('/api/public/slots?')).url, 'https://example.test').searchParams.get('kind'), kind);
    assert.equal(f.modeLinks.find(link => link.dataset.bookingKind === kind).attributes['aria-current'], 'page');
    f.respond(receipt(kind, { calendarStatus: 'synced', end: endAfter(75) }));
    await f.submit();
    assert.deepEqual(f.posts()[0].body, { kind, serviceId: 'lawn-care', start, name: 'Sample Customer', email: 'customer@example.test', phone: '410-555-0100', address: 'Example property', notes: '', idempotencyKey: 'fixture-idempotency-key-1' });
    assert.equal(f.$('#booking-success').hidden, false);
    assert.match(f.$('#success-timezone').textContent, /^75 minutes/);
    assert.doesNotMatch(f.$('#success-description').textContent, /has confirmed/);
    assert.equal(f.$('#success-sync').textContent, 'Added to Google Calendar');
    if (kind === 'estimate') assert.match(f.$('#success-sync-note').textContent, /does not reserve a service visit/);
    assert.equal(f.timers.size, 0, 'A synced receipt must not poll.');
  });
}

test('notes remain optional; populated notes are trimmed without becoming HTML', async () => {
  const notesTag = html.match(/<textarea\b[^>]*\bname="notes"[^>]*>/i)?.[0];
  assert.ok(notesTag); assert.doesNotMatch(notesTag, /\brequired\b/);
  const f = fixture(); await f.load(); await f.choose();
  f.contact.notes = '  <b>Side gate</b>  ';
  f.respond(receipt('service', { calendarStatus: 'synced' })); await f.submit();
  assert.equal(f.posts()[0].body.notes, '<b>Side gate</b>');
});

test('missing estimate duration fails closed only for the estimate path', async () => {
  const estimate = fixture('estimate', { estimateMinutes: undefined }); await estimate.load();
  assert.equal(estimate.$('#booking-form').hidden, true);
  assert.equal(estimate.$('#booking-unavailable').hidden, false);
  assert.equal(estimate.calls.length, 1, 'Missing duration must not request slots or guess a duration.');
  const service = fixture('service', { estimateMinutes: undefined }); await service.load();
  assert.equal(service.$('#booking-form').hidden, false);
  assert.match(service.$('#appointment-description').textContent, /^60-minute/);
});

test('required whitespace-only contact is rejected before any booking request', async () => {
  const f = fixture(); await f.load(); await f.choose(); f.contact.address = '   ';
  await f.submit(); assert.equal(f.posts().length, 0); assert.equal(f.state.attempt, null);
  assert.match(f.$('#submit-message').textContent, /property address/);
});

for (const [name, invalid] of [
  ['wrong kind', { kind: 'estimate' }], ['missing kind', { kind: undefined }],
  ['missing end', { end: undefined }], ['invalid end', { end: 'not-a-date' }],
  ['zero duration', { end: start }], ['negative duration', { end: endAfter(-15) }],
  ['unknown follow-up status', { status: 'approved' }], ['unknown calendar status', { calendarStatus: 'done' }]
]) {
  test(`malformed receipt (${name}) preserves the exact request and retry key`, async () => {
    const f = fixture(); await f.load(); await f.choose();
    f.respond(receipt('service', invalid)); await f.submit();
    assert.equal(f.$('#booking-success').hidden, true);
    assert.ok(f.state.attempt);
    assert.equal(f.$('#customer-fields').disabled, true);
    assert.match(f.$('#submit-booking .button-label').textContent, /Check & retry/);
    f.contact.name = 'Changed after uncertain response';
    f.respond(receipt('service', { calendarStatus: 'synced' })); await f.submit();
    assert.equal(f.posts().length, 2);
    assert.equal(f.posts()[1].serializedBody, f.posts()[0].serializedBody);
    assert.equal(f.$('#booking-success').hidden, false);
    assert.equal(f.state.attempt, null);
  });
}

test('an ambiguous network failure followed by rate limiting retains one request', async () => {
  const f = fixture(); await f.load(); await f.choose();
  f.replies.push(new Error('fixture connection lost')); await f.submit();
  f.respond({ error: 'Please wait', code: 'rate_limited' }, 429); await f.submit();
  assert.ok(f.state.attempt); assert.match(f.$('#submit-message').textContent, /wait a minute/);
  f.respond(receipt('service', { calendarStatus: 'synced' })); await f.submit();
  assert.equal(new Set(f.posts().map(call => call.serializedBody)).size, 1);
});

test('two rapid submissions send only one request while the first is in flight', async () => {
  const f = fixture('estimate'); await f.load(); await f.choose();
  let release;
  f.replies.push(() => new Promise(resolve => { release = resolve; }));
  const first = f.submit(); await f.submit();
  assert.equal(f.posts().length, 1); assert.equal(f.$('#submit-booking').disabled, true);
  release({ ok: true, status: 202, json: async () => receipt('estimate') }); await first;
  assert.equal(f.$('#booking-success').hidden, false);
  assert.equal(f.$('#success-sync').textContent, 'Calendar update pending');
  assert.doesNotMatch(f.$('#success-description').textContent, /has confirmed/);
});

test('bounded polling reuses the exact payload, ignores invalid results and preserves focus', async () => {
  const f = fixture('estimate'); await f.load(); await f.choose();
  f.respond(receipt('estimate')); await f.submit();
  assert.equal(f.$('#success-title').focusCount, 1);
  f.$('#success-reference').focus();
  f.respond(receipt('service', { calendarStatus: 'synced', end: endAfter(90) })); await f.runTimer(2000);
  assert.equal(f.$('#success-sync').textContent, 'Calendar update pending');
  assert.match(f.$('#success-timezone').textContent, /^15 minutes/);
  f.respond(receipt('estimate', { status: 'confirmed', calendarStatus: 'synced', end: endAfter(45) })); await f.runTimer(5000);
  assert.equal(f.$('#success-sync').textContent, 'Added to Google Calendar');
  assert.match(f.$('#success-description').textContent, /confirmed your estimate or callback/);
  assert.match(f.$('#success-timezone').textContent, /^45 minutes/);
  assert.equal(f.document.activeElement, f.$('#success-reference'));
  assert.equal(f.$('#success-title').focusCount, 1);
  assert.equal(new Set(f.posts().map(call => call.serializedBody)).size, 1);
  assert.equal(f.state.receipt.checks, 2); assert.equal(f.timers.size, 0);
});

test('pending polls stop after two checks and cannot accept a different saved request', async () => {
  const f = fixture(); await f.load(); await f.choose();
  f.respond(receipt()); await f.submit();
  f.respond(receipt('service', { id: 'different-request', calendarStatus: 'synced' })); await f.runTimer(2000);
  assert.equal(f.$('#success-reference').textContent, 'fixture-request');
  f.respond(receipt()); await f.runTimer(5000);
  assert.equal(f.posts().length, 3); assert.equal(f.timers.size, 0);
  assert.equal(f.$('#success-sync').textContent, 'Calendar update pending');
});

test('calendar failure is visible and does not imply a confirmed appointment', async () => {
  const f = fixture(); await f.load(); await f.choose();
  f.respond(receipt('service', { calendarStatus: 'failed' })); await f.submit();
  assert.equal(f.$('#success-sync').textContent, 'Calendar update needs attention');
  assert.doesNotMatch(f.$('#success-description').textContent, /has confirmed/);
  assert.equal(f.timers.size, 0);
});

(async () => {
  for (const { name, run } of tests) {
    try { await run(); } catch (error) { error.message = `${name}: ${error.message}`; throw error; }
  }
  console.log(`Public booking checks passed (${tests.length}): service/estimate payloads and durations, optional notes, missing configuration, exact idempotent retries, receipt validation, double submission, truthful bounded status polling and focus.`);
})().catch(error => { console.error(error); process.exitCode = 1; });
