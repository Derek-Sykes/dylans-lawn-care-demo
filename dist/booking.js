'use strict';
(() => {
  const $ = selector => document.querySelector(selector);
  const state = { config: null, slots: [], selected: null, loadingSlots: false, slotRequest: 0, slotController: null, submitting: false, attempt: null, receipt: null, receiptTimer: null };
  const form = $('#booking-form');
  const dateInput = $('#appointment-date');
  const serviceInput = $('#service');
  const submitButton = $('#submit-booking');
  const fields = ['#service-fields', '#time-fields', '#customer-fields'].map($);
  class APIError extends Error { constructor(message, status = 0, code = '') { super(message); this.status = status; this.code = code; } }
  async function api(path, { method = 'GET', body, signal } = {}) {
    const controller = new AbortController();
    const abort = () => controller.abort();
    if (signal?.aborted) controller.abort();
    else signal?.addEventListener('abort', abort, { once: true });
    const timeout = setTimeout(abort, 25000);
    try {
      const response = await fetch(path, { method, cache: 'no-store', credentials: 'same-origin', signal: controller.signal, headers: { Accept: 'application/json', ...(body ? { 'Content-Type': 'application/json' } : {}) }, ...(body ? { body: JSON.stringify(body) } : {}) });
      let data = null;
      try { data = await response.json(); } catch { /* A failed response may have no JSON body. */ }
      if (!response.ok) throw new APIError(typeof data?.error === 'string' ? data.error : 'We couldn’t complete that request. Please try again.', response.status, data?.code);
      if (!data || typeof data !== 'object') throw new APIError('The scheduling service returned an incomplete response. Please try again.');
      return data;
    } catch (error) {
      if (error instanceof APIError || signal?.aborted) throw error;
      throw new APIError('We couldn’t reach the scheduling service. Please check your connection and try again.');
    } finally { clearTimeout(timeout); signal?.removeEventListener('abort', abort); }
  }
  function notice(message) { const element = $('#submit-message'); element.textContent = message; element.hidden = !message; }
  function localDate(date, timeZone) {
    const parts = new Intl.DateTimeFormat('en-US', { timeZone, year: 'numeric', month: '2-digit', day: '2-digit' }).formatToParts(date);
    const part = type => parts.find(item => item.type === type).value;
    return `${part('year')}-${part('month')}-${part('day')}`;
  }
  function addDays(value, days) { const date = new Date(`${value}T12:00:00Z`); date.setUTCDate(date.getUTCDate() + days); return date.toISOString().slice(0, 10); }
  function formatTime(value) { return new Intl.DateTimeFormat('en-US', { timeZone: state.config.timeZone, hour: 'numeric', minute: '2-digit', timeZoneName: 'short' }).format(new Date(value)); }
  function formatAppointment(value) { return new Intl.DateTimeFormat('en-US', { timeZone: state.config.timeZone, weekday: 'long', month: 'long', day: 'numeric', year: 'numeric', hour: 'numeric', minute: '2-digit' }).format(new Date(value)); }
  function serviceName() { return state.config.services.find(service => service.id === serviceInput.value)?.name || 'Project conversation'; }
  function updateSummary() {
    const visible = Boolean(state.selected);
    $('#selection-summary').hidden = !visible;
    if (visible) $('#selection-text').textContent = `${serviceName()} · ${formatAppointment(state.selected.start)} · ${state.config.timeZone.replaceAll('_', ' ')}`;
  }
  function lockFields(locked) { fields.forEach(field => { field.disabled = locked; }); }
  function setSubmitting(submitting) {
    state.submitting = submitting;
    submitButton.disabled = submitting;
    submitButton.querySelector('.button-label').textContent = submitting ? 'Saving your request…' : state.attempt ? 'Check & retry this request' : 'Request this time';
    form.setAttribute('aria-busy', String(submitting));
    lockFields(submitting || Boolean(state.attempt));
  }
  async function loadSlots() {
    if (!state.config || state.attempt) return;
    state.slotController?.abort();
    const controller = new AbortController();
    state.slotController = controller;
    const request = ++state.slotRequest;
    state.selected = null;
    state.slots = [];
    $('#time-slots').replaceChildren();
    $('#retry-slots').hidden = true;
    updateSummary();
    notice('');
    if (!dateInput.value || !dateInput.checkValidity()) { $('#slots-status').textContent = 'Choose a date within the available booking window.'; state.loadingSlots = false; return; }
    const date = dateInput.value;
    state.loadingSlots = true;
    $('#available-times').setAttribute('aria-busy', 'true');
    $('#slots-status').textContent = 'Checking available times…';
    try {
      const data = await api(`/api/public/slots?date=${encodeURIComponent(date)}`, { signal: controller.signal });
      if (request !== state.slotRequest) return;
      if (data.date !== date || !Array.isArray(data.slots)) throw new APIError('We couldn’t read the available times. Please try again.');
      if (data.timeZone && data.timeZone !== state.config.timeZone) {
        new Intl.DateTimeFormat('en-US', { timeZone: data.timeZone }).format();
        state.config.timeZone = data.timeZone;
        $('#timezone-note').textContent = `All times are shown in ${data.timeZone.replaceAll('_', ' ')}.`;
      }
      state.slots = data.slots.filter(slot => slot && Number.isFinite(Date.parse(slot.start)) && Number.isFinite(Date.parse(slot.end)));
      $('#slots-status').textContent = state.slots.length ? `${state.slots.length} ${state.slots.length === 1 ? 'time is' : 'times are'} available. Choose one below.` : 'No times are available on this date. Try another day, or call Dylan to arrange a conversation.';
      const fragment = document.createDocumentFragment();
      state.slots.forEach((slot, index) => {
        const label = document.createElement('label'); label.className = 'time-choice';
        const input = document.createElement('input'); input.type = 'radio'; input.name = 'appointment-time'; input.value = slot.start; input.required = true; input.id = `time-${index}`;
        const caption = document.createElement('span'); caption.textContent = formatTime(slot.start);
        input.addEventListener('change', () => { state.selected = slot; updateSummary(); notice(''); });
        label.append(input, caption); fragment.append(label);
      });
      $('#time-slots').replaceChildren(fragment);
    } catch (error) {
      if (request !== state.slotRequest || controller.signal.aborted) return;
      $('#slots-status').textContent = error.message || 'Available times could not be loaded. Please try again.';
      $('#retry-slots').hidden = false;
    } finally {
      if (request === state.slotRequest) { state.loadingSlots = false; $('#available-times').setAttribute('aria-busy', 'false'); }
    }
  }
  async function loadConfig() {
    $('#booking-loading').hidden = false; $('#booking-unavailable').hidden = true; form.hidden = true;
    try {
      const data = await api('/api/public/config');
      if (!Array.isArray(data.services) || !data.timeZone || !Number.isFinite(data.horizonDays)) throw new APIError('Appointment options are unavailable right now. Please call Dylan or try again.');
      new Intl.DateTimeFormat('en-US', { timeZone: data.timeZone }).format();
      state.config = data;
      if (!data.bookingEnabled) {
        $('#unavailable-message').textContent = 'Online appointment times are unavailable right now. Call Dylan to discuss your project and arrange a conversation.';
        $('#booking-unavailable').hidden = false;
        return;
      }
      serviceInput.replaceChildren(new Option('Choose a service', ''));
      data.services.forEach(service => { if (typeof service.id === 'string' && typeof service.name === 'string') serviceInput.add(new Option(service.name, service.id)); });
      const today = localDate(new Date(), data.timeZone);
      dateInput.min = today; dateInput.max = addDays(today, data.horizonDays);
      dateInput.value = addDays(today, Math.min(data.horizonDays, Math.floor((data.minNoticeHours || 0) / 24)));
      $('#appointment-description').textContent = `${data.slotMinutes}-minute estimate or callback conversations.`;
      $('#timezone-note').textContent = `All times are shown in ${data.timeZone.replaceAll('_', ' ')}.`;
      form.hidden = false;
      await loadSlots();
    } catch (error) {
      $('#unavailable-message').textContent = error.message || 'Appointment options could not be loaded. Please call Dylan or try again.';
      $('#booking-unavailable').hidden = false;
    } finally { $('#booking-loading').hidden = true; }
  }
  function createKey() {
    if (typeof crypto.randomUUID === 'function') return crypto.randomUUID();
    const bytes = new Uint8Array(16); crypto.getRandomValues(bytes); return Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('');
  }
  function showSuccess(result, attempt, focus = true) {
    form.hidden = true;
    $('#booking-success').hidden = false;
    $('#success-service').textContent = state.config.services.find(service => service.id === attempt.serviceId)?.name || 'Project conversation';
    $('#success-time').textContent = formatAppointment(result.start);
    const duration = Math.round((Date.parse(result.end) - Date.parse(result.start)) / 60000);
    $('#success-timezone').textContent = `${Number.isFinite(duration) && duration > 0 ? duration : state.config.slotMinutes} minutes · ${state.config.timeZone.replaceAll('_', ' ')}`;
    $('#success-reference').textContent = result.id;
    const cancelled = result.status === 'cancelled';
    $('#success-title').textContent = cancelled ? 'This request was cancelled.' : 'Your request is saved.';
    $('#success-description').textContent = cancelled ? 'This request already exists and has been cancelled. Call Dylan if you need a new appointment.' : result.status === 'confirmed' ? 'Dylan has marked this request confirmed. Your calendar status is shown below.' : 'Your details have been received. Keep the reference below if you need to check on your request.';
    const synced = result.calendarStatus === 'synced';
    const sync = $('#success-sync');
    sync.className = `status-badge${synced ? ' synced' : ''}`;
    sync.textContent = cancelled ? (synced ? 'Removed from Google Calendar' : 'Calendar removal pending') : synced ? 'Added to Google Calendar' : result.calendarStatus === 'failed' ? 'Calendar update needs attention' : 'Calendar update pending';
    $('#success-sync-note').textContent = cancelled ? 'This time is no longer an active appointment request.' : synced ? 'This reserves time for a conversation about your project. It does not schedule lawn-service work.' : 'Your request is saved, but it is not yet confirmed in Google Calendar. Call Dylan if you need to check the time before making plans.';
    if (focus) $('#success-title').focus();
  }
  function scheduleReceiptCheck(delay) {
    clearTimeout(state.receiptTimer);
    const receipt = state.receipt;
    if (!receipt || receipt.checks >= 2 || ['synced', 'failed'].includes(receipt.result.calendarStatus)) return;
    state.receiptTimer = setTimeout(async () => {
      if (document.hidden || state.receipt !== receipt || receipt.busy) return;
      receipt.busy = true; receipt.checks++;
      try {
        // Reuse the exact accepted payload and key: this checks the saved request.
        const result = await api('/api/public/bookings', { method: 'POST', body: receipt.payload });
        if (state.receipt === receipt && result.id === receipt.result.id && Number.isFinite(Date.parse(result.start))) {
          receipt.result = result;
          showSuccess(result, receipt.payload, false);
        }
      } catch { /* Keep the last verified status; never imply a calendar confirmation. */ }
      finally { receipt.busy = false; if (state.receipt === receipt) scheduleReceiptCheck(5000); }
    }, delay);
  }
  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (state.submitting) return;
    if (!state.attempt) {
      if (!form.reportValidity()) return;
      if (state.loadingSlots || !state.selected) { notice('Please choose an available time before sending your request.'); dateInput.focus(); return; }
      const values = Object.fromEntries(new FormData(form));
      state.attempt = { serviceId: serviceInput.value, start: state.selected.start, name: values.name.trim(), email: values.email.trim(), phone: values.phone.trim(), address: values.address.trim(), notes: values.notes.trim(), idempotencyKey: createKey() };
      if (!state.attempt.name || !state.attempt.phone || !state.attempt.address || !state.attempt.email) { state.attempt = null; notice('Please complete your name, phone, email and property address.'); return; }
    }
    const attempt = state.attempt;
    notice(''); setSubmitting(true);
    try {
      const result = await api('/api/public/bookings', { method: 'POST', body: attempt });
      if (!result.id || !Number.isFinite(Date.parse(result.start))) throw new APIError('Your request may have been saved, but we couldn’t read the result.');
      showSuccess(result, attempt);
      state.receipt = { payload: attempt, result, checks: 0, busy: false };
      scheduleReceiptCheck(2000);
      state.attempt = null;
    } catch (error) {
      const invalidDetails = error.status === 400 && ['invalid_booking', 'invalid_email', 'invalid_phone', 'invalid_start'].includes(error.code);
      const unavailableTime = error.status === 409 && error.code === 'slot_unavailable';
      if (invalidDetails || unavailableTime) {
        state.attempt = null;
        if (unavailableTime) { await loadSlots(); notice(`${error.message} Please choose an available time and try again.`); }
        else notice(error.message);
      } else {
        notice(error.code === 'idempotency_mismatch' ? 'This request identifier is already associated with saved details. Please keep this page open and call Dylan at 410-365-1265 to check the request before booking again.' : `${error.status === 429 ? 'Please wait a minute before trying again. ' : ''}We couldn’t verify whether your request finished saving. Keep this page open and use “Check & retry this request” below. It will safely check the same request without creating a duplicate.`);
      }
    } finally { setSubmitting(false); }
  });
  dateInput.addEventListener('change', loadSlots);
  serviceInput.addEventListener('change', updateSummary);
  $('#retry-slots').addEventListener('click', loadSlots);
  $('#reload-config').addEventListener('click', loadConfig);
  window.addEventListener('pagehide', () => clearTimeout(state.receiptTimer));
  loadConfig();
})();
