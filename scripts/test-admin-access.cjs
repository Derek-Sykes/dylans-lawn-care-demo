'use strict';

// Actual portal controller; in-memory DOM/API only. No credentials, network or browser storage.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const file = path.join(__dirname, '..', 'booking', 'web', 'admin.js');
const source = fs.readFileSync(file, 'utf8');
const html = fs.readFileSync(path.join(__dirname, '..', 'booking', 'web', 'index.html'), 'utf8');
const testSource = source.replace(/\r?\n  start\(\);\r?\n\}\)\(\);\s*$/, '\n  globalThis.accessTest = { state, start, signInGoogle, connectGoogle, renderConnection, showSignedOut, switchPanel, createInvitation, revokeInvitation, loadInvitations, renderSettings, readSettings };\n})();');
assert.notEqual(source, testSource, 'Update the fixture intentionally if the controller bootstrap changes.');
const copy = value => JSON.parse(JSON.stringify(value));
const session = { authenticated: true, role: 'operator', actorEmail: 'operator@example.test', canManageAccess: true, canConnectCalendar: false, csrfToken: 'fixture-csrf', publicOrigin: 'https://example.test', google: { configured: true, connected: true, email: 'owner@example.test', mode: 'web' } };
const settings = { businessName: 'Example business', timeZone: 'America/New_York', slotMinutes: 60, estimateMinutes: 15, bufferMinutes: 15, minNoticeHours: 24, horizonDays: 30, weekly: [{ weekday: 1, start: '09:00', end: '17:00' }], exceptions: [], blockedWeekly: [], blockedDates: [] };
const invitation = { id: 'invitation-1', email: 'owner@example.test', expiresAt: '2099-09-14T13:00:00Z', status: 'pending' };
const privateURL = 'https://example.test/admin/#invite=fixture-private-token';

