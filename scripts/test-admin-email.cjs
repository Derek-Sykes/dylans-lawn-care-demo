'use strict';

// Exercise the real admin controller with isolated DOM/API responses. No live sends.
const assert = require('node:assert/strict');
const { fixture } = require('./test-admin-access.cjs');
const copy = value => JSON.parse(JSON.stringify(value));
const settings = { enabled: false, remindersEnabled: true, reminderHours: 24 };
const connection = { connected: true, email: 'sender@example.test', canConnect: true, error: '' };
const queued = { id: 'queued', to: 'customer@example.test', subject: 'We received your request', kind: 'receipt', status: 'queued', error: '', createdAt: '2030-09-11T12:00:00Z', scheduledAt: '2030-09-11T12:00:00Z' };
const uncertain = { ...queued, id: 'uncertain', subject: 'Your appointment is confirmed', kind: 'confirmation', status: 'uncertain' };
const failed = { ...queued, id: 'failed', subject: 'Your appointment time changed', kind: 'reschedule', status: 'failed', error: 'Gmail needs attention. Please reconnect.' };
const data = (overrides = {}) => ({ settings: copy(settings), connection: copy(connection), history: [], ...copy(overrides) });
const tests = [];
const test = (name, run) => tests.push({ name, run });
async function ready(overrides = {}, actor = {}) {
  const f = fixture(); f.authenticate(actor); f.state.activePanel = 'emails';
  f.respond('/api/admin/email', data(overrides)); assert.equal(await f.loadEmails(), true); return f;
}

test('Emails load shared settings, preserve the disabled default and never send implicitly', async () => {
  const f = await ready();
  assert.equal(f.$('#email-enabled').checked, false); assert.equal(f.$('#email-reminders').checked, true); assert.equal(f.$('#email-reminder-hours').value, '24');
  assert.equal(f.$('#save-email-settings').disabled, true);
  assert.match(f.$('#email-sender').textContent, /sender@example.test/);
  assert.match(f.$('#email-test-help').textContent, /only to sender@example.test/);
  assert.equal(f.calls.length, 1); assert.equal(f.calls[0].method, 'GET');
});

test('only the Calendar identity can connect or disconnect Gmail; co-owners manage shared preferences', async () => {
  const f = await ready({ connection: { ...connection, canConnect: false } }, { role: 'owner', canManageAccess: false });
  assert.equal(f.$('#connect-email').hidden, true); assert.equal(f.$('#disconnect-email').hidden, true);
  assert.equal(f.$('#email-settings-fields').disabled, false);
  await f.connectEmail(); await f.$('#disconnect-email').emit('click'); await f.emailCommand('disconnect');
  assert.equal(f.calls.length, 1); assert.equal(f.state.pendingEmailDisconnect, false);
  f.$('#email-enabled').checked = true; f.markEmailDirty();
  f.respond('/api/admin/email/settings', { settings: { ...settings, enabled: true } }); await f.saveEmailSettings({ preventDefault() {} });
  assert.equal(f.calls[1].method, 'PUT'); assert.equal(f.calls[1].headers['X-CSRF-Token'], 'fixture-csrf');
});

test('existing Calendar installs can enable Gmail once without replacing the Calendar grant', async () => {
  const f = await ready({ connection: { ...connection, connected: false } });
  assert.equal(f.$('#connect-email .button-label').textContent, 'Enable Gmail');
  assert.match(f.$('#email-connection-description').textContent, /Calendar is already set up/);
  f.respond('/api/admin/email/connect', { url: 'https://accounts.google.com/o/oauth2/v2/auth?scope=gmail.send' }); await f.connectEmail();
  assert.equal(f.calls[1].url, '/api/admin/email/connect'); assert.deepEqual(f.calls[1].body, {});
  assert.equal(f.calls.some(call => call.url === '/api/admin/google/connect'), false);
  assert.equal(f.navigations.length, 1); assert.match(f.navigations[0], /gmail.send/);
  const bad = await ready({ connection: { ...connection, connected: false } });
  bad.respond('/api/admin/email/connect', { url: 'https://example.evil/steal' }); await bad.connectEmail();
  assert.equal(bad.navigations.length, 0); assert.equal(bad.state.emailBusy, ''); assert.match(bad.$('#emails-message').textContent, /could not be opened/);
});

