'use strict';
(() => {
  const $ = selector => document.querySelector(selector);
  const kind = new URLSearchParams(window.location.search).get('type') === 'estimate' ? 'estimate' : 'service';
  const isEstimate = kind === 'estimate';
  const requestLabel = isEstimate ? 'Request estimate or callback' : 'Request appointment';
  document.querySelectorAll('[data-booking-kind]').forEach(link => {
    if (link.dataset.bookingKind === kind) link.setAttribute('aria-current', 'page');
    else link.removeAttribute('aria-current');
  });
  if (isEstimate) {
    document.title = 'Request an estimate or callback · Dylan’s Lawn Care';
    document.querySelector('meta[name="description"]').content = 'Request an estimate or a callback from Dylan’s Lawn Care. Share your property details and choose a short conversation before booking the work.';
    $('#booking-eyebrow').textContent = 'REQUEST AN ESTIMATE OR CALLBACK';
    const emphasis = document.createElement('em'); emphasis.textContent = 'project.';
    $('#booking-heading').replaceChildren('Plan your ', document.createElement('br'), 'next ', emphasis);
    $('#booking-introduction').textContent = 'Need a price or some advice before booking the work? Share your property details and choose a time for Dylan to call you.';
    $('#booking-scope').textContent = 'This reserves a short estimate or callback conversation. Book a service when you’re ready to schedule the work.';
    $('#service-description').textContent = 'What would you like an estimate or advice about?';
    $('#time-heading').textContent = 'When can Dylan call?';
    $('#booking-privacy').textContent = 'Your details go to Dylan so he can prepare for the call and discuss an estimate. You may receive emails about this request and a reminder. This request does not book a service visit.';
    $('#success-eyebrow').textContent = 'THANK YOU FOR GETTING IN TOUCH';
    $('#submit-booking .button-label').textContent = requestLabel;
  }

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
  function serviceName() { return state.config.services.find(service => service.id === serviceInput.value)?.name || 'Service appointment'; }
  function updateSummary() {
    const visible = Boolean(state.selected);
    $('#selection-summary').hidden = !visible;
    if (visible) $('#selection-text').textContent = `${serviceName()} · ${formatAppointment(state.selected.start)} · ${state.config.timeZone.replaceAll('_', ' ')}`;
  }
  function lockFields(locked) { fields.forEach(field => { field.disabled = locked; }); }
  function setSubmitting(submitting) {
    state.submitting = submitting;
    submitButton.disabled = submitting;
    submitButton.querySelector('.button-label').textContent = submitting ? 'Saving your request…' : state.attempt ? 'Check & retry this request' : requestLabel;
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
      const data = await api(`/api/public/slots?date=${encodeURIComponent(date)}&kind=${kind}`, { signal: controller.signal });
      if (request !== state.slotRequest) return;
      if (data.date !== date || !Array.isArray(data.slots)) throw new APIError('We couldn’t read the available times. Please try again.');
      if (data.timeZone && data.timeZone !== state.config.timeZone) {
        new Intl.DateTimeFormat('en-US', { timeZone: data.timeZone }).format();
        state.config.timeZone = data.timeZone;
        $('#timezone-note').textContent = `All times are shown in ${data.timeZone.replaceAll('_', ' ')}.`;
      }
      state.slots = data.slots.filter(slot => slot && Number.isFinite(Date.parse(slot.start)) && Number.isFinite(Date.parse(slot.end)));
      $('#slots-status').textContent = state.slots.length ? `${state.slots.length} ${state.slots.length === 1 ? 'time is' : 'times are'} available. Choose one below.` : 'No times are available on this date. Try another day, or call Dylan at 410-365-1265.';
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
      if (!Array.isArray(data.services) || !data.timeZone || !Number.isFinite(data.horizonDays) || !Number.isInteger(isEstimate ? data.estimateMinutes : data.slotMinutes) || (isEstimate ? data.estimateMinutes : data.slotMinutes) < 1) throw new APIError('Appointment options are unavailable right now. Please call Dylan or try again.');
      new Intl.DateTimeFormat('en-US', { timeZone: data.timeZone }).format();
      state.config = data;
      if (!data.bookingEnabled) {
        $('#unavailable-message').textContent = isEstimate ? 'Online callback times are unavailable right now. Call Dylan to request an estimate.' : 'Online appointment times are unavailable right now. Call Dylan to arrange your service appointment.';
        $('#booking-unavailable').hidden = false;
        return;
      }
      serviceInput.replaceChildren(new Option('Choose a service', ''));
      data.services.forEach(service => { if (typeof service.id === 'string' && typeof service.name === 'string') serviceInput.add(new Option(service.name, service.id)); });
      const today = localDate(new Date(), data.timeZone);
      dateInput.min = today; dateInput.max = addDays(today, data.horizonDays);
      dateInput.value = addDays(today, Math.min(data.horizonDays, Math.floor((data.minNoticeHours || 0) / 24)));
      $('#appointment-description').textContent = isEstimate ? `${data.estimateMinutes}-minute estimate or callback conversations. Your service visit is booked separately.` : `${data.slotMinutes}-minute service appointments. Dylan can adjust the length for your job.`;
      $('#timezone-note').textContent = `All times are shown in ${data.timeZone.replaceAll('_', ' ')}.`;
      form.hidden = false;
      await loadSlots();
    } catch (error) {
      $('#unavailable-message').textContent = error.message || 'Appointment options could not be loaded. Please call Dylan or try again.';
      $('#booking-unavailable').hidden = false;
    } finally { $('#booking-loading').hidden = true; }
  }
  function validReceipt(result) {
    return result && typeof result.id === 'string' && result.id.length > 0 && result.id.length <= 128
      && result.kind === kind
      && ['needs_followup', 'contacted', 'confirmed', 'cancelled'].includes(result.status)
      && ['pending', 'synced', 'failed'].includes(result.calendarStatus)
      && Number.isFinite(Date.parse(result.start)) && Number.isFinite(Date.parse(result.end))
      && Date.parse(result.end) > Date.parse(result.start);
  }
  function createKey() {
    if (typeof crypto.randomUUID === 'function') return crypto.randomUUID();
    const bytes = new Uint8Array(16); crypto.getRandomValues(bytes); return Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('');
  }
  function showSuccess(result, attempt, focus = true) {
    form.hidden = true;
    $('#booking-success').hidden = false;
    $('#success-service').textContent = state.config.services.find(service => service.id === attempt.serviceId)?.name || 'Service appointment';
    $('#success-time').textContent = formatAppointment(result.start);
    const duration = Math.round((Date.parse(result.end) - Date.parse(result.start)) / 60000);
    $('#success-timezone').textContent = `${duration} minutes · ${state.config.timeZone.replaceAll('_', ' ')}`;
    $('#success-reference').textContent = result.id;
    const cancelled = result.status === 'cancelled';
    $('#success-title').textContent = cancelled ? 'This request was cancelled.' : isEstimate ? 'Your estimate request is saved.' : 'Your appointment request is saved.';
    $('#success-description').textContent = cancelled ? 'This request already exists and has been cancelled. Call Dylan if you need a new appointment.' : result.status === 'confirmed' ? (isEstimate ? 'Dylan has confirmed your estimate or callback conversation.' : 'Dylan has confirmed your service appointment.') : (isEstimate ? 'Dylan has received your estimate or callback request and your property details. Keep your reference below.' : 'Dylan has received your service request and will review the job details to confirm your appointment. Keep your reference below.');
    const synced = result.calendarStatus === 'synced';
    const sync = $('#success-sync');
    sync.className = `status-badge${synced ? ' synced' : ''}`;
    sync.textContent = cancelled ? (synced ? 'Removed from Google Calendar' : 'Calendar removal pending') : synced ? 'Added to Google Calendar' : result.calendarStatus === 'failed' ? 'Calendar update needs attention' : 'Calendar update pending';
    $('#success-sync-note').textContent = cancelled ? 'This time is no longer an active appointment request.' : synced ? (isEstimate ? 'Your conversation time is reserved in Dylan’s calendar. This does not reserve a service visit.' : 'Your requested service time is reserved in Dylan’s calendar.') : 'Your request is saved. The calendar update is still outstanding; call Dylan if you need to check the time before making plans.';
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
        if (state.receipt === receipt && validReceipt(result) && result.id === receipt.result.id) {
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
      state.attempt = { kind, serviceId: serviceInput.value, start: state.selected.start, name: values.name.trim(), email: values.email.trim(), phone: values.phone.trim(), address: values.address.trim(), notes: values.notes.trim(), idempotencyKey: createKey() };
      if (!state.attempt.name || !state.attempt.phone || !state.attempt.address || !state.attempt.email) { state.attempt = null; notice('Please complete your name, phone, email and property address.'); return; }
    }
    const attempt = state.attempt;
    notice(''); setSubmitting(true);
    try {
      const result = await api('/api/public/bookings', { method: 'POST', body: attempt });
      if (!validReceipt(result)) throw new APIError('Your request may have been saved, but we couldn’t read the result.');
      showSuccess(result, attempt);
      state.receipt = { payload: attempt, result, checks: 0, busy: false };
      scheduleReceiptCheck(2000);
      state.attempt = null;
    } catch (error) {
      const invalidDetails = error.status === 400 && ['invalid_booking', 'invalid_email', 'invalid_phone', 'invalid_start', 'invalid_kind'].includes(error.code);
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