function fixture(hash = '', search = '') {
  const elements = new Map(); const fields = new Map(); const calls = []; const replies = [];
  const document = { activeElement: null, hidden: false, listeners: {} };
  class Element {
    constructor(tag = 'div') { this.tagName = tag; this.children = []; this.className = ''; this.dataset = {}; this.attributes = {}; this.listeners = {}; this.hidden = false; this.disabled = false; this.checked = false; this.open = false; this.files = []; this.textContent = ''; this.value = ''; this.classList = { toggle() {} }; }
    set value(value) { this._value = String(value); } get value() { return this._value; }
    append(...children) { children.forEach(child => this.children.push(...(child.tagName === 'fragment' ? child.children : [child]))); }
    replaceChildren(...children) { this.children = []; this.append(...children); }
    add(option) { this.append(option); }
    setAttribute(name, value) { this.attributes[name] = value; }
    removeAttribute(name) { delete this.attributes[name]; }
    addEventListener(name, handler) { (this.listeners[name] ||= []).push(handler); }
    querySelectorAll(selector) {
      const parts = selector.split(' '); const first = parts.shift();
      const descendants = this.children.flatMap(child => [child, ...child.all()]);
      const found = descendants.filter(child => matches(child, first));
      return parts.length ? found.flatMap(child => child.querySelectorAll(parts.join(' '))) : found;
    }
    querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
    all() { return this.children.flatMap(child => [child, ...child.all()]); }
    reset() { this.value = ''; this.children.forEach(child => child.reset()); if (this.id === 'settings-form') fields.forEach(field => { field.value = ''; }); }
    reportValidity() { return true; }
    focus() { document.activeElement = this; }
    select() { this.selected = true; }
    close() { this.open = false; }
    contains(element) { return this === element || this.all().includes(element); }
    async emit(name) { for (const handler of this.listeners[name] || []) await handler({ preventDefault() {} }); }
  }
  function matches(element, selector) {
    if (selector.startsWith('.')) return element.className.split(' ').includes(selector.slice(1));
    if (selector === '[data-panel]') return Boolean(element.dataset.panel);
    return element.tagName === selector;
  }
  for (const match of html.matchAll(/<([a-z][\w-]*)\b([^>]*\bid="([^"]+)"[^>]*)>/gi)) {
    const element = new Element(match[1]); element.id = match[3];
    element.className = match[2].match(/\bclass="([^"]+)"/)?.[1] || '';
    element.hidden = /\bhidden(?:\s|=|$)/.test(match[2]); elements.set(`#${element.id}`, element);
  }
  const panels = [...html.matchAll(/<button\b([^>]*\bdata-panel="([^"]+)"[^>]*)>/g)].map(match => {
    const element = new Element('button'); element.dataset = { panel: match[2], kind: match[1].match(/data-kind="([^"]+)"/)?.[1] };
    const id = match[1].match(/id="([^"]+)"/)?.[1]; if (id) elements.set(`#${id}`, element); return element;
  });
  for (const name of ['businessName', 'timeZone', 'slotMinutes', 'estimateMinutes', 'bufferMinutes', 'externalBufferMinutes', 'minNoticeHours', 'horizonDays']) fields.set(name, new Element('input'));
  const $ = selector => {
    if (!elements.has(selector) && /^#[\w-]+ \.button-label$/.test(selector)) {
      const parent = elements.get(selector.split(' ')[0]); assert.ok(parent); const label = new Element('span'); label.className = 'button-label'; parent.append(label); elements.set(selector, label);
    }
    assert.ok(elements.has(selector), `Unknown HTML selector: ${selector}`); return elements.get(selector);
  };
  $('#settings-form').elements = { namedItem: name => fields.get(name) };
  $('#booking-filter').value = 'all';
  document.querySelector = $;
  document.querySelectorAll = selector => selector === '[data-panel]' ? panels : [...new Set([...elements.values()].flatMap(element => [element, ...element.all()]))].filter(element => matches(element, selector));
  document.createElement = tag => new Element(tag);
  document.createDocumentFragment = () => new Element('fragment');
  document.createTextNode = text => Object.assign(new Element('text'), { textContent: text });
  document.addEventListener = (name, handler) => { (document.listeners[name] ||= []).push(handler); };
  const window = new Element('window');
  let location = new URL(`https://example.test/admin/${search}${hash}`); const navigations = []; const historyCalls = [];
  window.location = { hash: location.hash, search: location.search, pathname: location.pathname, href: location.href, assign: url => navigations.push(url) };
  window.confirm = () => { throw new Error('Invitation actions must not open a native browser confirmation.'); };
  const history = { replaceState(_, __, value) { historyCalls.push(value); location = new URL(value, location); Object.assign(window.location, { hash: location.hash, search: location.search, pathname: location.pathname, href: location.href }); } };
  const timers = new Map(); let timerID = 0;
  const reply = (data, status = 200) => ({ ok: status >= 200 && status < 300, status, json: async () => copy(data) });
  const clipboard = [];
  const context = vm.createContext({
    document, window, history, URL, URLSearchParams, Intl, Date, TextEncoder, AbortController,
    navigator: { clipboard: { writeText: async text => { clipboard.push(text); } } },
    FormData: function FormData() { return new Map([...fields].map(([name, field]) => [name, field.value])); },
    Option: function Option(text, value) { return Object.assign(new Element('option'), { textContent: text, value }); },
    setTimeout(handler, delay) { const id = ++timerID; timers.set(id, { handler, delay }); return id; }, clearTimeout(id) { timers.delete(id); },
    fetch: async (url, options) => {
      assert.equal(window.location.hash, '', 'Private fragment must be cleared before the first request.');
      calls.push({ url, method: options.method, body: options.body ? JSON.parse(options.body) : undefined, headers: options.headers });
      if (replies.length && replies[0].url === url) { const result = replies.shift(); return result.handler ? result.handler() : reply(result.data, result.status); }
      if (url === '/api/admin/session') return reply(copy(session));
      if (url === '/api/admin/bookings') return reply({ bookings: [] });
      if (url === '/api/admin/settings') return reply(copy(settings));
      if (url === '/api/admin/invitations' && options.method === 'GET') return reply({ invitations: [] });
      assert.fail(`Unexpected API call: ${options.method} ${url}`);
    }
  });
  vm.runInContext(testSource, context, { filename: file });
  return { $, document, window, calls, replies, fields, navigations, historyCalls, clipboard, ...context.accessTest,
    invitationButton: text => $('#invitation-list').querySelectorAll('button').find(button => button.textContent === text),
    authenticate(overrides = {}) { this.state.session = { ...copy(session), ...overrides }; this.state.csrf = 'fixture-csrf'; },
    respond(url, data, status = 200) { replies.push({ url, data, status }); }
  };
}