test('every Gmail OAuth outcome returns to Emails without starting another connection', async () => {
  for (const outcome of ['gmail_connected', 'gmail_denied', 'gmail_failed', 'gmail_wrong_account', 'gmail_missing_scopes', 'gmail_configuration_required']) {
    const f = fixture('', `?google=${outcome}`); f.respond('/api/admin/email', data()); await f.start();
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(f.state.activePanel, 'emails'); assert.equal(f.$('#emails-message').hidden, false);
    assert.equal(f.calls.some(call => call.method === 'POST'), false); assert.equal(f.window.location.search, '');
    assert.equal(f.$('#calendar-message').textContent, '');
  }
});

test('email settings support each reminder time and survive refresh while being edited', async () => {
  const f = await ready();
  f.$('#email-enabled').checked = true; f.$('#email-reminder-hours').value = '6'; f.markEmailDirty();
  f.respond('/api/admin/email', data({ settings: { ...settings, reminderHours: 12 } })); await f.loadEmails();
  assert.equal(f.$('#email-enabled').checked, true); assert.equal(f.$('#email-reminder-hours').value, '6'); assert.equal(f.state.emailDirty, true);
  for (const hours of [1, 2, 6, 12, 24, 48]) {
    f.$('#email-reminder-hours').value = String(hours); f.markEmailDirty();
    const saved = { enabled: true, remindersEnabled: true, reminderHours: hours };
    f.respond('/api/admin/email/settings', { settings: saved }); await f.saveEmailSettings({ preventDefault() {} });
    assert.deepEqual(f.calls.at(-1).body, saved); assert.equal(f.state.emailDirty, false);
    assert.match(f.$('#emails-message').textContent, /Enabling does not email existing appointments/);
  }
  f.$('#email-reminder-hours').value = '7'; f.markEmailDirty(); const count = f.calls.length;
  await f.saveEmailSettings({ preventDefault() {} }); assert.equal(f.calls.length, count); assert.match(f.$('#emails-message').textContent, /Choose a reminder time/);
  await f.$('#reset-email-settings').emit('click'); assert.equal(f.$('#email-reminder-hours').value, '48'); assert.equal(f.state.emailDirty, false);
});

test('a Gmail redirect cannot discard an unsaved preference change', async () => {
  const f = await ready({ connection: { ...connection, connected: false } });
  f.$('#email-enabled').checked = true; f.markEmailDirty(); await f.connectEmail();
  assert.equal(f.calls.length, 1); assert.match(f.$('#emails-message').textContent, /Save or discard/);
});

test('test send has no editable recipient, queues only on a deliberate action and reports acceptance honestly', async () => {
  const f = await ready();
  f.respond('/api/admin/email/test', { message: { ...queued, kind: 'test', to: connection.email } }, 202);
  f.respond('/api/admin/email', data({ history: [{ ...queued, kind: 'test', to: connection.email }] }));
  await f.$('#send-test-email').emit('click');
  assert.equal(f.calls[1].url, '/api/admin/email/test'); assert.deepEqual(f.calls[1].body, {});
  assert.match(f.$('#emails-message').textContent, /Test queued for the connected sender only/);
  assert.doesNotMatch(f.$('#emails-message').textContent, /delivered|sent successfully/i);
  const disconnected = await ready({ connection: { ...connection, connected: false } });
  await disconnected.emailCommand('test'); assert.equal(disconnected.calls.length, 1); assert.equal(disconnected.$('#send-test-email').disabled, true);
});