const tests = [];
const test = (name, run) => tests.push({ name, run });
test('Google portal sign-in never requests a calendar connection', async () => {
  const f = fixture();
  f.respond('/api/admin/signin', { url: 'https://accounts.google.com/o/oauth2/v2/auth?scope=openid' });
  await f.signInGoogle();
  assert.equal(f.calls.length, 1); assert.equal(f.calls[0].url, '/api/admin/signin');
  assert.equal(f.navigations.length, 1);
});
test('invitation fragment is removed immediately and accepted only on a deliberate Google action', async () => {
  const f = fixture('#invite=fixture-private-token');
  assert.equal(f.window.location.hash, ''); assert.equal(f.calls.length, 0);
  assert.deepEqual(f.historyCalls, ['/admin/']);
  f.respond('/api/admin/invitations/accept', { url: 'https://accounts.google.com/o/oauth2/v2/auth?scope=openid' });
  await f.signInGoogle();
  assert.deepEqual(f.calls[0].body, { token: 'fixture-private-token' });
  assert.equal(f.calls[0].url, '/api/admin/invitations/accept');
  assert.equal(f.$('#invitation-link').value, '');
});
test('operator access does not grant permission to replace the owner calendar', async () => {
  const f = fixture(); f.authenticate(); f.renderConnection();
  assert.equal(f.$('#access-nav').hidden, false);
  assert.equal(f.$('#connect-google').hidden, true); assert.equal(f.$('#disconnect-google').hidden, true);
  assert.match(f.$('#session-identity').textContent, /operator@example.test/);
  assert.match(f.$('#calendar-email').textContent, /owner@example.test/);
  await f.connectGoogle(); assert.equal(f.calls.length, 0);
  f.authenticate({ canConnectCalendar: true });
  f.respond('/api/admin/google/connect', { url: 'https://accounts.google.com/o/oauth2/v2/auth?scope=calendar' });
  await f.connectGoogle(); assert.equal(f.calls[0].headers['X-CSRF-Token'], 'fixture-csrf');
});
test('owner role cannot create, list or view operator invitations', async () => {
  const f = fixture(); f.authenticate({ role: 'owner', canManageAccess: false, canConnectCalendar: true }); f.renderConnection();
  f.$('#invitation-email').value = 'someone@example.test';
  await f.createInvitation({ preventDefault() {} }); await f.loadInvitations(); f.switchPanel('access');
  assert.equal(f.calls.length, 0); assert.equal(f.$('#access-nav').hidden, true); assert.notEqual(f.state.activePanel, 'access');
});
test('create, copy and revoke a private link without persisting or recovering its token', async () => {
  const f = fixture(); f.authenticate(); f.state.activePanel = 'access'; f.$('#invitation-email').value = ' owner@example.test ';
  f.state.invitations = [{ ...invitation, id: 'earlier-link', email: 'previous-recipient@example.test' }];
  f.respond('/api/admin/invitations', { invitation, url: privateURL }, 201);
  await f.createInvitation({ preventDefault() {} });
  assert.deepEqual(f.calls[0].body, { email: 'owner@example.test' });
  assert.equal(f.calls[0].headers['X-CSRF-Token'], 'fixture-csrf');
  assert.equal(f.$('#invitation-result').hidden, false); assert.equal(f.$('#invitation-link').value, privateURL);
  assert.equal(f.state.invitations.find(item => item.id === 'earlier-link').status, 'revoked');
  await f.$('#copy-invitation').emit('click'); assert.deepEqual(f.clipboard, [privateURL]);
  await f.invitationButton('Revoke').emit('click');
  assert.equal(f.calls.length, 1, 'Showing inline confirmation must not revoke.');
  assert.equal(f.document.activeElement, f.invitationButton('Confirm revocation'));
  await f.invitationButton('Keep invitation').emit('click');
  assert.equal(f.state.pendingRevokeId, null); assert.equal(f.document.activeElement, f.invitationButton('Revoke'));
  await f.invitationButton('Revoke').emit('click');
  f.respond('/api/admin/invitations/invitation-1', null, 204); await f.invitationButton('Confirm revocation').emit('click');
  assert.equal(f.calls[1].method, 'DELETE'); assert.equal(f.state.invitations[0].status, 'revoked');
  assert.equal(f.state.invitationLink, null); assert.equal(f.$('#invitation-link').value, '');
  assert.equal(f.state.pendingRevokeId, null); assert.equal(f.document.activeElement.textContent, invitation.email);
});