test('history labels reflect queued, sending, accepted, failed, uncertain and skipped states as plain text', async () => {
  const history = ['queued', 'sending', 'sent', 'failed', 'uncertain', 'skipped'].map((status, i) => ({ ...queued, id: String(i), status, subject: '<img src=x> Customer appointment' }));
  const f = await ready({ history });
  const labels = f.$('#email-history').querySelectorAll('.badge').map(item => item.textContent);
  assert.deepEqual(labels, ['Queued', 'Sending', 'Sent via Gmail', 'Needs attention', 'Delivery unconfirmed', 'Skipped']);
  assert.equal(f.$('#email-history').querySelectorAll('button').length, 2);
  assert.equal(f.$('#email-history').querySelector('h3').textContent, '<img src=x> Customer appointment'); assert.equal(f.$('#email-history').querySelector('h3').children.length, 0);
});

test('uncertain retry defaults to keeping the result and requires separate duplicate-risk confirmation', async () => {
  const f = await ready({ history: [uncertain] });
  await f.retryEmail(uncertain); assert.equal(f.calls.length, 1);
  await f.emailButton('Review retry').emit('click');
  assert.equal(f.document.activeElement, f.emailButton('Keep as is')); assert.equal(f.calls.length, 1);
  assert.match(f.$('#email-history').querySelector('.email-confirmation').querySelector('p').textContent, /may already have been sent.*duplicate/);
  await f.emailButton('Keep as is').emit('click'); assert.equal(f.state.pendingEmailRetryId, null); assert.equal(f.document.activeElement, f.emailButton('Review retry'));
  await f.emailButton('Review retry').emit('click');
  f.respond('/api/admin/email/messages/uncertain/retry', { message: { ...uncertain, status: 'queued' } }, 202);
  f.respond('/api/admin/email', data({ history: [{ ...uncertain, status: 'queued' }] }));
  await f.emailButton('Send again anyway').emit('click');
  assert.deepEqual(f.calls[1].body, { confirmUncertain: true }); assert.equal(f.calls[1].headers['X-CSRF-Token'], 'fixture-csrf');
  assert.equal(f.state.pendingEmailRetryId, null); assert.equal(f.state.email.history[0].status, 'queued');
});

test('failed retry has no uncertainty override and pending or completed messages cannot be retried', async () => {
  const f = await ready({ history: [failed, queued] });
  f.respond('/api/admin/email/messages/failed/retry', { message: { ...failed, status: 'queued' } }, 202);
  f.respond('/api/admin/email', data({ history: [{ ...failed, status: 'queued' }, queued] })); await f.retryEmail(failed);
  assert.deepEqual(f.calls[1].body, {}); const count = f.calls.length;
  await f.retryEmail(queued); await f.retryEmail({ ...queued, status: 'sent' }); assert.equal(f.calls.length, count);
});

test('Gmail disconnect requires inline confirmation and preserves Calendar while turning emails off', async () => {
  const f = await ready({ settings: { ...settings, enabled: true } });
  await f.emailCommand('disconnect'); assert.equal(f.calls.length, 1);
  await f.$('#disconnect-email').emit('click'); assert.equal(f.document.activeElement, f.$('#keep-email-connection'));
  await f.$('#keep-email-connection').emit('click'); assert.equal(f.state.pendingEmailDisconnect, false);
  await f.$('#disconnect-email').emit('click'); const before = JSON.stringify(f.state.session.google);
  f.respond('/api/admin/email/disconnect', null, 204); f.respond('/api/admin/email', data({ connection: { ...connection, connected: false }, settings }));
  await f.$('#confirm-email-disconnect').emit('click');
  assert.equal(f.calls[1].url, '/api/admin/email/disconnect'); assert.equal(JSON.stringify(f.state.session.google), before);
  assert.equal(f.$('#email-enabled').checked, false); assert.match(f.$('#emails-message').textContent, /Calendar and appointments are unchanged/);
});

test('leaving Emails cancels uncertainty and disconnect confirmations without sending', async () => {
  const f = await ready({ history: [uncertain] }); await f.emailButton('Review retry').emit('click'); await f.$('#disconnect-email').emit('click');
  f.switchPanel('availability'); assert.equal(f.state.pendingEmailRetryId, null); assert.equal(f.state.pendingEmailDisconnect, false);
  await f.retryEmail(uncertain); await f.emailCommand('disconnect'); assert.equal(f.calls.length, 1);
});