test('leaving Access cancels a pending inline revocation and hides its controls', async () => {
  const f = fixture(); f.authenticate(); f.state.activePanel = 'access';
  f.respond('/api/admin/invitations', { invitations: [invitation] }); await f.loadInvitations();
  await f.invitationButton('Revoke').emit('click'); assert.equal(f.state.pendingRevokeId, invitation.id);
  f.switchPanel('availability');
  assert.equal(f.state.pendingRevokeId, null); assert.equal(f.invitationButton('Confirm revocation'), undefined);
  await f.revokeInvitation(invitation); assert.equal(f.calls.length, 1);
});

test('inline revoke errors allow one retry, retain focus and block double submissions', async () => {
  const f = fixture(); f.authenticate(); f.state.activePanel = 'access';
  f.respond('/api/admin/invitations', { invitations: [invitation] }); await f.loadInvitations();
  await f.invitationButton('Revoke').emit('click');
  f.respond('/api/admin/invitations/invitation-1', { error: 'Try again shortly', code: 'unavailable' }, 503);
  await f.invitationButton('Confirm revocation').emit('click');
  assert.equal(f.state.pendingRevokeId, invitation.id); assert.equal(f.state.invitations[0].status, 'pending');
  assert.equal(f.document.activeElement, f.invitationButton('Confirm revocation'));
  let release; f.replies.push({ url: '/api/admin/invitations/invitation-1', handler: () => new Promise(resolve => { release = resolve; }) });
  const confirm = f.invitationButton('Confirm revocation'); const pending = confirm.emit('click'); await confirm.emit('click');
  assert.equal(f.calls.filter(call => call.method === 'DELETE').length, 2, 'The second click cannot create another in-flight DELETE.');
  release({ ok: true, status: 204 }); await pending;
  assert.equal(f.state.invitations[0].status, 'revoked');
});