test('slow loads cannot overwrite saved email preferences and duplicate saves are blocked', async () => {
  const f = await ready(); let releaseLoad; f.replies.push({ url: '/api/admin/email', handler: () => new Promise(resolve => { releaseLoad = resolve; }) });
  const loading = f.loadEmails(); f.$('#email-enabled').checked = true; f.markEmailDirty();
  let releaseSave; f.replies.push({ url: '/api/admin/email/settings', handler: () => new Promise(resolve => { releaseSave = resolve; }) });
  const saving = f.saveEmailSettings({ preventDefault() {} }); await f.saveEmailSettings({ preventDefault() {} });
  assert.equal(f.calls.filter(call => call.method === 'PUT').length, 1);
  releaseSave({ ok: true, status: 200, json: async () => ({ settings: { ...settings, enabled: true } }) }); await saving;
  releaseLoad({ ok: true, status: 200, json: async () => data() }); await loading;
  assert.equal(f.state.email.settings.enabled, true); assert.equal(f.$('#email-enabled').checked, true); assert.equal(f.state.loadingEmails, false);
});

test('sign-out removes sender, history and pending actions and ignores late private responses', async () => {
  for (const mode of ['load', 'retry']) {
    const f = await ready({ history: [uncertain] }); await f.emailButton('Review retry').emit('click');
    let release; f.replies.push({ url: mode === 'load' ? '/api/admin/email' : '/api/admin/email/messages/uncertain/retry', handler: () => new Promise(resolve => { release = resolve; }) });
    const pending = mode === 'load' ? f.loadEmails() : f.retryEmail(uncertain); f.showSignedOut();
    release({ ok: true, status: 200, json: async () => mode === 'load' ? data({ history: [uncertain] }) : ({ message: queued }) }); await pending;
    assert.equal(f.state.email, null); assert.equal(f.state.emailBusy, ''); assert.equal(f.state.pendingEmailRetryId, null); assert.equal(f.state.emailDirty, false);
    assert.equal(f.$('#email-sender').textContent, ''); assert.equal(f.$('#email-history').querySelectorAll('article').length, 0);
    assert.equal(f.$('#emails-message').textContent, ''); assert.equal(f.$('#email-history').attributes['aria-busy'], 'false');
  }
});

test('removed or bootstrap actors cannot load or use the email feature', async () => {
  const f = await ready(); f.respond('/api/admin/email', { error: 'Please sign in again' }, 401); await f.loadEmails();
  assert.equal(f.state.session.authenticated, false); assert.equal(f.state.email, null); const count = f.calls.length;
  await f.loadEmails(); await f.connectEmail(); await f.emailCommand('test'); assert.equal(f.calls.length, count);
  f.authenticate({ role: 'bootstrap' }); f.renderConnection(); await f.loadEmails(); f.switchPanel('emails');
  assert.equal(f.calls.length, count); assert.notEqual(f.state.activePanel, 'emails');
});

test('invalid responses and failed saves preserve drafts with a recoverable message', async () => {
  const f = await ready(); f.$('#email-enabled').checked = true; f.markEmailDirty();
  f.respond('/api/admin/email/settings', { error: 'Preferences could not be saved. Please try again.' }, 503); await f.saveEmailSettings({ preventDefault() {} });
  assert.equal(f.state.emailDirty, true); assert.equal(f.$('#email-enabled').checked, true); assert.equal(f.$('#save-email-settings').disabled, false);
  f.respond('/api/admin/email', data({ history: [{ ...queued, error: { token: 'must-not-render' } }] })); assert.equal(await f.loadEmails(), false);
  assert.match(f.$('#emails-message').textContent, /could not be read/); assert.doesNotMatch(f.$('#emails-message').textContent, /must-not-render/);
});

(async () => {
  for (const { name, run } of tests) { try { await run(); } catch (error) { error.message = `${name}: ${error.message}`; throw error; } }
  console.log(`Admin email checks passed (${tests.length}): existing-calendar Gmail upgrade, shared preferences, sender-only tests, truthful statuses, deliberate retries, reminder choices, privacy and stale responses.`);
})().catch(error => { console.error(error); process.exitCode = 1; });