test('hiding a copied private link replaces the old copy success message', async () => {
  const f = fixture(); f.authenticate(); f.state.activePanel = 'access'; f.$('#invitation-email').value = invitation.email;
  f.respond('/api/admin/invitations', { invitation, url: privateURL }, 201); await f.createInvitation({ preventDefault() {} });
  await f.$('#copy-invitation').emit('click'); await f.$('#hide-invitation').emit('click');
  assert.equal(f.$('#invitation-message').textContent, 'Private link hidden.');
  assert.equal(f.$('#invitation-link').value, ''); assert.equal(f.state.invitationLink, null);
});
test('sign-out during revocation clears pending controls and ignores the late response', async () => {
  const f = fixture(); f.authenticate(); f.state.activePanel = 'access';
  f.respond('/api/admin/invitations', { invitations: [invitation] }); await f.loadInvitations();
  await f.invitationButton('Revoke').emit('click');
  let release; f.replies.push({ url: '/api/admin/invitations/invitation-1', handler: () => new Promise(resolve => { release = resolve; }) });
  const pending = f.invitationButton('Confirm revocation').emit('click'); f.showSignedOut();
  release({ ok: true, status: 204 }); await pending;
  assert.equal(f.state.pendingRevokeId, null); assert.equal(f.state.invitations.length, 0);
  assert.equal(f.$('#invitation-list').querySelectorAll('button').length, 0); assert.equal(f.$('#invitation-list').attributes['aria-busy'], 'false');
  assert.equal(f.$('#invitation-list').all().some(element => element.textContent.includes(invitation.email)), false);
  assert.equal(f.$('#invitation-message').textContent, '');
});
test('leaving Access clears the one-time link and preserves the email-only history', async () => {
  const f = fixture(); f.authenticate(); f.state.activePanel = 'access'; f.$('#invitation-email').value = invitation.email;
  f.respond('/api/admin/invitations', { invitation, url: privateURL }, 201); await f.createInvitation({ preventDefault() {} });
  f.switchPanel('availability');
  assert.equal(f.state.invitationLink, null); assert.equal(f.$('#invitation-link').value, '');
  assert.equal(f.state.invitations.length, 1); assert.equal(JSON.stringify(f.state.invitations).includes('fixture-private-token'), false);
});
test('a slow invitation response cannot restore private data after sign-out', async () => {
  const f = fixture(); f.authenticate(); f.state.activePanel = 'access'; f.$('#invitation-email').value = invitation.email;
  let release; f.replies.push({ url: '/api/admin/invitations', handler: () => new Promise(resolve => { release = resolve; }) });
  const pending = f.createInvitation({ preventDefault() {} }); f.showSignedOut();
  release({ ok: true, status: 201, json: async () => ({ invitation, url: privateURL }) }); await pending;
  assert.equal(f.$('#invitation-link').value, ''); assert.equal(f.$('#calendar-email').textContent, '');
  assert.equal(f.$('#session-identity').textContent, ''); assert.equal(f.state.invitations.length, 0);
  assert.equal(f.state.session.authenticated, false); assert.equal(f.state.activePanel, 'bookings');
});
test('invitation links from a different origin are never displayed', async () => {
  const f = fixture(); f.authenticate(); f.state.activePanel = 'access'; f.$('#invitation-email').value = invitation.email;
  f.respond('/api/admin/invitations', { invitation, url: 'https://other.example/admin/#invite=bad-link' }, 201);
  await f.createInvitation({ preventDefault() {} });
  assert.equal(f.$('#invitation-result').hidden, true); assert.equal(f.state.invitationLink, null);
  assert.match(f.$('#invitation-message').textContent, /could not be read/);
});
test('external buffer defaults to 30, preserves zero and saves independently of the job buffer', async () => {
  const f = fixture(); f.authenticate(); f.renderSettings(copy(settings));
  assert.equal(f.fields.get('externalBufferMinutes').value, '30'); assert.equal(f.readSettings().externalBufferMinutes, 30);
  f.renderSettings({ ...copy(settings), externalBufferMinutes: 0, bufferMinutes: 45 });
  assert.equal(f.fields.get('externalBufferMinutes').value, '0');
  const value = f.readSettings(); assert.equal(value.externalBufferMinutes, 0); assert.equal(value.bufferMinutes, 45);
  assert.deepEqual(copy(value.weekly), settings.weekly);
  f.fields.get('externalBufferMinutes').value = '60';
  assert.equal(f.readSettings().externalBufferMinutes, 60); assert.equal(f.readSettings().bufferMinutes, 45);
});
test('legacy setup fragment still bootstraps, while an invitation never consumes setup credentials', async () => {
  const f = fixture('#setup=fixture-bootstrap');
  f.respond('/api/admin/bootstrap', copy(session)); await f.start();
  assert.equal(f.calls[0].url, '/api/admin/bootstrap'); assert.deepEqual(f.calls[0].body, { token: 'fixture-bootstrap' });
  assert.equal(f.$('#portal').hidden, false);
  const invited = fixture('#invite=fixture-private-token&setup=must-not-use');
  await invited.start();
  assert.equal(invited.calls[0].url, '/api/admin/session'); assert.equal(invited.calls.length, 1);
  assert.equal(invited.$('#portal').hidden, true); assert.match(invited.$('#signin-copy').textContent, /private owner invitation/);
});
test('a first setup session clearly continues through identity sign-in without opening management', async () => {
  const f = fixture('#setup=fixture-bootstrap');
  f.respond('/api/admin/bootstrap', { ...copy(session), role: 'bootstrap', actorEmail: '', canManageAccess: false, canConnectCalendar: false });
  await f.start();
  assert.equal(f.calls.length, 1); assert.equal(f.$('#portal').hidden, true);
  assert.equal(f.$('#signin-google .button-label').textContent, 'Finish setup with Google');
  f.respond('/api/admin/signin', { error: 'Setup unavailable', code: 'configuration_required' }, 409);
  await f.signInGoogle();
  assert.equal(f.calls[1].headers['X-CSRF-Token'], 'fixture-csrf');
  assert.match(f.$('#page-message').textContent, /Setup unavailable/);
});
test('bootstrap retains one-time private configuration recovery before Google sign-in', async () => {
  const f = fixture('#setup=fixture-bootstrap');
  const setup = { ...copy(session), role: 'bootstrap', actorEmail: '', canManageAccess: false, canConnectCalendar: true, google: { configured: true, connected: false, mode: 'desktop', requiresClientConfiguration: true } };
  f.respond('/api/admin/bootstrap', setup); await f.start();
  assert.equal(f.$('#google-configuration').hidden, false);
  assert.ok(f.$('#entry-configuration').children.includes(f.$('#google-configuration')));
  assert.equal(f.$('#signin-google').disabled, true); assert.equal(f.$('#connect-google').hidden, true);
  const data = JSON.stringify({ installed: { client_id: 'fixture-client.apps.googleusercontent.com', client_secret: 'fixture-secret-only' } });
  f.$('#google-configuration-file').files = [{ size: Buffer.byteLength(data), text: async () => data }];
  f.respond('/api/admin/google/configure', null, 204);
  f.respond('/api/admin/session', { ...setup, google: { ...setup.google, requiresClientConfiguration: false } });
  await f.$('#google-configuration-form').emit('submit');
  assert.equal(f.calls[1].headers['X-CSRF-Token'], 'fixture-csrf');
  assert.equal(f.calls[1].body.clientSecret, 'fixture-secret-only');
  assert.equal(f.$('#google-configuration-file').value, ''); assert.equal(f.$('#google-configuration').hidden, true);
  assert.equal(f.$('#signin-google').disabled, false); assert.equal(f.document.activeElement, f.$('#signin-google'));
  assert.match(f.$('#page-message').textContent, /Finish setup with Google/);
  assert.doesNotMatch(f.$('#page-message').textContent, /fixture-secret/);
});
test('successful identity login stays on bookings; invitation failures give safe recovery instructions', async () => {
  const signedIn = fixture('', '?google=signed_in'); await signedIn.start();
  assert.equal(signedIn.state.activePanel, 'bookings');
  assert.equal(signedIn.calls.some(call => call.url === '/api/admin/google/connect'), false);
  assert.equal(signedIn.window.location.search, '');
  for (const [outcome, expected] of [['wrong_account', /reopen its private link/], ['invalid_invitation', /new private link/], ['invitation_expired', /new private link/], ['invitation_revoked', /new private link/], ['invitation_used', /already been accepted/], ['owner_transfer_required', /ownership transfer/]]) {
    const f = fixture('', `?google=${outcome}`);
    f.respond('/api/admin/session', { authenticated: false, google: { configured: true } }); await f.start();
    assert.equal(f.$('#portal').hidden, true); assert.equal(f.$('#signin-google').hidden, false);
    assert.match(f.$('#page-message').textContent, expected); assert.equal(f.calls.length, 1);
  }
});

(async () => {
  for (const { name, run } of tests) { try { await run(); } catch (error) { error.message = `${name}: ${error.message}`; throw error; } }
  console.log(`Admin access checks passed (${tests.length}): separate sign-in/calendar actions, role gates, private invitation lifecycle, fragment clearing, stale response cleanup and independent buffers.`);
})().catch(error => { console.error(error); process.exitCode = 1; });
