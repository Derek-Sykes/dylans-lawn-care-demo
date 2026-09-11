'use strict';
(() => {
  // Private credentials are removed before any request and never enter browser storage.
  const entryFragment = new URLSearchParams(window.location.hash.slice(1));
  let invitationToken = entryFragment.get('invite');
  let bootstrapToken = invitationToken ? null : entryFragment.get('setup');
  entryFragment.delete('invite'); entryFragment.delete('setup');
  if (window.location.hash) history.replaceState(null, '', window.location.pathname + window.location.search);
  const oauthOutcome = new URLSearchParams(window.location.search).get('google');
  if (oauthOutcome) { const url = new URL(window.location.href); url.searchParams.delete('google'); history.replaceState(null, '', url.pathname + url.search); }
  const $ = selector => document.querySelector(selector);
  const state = { session: null, sessionEpoch: 0, sessionRequest: 0, refreshingSession: false, csrf: '', settings: null, bookings: [], bookingRevision: 0, activePanel: 'bookings', selectedId: null, dirty: false, saving: false, loadingBookings: false, connecting: false, configuring: false, disconnecting: false, replacingConfiguration: false, poll: null };
  Object.assign(state, { invitations: [], invitationLink: null, pendingRevokeId: null, invitationRevision: 0, loadingInvitations: false, savingInvitation: false });
  Object.assign(state, { members: [], pendingRemoveId: null, memberRevision: 0, loadingMembers: false, removingMember: false });
  state.activeKind = 'service';
  const statusNames = { needs_followup: 'Needs contact', contacted: 'Contacted', confirmed: 'Confirmed', cancelled: 'Cancelled' };
  const kindOf = booking => booking.kind === 'estimate' ? 'estimate' : 'service';
  const services = { 'lawn-care': 'Lawn care', landscaping: 'Landscaping', 'snow-ice': 'Snow & ice' };
  const weekdays = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
  const dialog = $('#booking-dialog');
  function node(tag, className, text) { const element = document.createElement(tag); if (className) element.className = className; if (text !== undefined) element.textContent = text; return element; }
  function message(selector, text, kind = '') { const element = $(selector); element.textContent = text; element.className = `notice${kind ? ` ${kind}` : ''}${selector === '#page-message' ? ' page-message' : ''}`; element.hidden = !text; }
  class APIError extends Error { constructor(text, status = 0, code = '') { super(text); this.status = status; this.code = code; } }
  async function api(path, { method = 'GET', body } = {}) {
    const requestEpoch = state.sessionEpoch;
    const controller = new AbortController(); const timeout = setTimeout(() => controller.abort(), 25000);
    try {
      const headers = { Accept: 'application/json' };
      if (method !== 'GET') { headers['Content-Type'] = 'application/json'; if (state.csrf) headers['X-CSRF-Token'] = state.csrf; }
      const response = await fetch(path, { method, credentials: 'same-origin', cache: 'no-store', headers, signal: controller.signal, ...(body !== undefined ? { body: JSON.stringify(body) } : {}) });
      let data = null;
      if (response.status !== 204) { try { data = await response.json(); } catch { /* Safe error below for an incomplete API response. */ } }
      if (!response.ok) {
        if (response.status === 401 && requestEpoch === state.sessionEpoch && state.session?.authenticated) showSignedOut();
        throw new APIError(typeof data?.error === 'string' ? data.error : response.status === 403 ? 'Your session changed. Refresh the page, then try again.' : 'The request could not be completed. Please try again.', response.status, data?.code);
      }
      return data;
    } catch (error) { if (error instanceof APIError) throw error; throw new APIError('The portal could not reach the booking service. Check your connection and try again.'); }
    finally { clearTimeout(timeout); }
  }
  function validPublicURL(origin) { try { const url = new URL(origin); return ['http:', 'https:'].includes(url.protocol) ? url.href : null; } catch { return null; } }
  function parseGoogleConfiguration(text, size) {
    if (!Number.isFinite(size) || size <= 0 || size > 65536 || typeof text !== 'string' || new TextEncoder().encode(text).length > 65536) throw new Error('Choose a Google configuration JSON file no larger than 64 KB.');
    let data;
    try { data = JSON.parse(text.replace(/^\uFEFF/, '')); } catch { throw new Error('This file is not valid JSON. Choose the private Google configuration file.'); }
    const config = data && typeof data === 'object' && !Array.isArray(data) ? (data.installed ?? data) : null;
    if (!config || typeof config !== 'object' || Array.isArray(config) || typeof config.client_id !== 'string' || typeof config.client_secret !== 'string' || !config.client_id.trim() || !config.client_secret) throw new Error('This file needs a client_id and client_secret in its installed section or at the top level.');
    if (!/^[\x21-\x7E]{8,512}$/.test(config.client_secret)) throw new Error('The private Google configuration contains an invalid client secret. Choose the original configuration file.');
    return { clientId: config.client_id.trim(), clientSecret: config.client_secret };
  }
  function renderConnection() {
    const google = state.session?.google || {};
    const connected = Boolean(google.connected);
    const needsAttention = Boolean(google.error);
    const configurationRequired = Boolean(state.session?.authenticated && google.requiresClientConfiguration);
    const initialSetup = state.session?.role === 'bootstrap';
    const canConnect = Boolean(state.session?.authenticated && state.session.canConnectCalendar && !initialSetup);
    const canManage = Boolean(state.session?.authenticated && state.session.canManageAccess);
    $('#sidebar-dot').classList.toggle('connected', connected && !needsAttention);
    $('#sidebar-connection').textContent = needsAttention ? 'Calendar needs attention' : connected ? 'Calendar connected' : 'Calendar disconnected';
    $('#calendar-badge').textContent = needsAttention ? 'Needs attention' : connected ? 'Connected' : google.configured ? 'Not connected' : 'Setup needed';
    $('#calendar-badge').className = `badge ${connected && !needsAttention ? 'success' : 'warning'}`;
    $('#calendar-title').textContent = needsAttention ? 'The calendar needs attention.' : connected ? 'The booking calendar is connected.' : 'Connect the shared calendar.';
    $('#calendar-description').textContent = connected ? 'Everyone in this workspace uses this booking calendar. Busy times from that account’s primary calendar and booking calendar stay out of the available slots.' : google.configured ? canConnect ? 'Connect your Google Calendar to handle bookings for the whole workspace. This separate permission step makes online times available; signing in or accepting an invitation does not connect a calendar.' : 'The calendar account holder needs to sign in and reconnect Google Calendar before customers can book online. Your portal access stays the same.' : 'Google Calendar setup is not available on this installation yet. You can still manage availability and existing requests here.';
    $('#calendar-email').textContent = google.email ? `Calendar account: ${google.email}` : ''; $('#calendar-email').hidden = !google.email;
    $('#connect-google').hidden = !canConnect || (connected && !needsAttention); $('#connect-google').disabled = !google.configured || configurationRequired || state.connecting || state.configuring || state.disconnecting;
    $('#connect-google .button-label').textContent = connected && needsAttention ? 'Reconnect Google Calendar' : 'Connect Google Calendar';
    const canImport = Boolean(state.session?.authenticated && (canManage || initialSetup) && google.mode === 'desktop' && google.configured);
    const showConfiguration = canImport && (configurationRequired || state.replacingConfiguration);
    const configurationSlot = $(initialSetup ? '#entry-configuration' : '#calendar-configuration-slot');
    if ($('#google-configuration').parentElement !== configurationSlot) configurationSlot.append($('#google-configuration'));
    $('#google-configuration').hidden = !showConfiguration;
    $('#replace-google-configuration').hidden = !canImport || configurationRequired || showConfiguration;
    $('#replace-google-configuration').disabled = state.configuring || state.connecting || state.disconnecting;
    $('#cancel-configuration').hidden = configurationRequired;
    $('#configuration-title').textContent = configurationRequired ? 'Add your private Google configuration.' : 'Replace your private Google configuration.';
    $('#configuration-fields').disabled = state.configuring;
    $('#import-google-configuration').disabled = state.configuring || !$('#google-configuration-file').files.length;
    $('#disconnect-google').hidden = !connected || !canConnect;
    if (initialSetup) $('#signin-google').disabled = configurationRequired || state.configuring || state.connecting;
    if (google.error) message('#calendar-message', String(google.error), 'error'); else message('#calendar-message', '');
    const publicURL = validPublicURL(state.session?.publicOrigin);
    $('#public-site-link').hidden = !publicURL;
    if (publicURL) $('#public-site-link').href = publicURL;
    $('#session-identity').textContent = state.session?.authenticated ? `${state.session.actorEmail || 'Installation access'} · ${state.session.role === 'owner' ? 'Owner' : state.session.role === 'bootstrap' ? 'Setup' : 'Operator'}` : '';
    $('#session-identity').hidden = !state.session?.authenticated;
    $('#access-nav').hidden = !canManage;
    if (!canManage) {
      clearInvitationLink(); state.pendingRevokeId = null; state.invitations = []; state.invitationRevision++; state.loadingInvitations = false; state.savingInvitation = false; $('#invitation-list').replaceChildren();
      $('#invitation-form').reset(); $('#invitation-fields').disabled = false; $('#refresh-invitations').disabled = false; $('#invitation-list').setAttribute('aria-busy', 'false'); $('#create-invitation .button-label').textContent = 'Create invitation'; message('#invitation-message', ''); message('#invitations-message', '');
      resetMembers();
      if (state.activePanel === 'access') switchPanel('bookings');
    }
  }
  function invalidateSessionRefresh() { state.sessionRequest++; state.refreshingSession = false; }
  async function refreshSession() {
    if (!state.session?.authenticated || state.refreshingSession || state.connecting || state.configuring || state.disconnecting) return false;
    const epoch = state.sessionEpoch; const request = ++state.sessionRequest;
    state.refreshingSession = true;
    try {
      const session = await api('/api/admin/session');
      if (epoch !== state.sessionEpoch || request !== state.sessionRequest) return false;
      if (!session || typeof session.authenticated !== 'boolean') throw new APIError('The latest calendar connection could not be checked. Please try again.');
      if (!session.authenticated) { showSignedOut(); return false; }
      state.session = session; state.csrf = session.csrfToken || ''; renderConnection(); return true;
    } catch (error) {
      if (epoch === state.sessionEpoch && request === state.sessionRequest) message('#calendar-message', 'The latest calendar connection could not be checked. Refresh the page or try this panel again.', 'error');
      return false;
    } finally { if (epoch === state.sessionEpoch && request === state.sessionRequest) state.refreshingSession = false; }
  }
  function showSignedOut() {
    clearTimeout(state.poll);
    state.sessionEpoch++;
    invalidateSessionRefresh();
    const previous = state.session;
    state.session = previous ? { authenticated: false, setupRequired: previous.setupRequired, google: { configured: Boolean(previous.google?.configured), connected: Boolean(previous.google?.connected), mode: previous.google?.mode } } : null;
    state.csrf = ''; state.bookings = []; state.settings = null; state.selectedId = null; state.dirty = false; state.saving = false; state.loadingBookings = false; state.connecting = false; state.configuring = false; state.disconnecting = false; state.replacingConfiguration = false;
    state.invitations = []; state.pendingRevokeId = null; state.invitationRevision++; state.loadingInvitations = false; state.savingInvitation = false;
    resetMembers();
    clearInvitationLink(); $('#invitation-list').replaceChildren(); $('#invitation-list').setAttribute('aria-busy', 'false'); $('#invitation-form').reset(); $('#invitation-fields').disabled = false; $('#refresh-invitations').disabled = false;
    $('#create-invitation .button-label').textContent = 'Create invitation';
    $('#session-identity').textContent = ''; $('#session-identity').hidden = true; $('#access-nav').hidden = true; $('#access-panel').hidden = true;
    if (dialog.open) dialog.close();
    $('#booking-detail').replaceChildren(); $('#detail-notes').value = ''; $('#booking-list').replaceChildren();
    $('#booking-dialog-title').textContent = 'Request details';
    $('#booking-search').value = ''; $('#booking-filter').value = 'all'; $('#detail-status').value = 'needs_followup';
    $('#booking-update-fields').disabled = false; $('#detail-status').disabled = false;
    ['#save-booking', '#cancel-booking', '#retry-booking'].forEach(selector => { $(selector).disabled = false; });
    $('#calendar-email').textContent = ''; $('#calendar-email').hidden = true;
    $('#weekly-hours').replaceChildren(); $('#date-exceptions').replaceChildren(); $('#time-off-rows').replaceChildren(); $('#time-off-panel').open = false; updateTimeOffSummary();
    $('#settings-form').reset(); $('#settings-fields').disabled = true; $('#save-settings').disabled = true; $('#reset-settings').disabled = true;
    $('#google-configuration-form').reset(); $('#google-configuration-file').value = ''; $('#google-configuration').hidden = true;
    $('#import-google-configuration').disabled = true; $('#import-google-configuration .button-label').textContent = 'Import configuration';
    $('#logout').disabled = false; $('#signin-google').disabled = false; $('#disconnect-google').disabled = false; $('#refresh-bookings').disabled = false;
    $('#settings-state').textContent = ''; $('#request-count').textContent = '0'; $('#estimate-count').textContent = '0';
    ['#stat-followup', '#stat-confirmed', '#stat-calendar'].forEach(selector => { $(selector).textContent = '—'; });
    ['#booking-detail-message', '#calendar-refresh-message', '#bookings-message', '#settings-message', '#calendar-message', '#configuration-message', '#invitation-message', '#invitations-message', '#page-message'].forEach(selector => message(selector, ''));
    $('#portal').hidden = true; $('#logout').hidden = true; $('#loading-view').hidden = true; $('#signin-view').hidden = false;
    const signInAvailable = Boolean(state.session?.google?.configured);
    $('#signin-google').hidden = !signInAvailable;
    $('#signin-copy').textContent = invitationToken ? 'You have a private workspace invitation. Continue with the Google account this invitation was sent to. You will share the existing bookings and availability.' : signInAvailable ? 'Sign in with your approved Google account to manage bookings and availability. Signing in does not change the connected calendar.' : 'Google sign-in is not ready on this installation. Ask the person who set up the portal to finish its private setup.';
    $('#signin-google .button-label').textContent = invitationToken ? 'Accept invitation with Google' : 'Sign in with Google';
    switchPanel('bookings', false, 'service');
  }
  function switchPanel(panel, focus = false, kind = state.activeKind) {
    if (panel === 'access' && !state.session?.canManageAccess) return;
    if (state.activePanel === 'access' && panel !== 'access') { clearInvitationLink(); state.pendingRevokeId = null; state.pendingRemoveId = null; renderInvitations(); renderMembers(); message('#invitation-message', ''); message('#members-message', ''); }
    state.activePanel = panel;
    if (panel === 'bookings') state.activeKind = kind === 'estimate' ? 'estimate' : 'service';
    document.querySelectorAll('.workspace-panel').forEach(element => { element.hidden = element.id !== `${panel}-panel`; });
    document.querySelectorAll('[data-panel]').forEach(button => { const selected = button.dataset.panel === panel && (panel !== 'bookings' || button.dataset.kind === state.activeKind); button.classList.toggle('active', selected); if (selected) button.setAttribute('aria-current', 'page'); else button.removeAttribute('aria-current'); });
    renderBookings();
    if (focus) $(`#${panel}-heading`).focus();
    if (panel === 'calendar') refreshSession();
    if (panel === 'bookings') loadBookings(true);
    if (panel === 'access') { refreshSession(); loadInvitations(); loadMembers(); }
  }
  function formatDate(value, timeOnly = false) {
    const date = new Date(value);
    if (!Number.isFinite(date.getTime())) return 'Time unavailable';
    const options = timeOnly ? { hour: 'numeric', minute: '2-digit' } : { weekday: 'short', month: 'short', day: 'numeric', year: 'numeric', hour: 'numeric', minute: '2-digit' };
    try { return new Intl.DateTimeFormat('en-US', { ...options, timeZone: state.settings?.timeZone || 'America/New_York', timeZoneName: 'short' }).format(date); }
    catch { return new Intl.DateTimeFormat('en-US', options).format(date); }
  }
  function appointmentDuration(booking) {
    const minutes = Math.round((Date.parse(booking.end) - Date.parse(booking.start)) / 60000);
    if (!Number.isFinite(minutes) || minutes <= 0) return 'Duration unavailable';
    const hours = Math.floor(minutes / 60); const remaining = minutes % 60;
    return [hours ? `${hours} ${hours === 1 ? 'hour' : 'hours'}` : '', remaining ? `${remaining} ${remaining === 1 ? 'minute' : 'minutes'}` : ''].filter(Boolean).join(' ');
  }
  function appointmentEnd(booking) {
    try {
      const day = new Intl.DateTimeFormat('en-US', { timeZone: state.settings?.timeZone || 'America/New_York', year: 'numeric', month: 'numeric', day: 'numeric' });
      return formatDate(booking.end, day.format(new Date(booking.start)) === day.format(new Date(booking.end)));
    } catch { return formatDate(booking.end); }
  }
  function syncText(booking) {
    if (booking.status === 'cancelled') return booking.calendarStatus === 'synced' ? 'Calendar removal complete' : booking.calendarStatus === 'failed' ? 'Calendar removal failed' : 'Calendar removal pending';
    return booking.calendarStatus === 'synced' ? 'On Google Calendar' : booking.calendarStatus === 'failed' ? 'Calendar update failed' : 'Calendar update pending';
  }
  function badge(text, kind = '') { return node('span', `badge ${kind}`, text); }
  function renderBookings() {
    const all = state.bookings.filter(item => kindOf(item) === state.activeKind);
    $('#bookings-heading').replaceChildren(document.createTextNode(state.activeKind === 'estimate' ? 'Estimates & callbacks' : 'Appointments'), node('span', '', '.'));
    $('#bookings-intro').textContent = state.activeKind === 'estimate' ? 'Estimate and callback conversations, with the property details and contact progress together.' : 'Lawn, landscape and snow jobs, with their scheduled times and details.';
    $('#stat-followup').textContent = all.filter(item => item.status === 'needs_followup').length;
    $('#stat-confirmed').textContent = all.filter(item => item.status === 'confirmed').length;
    $('#stat-calendar').textContent = all.filter(item => item.calendarStatus !== 'synced').length;
    $('#request-count').textContent = state.bookings.filter(item => kindOf(item) === 'service' && item.status !== 'cancelled').length;
    $('#estimate-count').textContent = state.bookings.filter(item => kindOf(item) === 'estimate' && item.status !== 'cancelled').length;
    const filter = $('#booking-filter').value; const query = $('#booking-search').value.trim().toLocaleLowerCase();
    const filtered = all.filter(item => (filter === 'all' || item.status === filter) && (!query || [item.name, item.email, item.phone].some(value => String(value || '').toLocaleLowerCase().includes(query)))).sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt));
    const fragment = document.createDocumentFragment();
    if (!filtered.length) fragment.append(node('p', 'empty-state', all.length ? 'No requests match these filters.' : state.activeKind === 'estimate' ? 'No estimates or callbacks yet. New requests will appear here.' : 'No service appointments yet. New job requests will appear here.'));
    filtered.forEach(booking => {
      const row = node('article', 'request-row'); const details = node('div');
      details.append(node('h3', '', booking.name || 'Customer request'), node('p', '', services[booking.serviceId] || 'Service appointment'));
      const badges = node('div', 'request-meta'); badges.append(badge(statusNames[booking.status] || booking.status, booking.status === 'confirmed' ? 'success' : booking.status === 'needs_followup' ? 'warning' : 'neutral'), badge(syncText(booking), booking.calendarStatus === 'failed' ? 'danger' : booking.calendarStatus === 'synced' ? 'success' : 'warning')); details.append(badges);
      const when = node('div', 'request-time'); when.append(node('p', '', formatDate(booking.start)), node('p', '', `Until ${appointmentEnd(booking)} · ${appointmentDuration(booking)}`), node('p', '', booking.phone || booking.email || ''));
      const open = node('button', 'button button-secondary', 'View'); open.type = 'button'; open.setAttribute('aria-label', `View request from ${booking.name || 'customer'}`); open.addEventListener('click', () => openBooking(booking.id));
      row.append(details, when, open); fragment.append(row);
    });
    $('#booking-list').replaceChildren(fragment);
  }
  async function loadBookings(silent = false, mutationRefresh = false) {
    if (state.loadingBookings || (state.saving && !mutationRefresh) || !state.session?.authenticated) return false;
    const epoch = state.sessionEpoch; const revision = state.bookingRevision;
    state.loadingBookings = true; $('#refresh-bookings').disabled = true;
    if (!silent) message('#bookings-message', 'Loading requests…');
    try {
      const data = await api('/api/admin/bookings');
      if (epoch !== state.sessionEpoch || revision !== state.bookingRevision || (state.saving && !mutationRefresh) || !state.session?.authenticated) return false;
      if (!Array.isArray(data?.bookings)) throw new APIError('The request list could not be read. Please refresh.');
      const previous = state.bookings.find(booking => booking.id === state.selectedId);
      state.bookings = data.bookings;
      if (!silent || !$('#booking-list').contains(document.activeElement)) renderBookings();
      const selected = state.bookings.find(booking => booking.id === state.selectedId);
      if (dialog.open && selected) renderDetail(selected, previous);
      message('#bookings-message', data.calendarSyncError ? 'Google Calendar changes could not be checked. These are the last saved appointment times. Refresh again before relying on the schedule.' : '', data.calendarSyncError ? 'error' : '');
      message('#calendar-refresh-message', data.calendarSyncError ? 'Google Calendar changes could not be checked. The appointment times shown are the last saved times.' : '', data.calendarSyncError ? 'error' : '');
      return true;
    } catch (error) { if (epoch === state.sessionEpoch) message('#bookings-message', error.message, 'error'); return false; }
    finally { if (epoch === state.sessionEpoch) { state.loadingBookings = false; $('#refresh-bookings').disabled = false; } }
  }
  function detailItem(label, value, className = '', href = null) {
    const wrapper = node('div', className); const definition = node('dd');
    if (href && value) { const link = node('a', '', value); link.href = href; definition.append(link); } else definition.textContent = value || 'Not provided';
    wrapper.append(node('dt', '', label), definition); return wrapper;
  }
  function renderDetail(booking, previous = null) {
    const notesDraft = previous && $('#detail-notes').value !== (previous.adminNotes || '') ? $('#detail-notes').value : null;
    const statusDraft = previous && $('#detail-status').value !== previous.status ? $('#detail-status').value : null;
    $('#booking-dialog-title').textContent = booking.name || 'Customer request';
    const time = node('div', 'detail-time'); time.append(node('strong', '', formatDate(booking.start)), node('p', '', `Until ${appointmentEnd(booking)} · ${appointmentDuration(booking)} · ${services[booking.serviceId] || 'Service appointment'}`));
    const sync = node('div', 'request-meta'); sync.append(badge(kindOf(booking) === 'estimate' ? 'Estimate / callback' : 'Service appointment', 'neutral'), badge(statusNames[booking.status] || booking.status, 'neutral'), badge(syncText(booking), booking.calendarStatus === 'failed' ? 'danger' : booking.calendarStatus === 'synced' ? 'success' : 'warning'));
    const explanation = booking.status === 'cancelled' ? booking.calendarStatus === 'synced' ? 'This request is cancelled. To schedule again, create a new request using current availability.' : 'Cancellation is saved. This time stays reserved until removal from Google Calendar succeeds.' : booking.calendarStatus === 'synced' ? kindOf(booking) === 'estimate' ? 'This time is reserved for an estimate or callback. Track whether the customer needs contact, has been contacted, or the conversation is confirmed.' : 'The service appointment is on Google Calendar. Track contact progress and confirm the job once its details are settled.' : 'The request is saved. Its Google Calendar update is not yet complete. Review the details before confirming the appointment.';
    const data = node('dl', 'detail-grid');
    const phone = String(booking.phone || '').replace(/[^\d+]/g, '');
    data.append(detailItem('Phone', booking.phone, '', phone ? `tel:${phone}` : null), detailItem('Email', booking.email, '', booking.email ? `mailto:${encodeURIComponent(booking.email)}` : null), detailItem('Property address', booking.address, 'wide'), detailItem('Customer notes', booking.notes || 'No notes supplied.', 'wide'), detailItem('Request reference', booking.id, 'wide'));
    $('#booking-detail').replaceChildren(time, sync, node('p', 'sync-description', explanation), data);
    if (!previous || statusDraft === null || booking.status === 'cancelled') $('#detail-status').value = booking.status;
    $('#detail-status').disabled = booking.status === 'cancelled';
    $('#detail-status').querySelector('option[value="confirmed"]').disabled = booking.calendarStatus !== 'synced';
    if (notesDraft === null && $('#detail-notes').value !== (booking.adminNotes || '')) $('#detail-notes').value = booking.adminNotes || '';
    $('#cancel-booking').hidden = booking.status === 'cancelled';
    $('#retry-booking').hidden = booking.calendarStatus === 'synced';
  }
  function openBooking(id) { const booking = state.bookings.find(item => item.id === id); if (!booking) return; state.selectedId = id; renderDetail(booking); message('#booking-detail-message', ''); dialog.showModal(); $('#close-booking').focus(); }
  function lockDetail(locked) { state.saving = locked; $('#booking-update-fields').disabled = locked; ['#save-booking', '#cancel-booking', '#retry-booking'].forEach(selector => { $(selector).disabled = locked; }); }
  async function mutateBooking(method, suffix, payload, successMessage) {
    if (state.saving || !state.selectedId) return;
    state.bookingRevision++;
    const id = state.selectedId; const epoch = state.sessionEpoch; lockDetail(true); message('#booking-detail-message', 'Saving…');
    try {
      const data = await api(`/api/admin/bookings/${encodeURIComponent(id)}${suffix}`, { method, body: payload || {} });
      if (epoch !== state.sessionEpoch || !state.session?.authenticated) return;
      const updated = data?.booking || data;
      if (updated?.id === id) { state.bookings = state.bookings.map(item => item.id === id ? updated : item); renderBookings(); if (dialog.open && state.selectedId === id) renderDetail(updated); }
      else { const loaded = await loadBookings(true, true); const booking = state.bookings.find(item => item.id === id); if (loaded && dialog.open && state.selectedId === id && booking) renderDetail(booking); if (!loaded) successMessage += ' Refresh requests to check the latest calendar status.'; }
      if (epoch === state.sessionEpoch && dialog.open && state.selectedId === id) message('#booking-detail-message', successMessage, 'success');
    } catch (error) { if (epoch === state.sessionEpoch) message('#booking-detail-message', error.message, 'error'); }
    finally { if (epoch === state.sessionEpoch) { state.bookingRevision++; lockDetail(false); const booking = state.bookings.find(item => item.id === state.selectedId); $('#detail-status').disabled = booking?.status === 'cancelled'; } }
  }
  function dirty() { state.dirty = true; $('#settings-state').textContent = 'Unsaved changes'; message('#settings-message', ''); }
  function timeInput(labelText, value) { const label = node('label'); const caption = node('span', 'small muted', labelText); const input = node('input'); input.type = 'time'; input.value = value; input.required = true; label.append(caption, input); return label; }
  function renderWeekly(weekly) {
    const fragment = document.createDocumentFragment();
    weekdays.forEach((day, weekday) => {
      const entries = weekly.filter(item => item.weekday === weekday); const row = node('div', 'weekly-day'); row.dataset.weekday = weekday;
      const toggleLabel = node('label', 'day-toggle'); const toggle = node('input'); toggle.type = 'checkbox'; toggle.checked = entries.length > 0; toggle.className = 'day-enabled'; toggleLabel.append(toggle, document.createTextNode(day));
      const slots = node('div', 'day-slots'); const closed = node('span', 'day-closed', 'Unavailable');
      function syncDay() { slots.hidden = !toggle.checked; closed.hidden = toggle.checked; slots.querySelectorAll('input').forEach(input => { input.disabled = !toggle.checked; }); }
      function appendWindow(start = '09:00', end = '17:00') {
        const window = node('div', 'time-window'); const startLabel = timeInput(`${day} from`, start); const endLabel = timeInput('Until', end); const remove = node('button', 'icon-button', '×'); remove.type = 'button'; remove.setAttribute('aria-label', `Remove ${day} time window`);
        remove.addEventListener('click', () => { window.remove(); if (!slots.children.length) toggle.checked = false; syncDay(); dirty(); });
        window.append(startLabel, node('span', '', '–'), endLabel, remove); slots.append(window);
      }
      entries.forEach(entry => appendWindow(entry.start, entry.end));
      const add = node('button', 'add-window', '+ Add hours'); add.type = 'button'; add.setAttribute('aria-label', `Add hours for ${day}`); add.addEventListener('click', () => { toggle.checked = true; appendWindow(); syncDay(); dirty(); slots.lastElementChild.querySelector('input').focus(); });
      toggle.addEventListener('change', () => { if (toggle.checked && !slots.children.length) appendWindow(); syncDay(); });
      const content = node('div'); content.append(slots, closed); row.append(toggleLabel, content, add); fragment.append(row); syncDay();
    });
    $('#weekly-hours').replaceChildren(fragment);
  }
  function appendException(exception = {}) {
    const row = node('div', 'exception-row');
    const dateLabel = node('label', '', 'Date'); const date = node('input'); date.type = 'date'; date.required = true; date.value = exception.date || ''; date.className = 'exception-date'; dateLabel.append(date);
    const typeLabel = node('label', '', 'Availability'); const type = node('select'); type.className = 'exception-type'; type.add(new Option('Closed', 'closed')); type.add(new Option('Custom hours', 'hours')); type.value = exception.closed === false ? 'hours' : 'closed'; typeLabel.append(type);
    const hours = node('div', 'exception-hours'); hours.append(timeInput('From', exception.start || '09:00'), timeInput('Until', exception.end || '17:00'));
    const remove = node('button', 'icon-button', '×'); remove.type = 'button'; remove.setAttribute('aria-label', 'Remove date exception');
    function sync() { hours.hidden = type.value === 'closed'; hours.querySelectorAll('input').forEach(input => { input.disabled = hours.hidden; }); }
    type.addEventListener('change', sync);
    remove.addEventListener('click', () => { row.remove(); $('#exceptions-empty').hidden = Boolean($('#date-exceptions').children.length); dirty(); });
    row.append(dateLabel, typeLabel, hours, remove); $('#date-exceptions').append(row); $('#exceptions-empty').hidden = true; sync(); return date;
  }
  function updateTimeOffSummary() {
    const count = $('#time-off-rows').children.length;
    $('#time-off-empty').hidden = count > 0;
    $('#time-off-summary').textContent = count ? `${count} ${count === 1 ? 'time-off block' : 'time-off blocks'}` : 'Optional · block a day or part of a day';
  }
  function appendTimeOff(block = {}, kind = 'weekly') {
    const row = node('div', 'time-off-row');
    const repeatLabel = node('label', '', 'Applies'); const repeat = node('select', 'time-off-repeat'); repeat.add(new Option('Repeats weekly', 'weekly')); repeat.add(new Option('Specific date', 'date')); repeat.value = kind; repeatLabel.append(repeat);
    const weekdayLabel = node('label', 'time-off-weekday-label', 'Day of the week'); const weekday = node('select', 'time-off-weekday'); weekdays.forEach((day, index) => weekday.add(new Option(day, String(index)))); weekday.value = String(Number.isInteger(block.weekday) ? block.weekday : 1); weekdayLabel.append(weekday);
    const dateLabel = node('label', 'time-off-date-label', 'Date'); const date = node('input', 'time-off-date'); date.type = 'date'; date.required = true; date.value = block.date || ''; dateLabel.append(date);
    const durationLabel = node('label', 'time-off-duration-label', 'Block'); const duration = node('select', 'time-off-duration'); duration.add(new Option('Whole day', 'day')); duration.add(new Option('Time window', 'hours')); duration.value = block.allDay === false ? 'hours' : 'day'; durationLabel.append(duration);
    const hours = node('div', 'time-off-hours'); hours.append(timeInput('From', block.start || '12:00'), timeInput('Until', block.end || '13:00'));
    const remove = node('button', 'icon-button', '×'); remove.type = 'button'; remove.setAttribute('aria-label', 'Remove time off');
    function sync() {
      const weekly = repeat.value === 'weekly';
      weekdayLabel.hidden = !weekly; weekday.disabled = !weekly;
      dateLabel.hidden = weekly; date.disabled = weekly;
      hours.hidden = duration.value === 'day'; hours.querySelectorAll('input').forEach(input => { input.disabled = hours.hidden; });
    }
    repeat.addEventListener('change', sync); duration.addEventListener('change', sync);
    remove.addEventListener('click', () => { row.remove(); updateTimeOffSummary(); dirty(); });
    row.append(repeatLabel, weekdayLabel, dateLabel, durationLabel, hours, remove); $('#time-off-rows').append(row); sync(); updateTimeOffSummary(); return repeat;
  }
  function timeOffSettings(blocks) {
    const result = { blockedWeekly: [], blockedDates: [] };
    const clockPattern = /^(?:[01]\d|2[0-3]):[0-5]\d$/;
    blocks.forEach(block => {
      const value = { allDay: block.allDay === true };
      if (block.kind === 'weekly') {
        value.weekday = Number(block.weekday);
        if (!Number.isInteger(value.weekday) || value.weekday < 0 || value.weekday > 6) throw new Error('Choose a day of the week for each recurring time-off block.');
      } else if (block.kind === 'date') {
        const date = new Date(`${block.date}T12:00:00Z`);
        if (!/^\d{4}-\d{2}-\d{2}$/.test(block.date) || !Number.isFinite(date.getTime()) || date.toISOString().slice(0, 10) !== block.date) throw new Error('Choose a valid date for each specific time-off block.');
        value.date = block.date;
      } else throw new Error('Choose whether time off repeats weekly or applies to a specific date.');
      if (!value.allDay) {
        if (!clockPattern.test(block.start) || !clockPattern.test(block.end) || block.start >= block.end) throw new Error('Each time-off window needs an end time later than its start on the same day.');
        value.start = block.start; value.end = block.end;
      }
      result[block.kind === 'weekly' ? 'blockedWeekly' : 'blockedDates'].push(value);
    });
    if (result.blockedWeekly.length > 70 || result.blockedDates.length > 365) throw new Error('Use up to 70 recurring time-off blocks and 365 blocks for specific dates.');
    return result;
  }
  function renderSettings(settings) {
    state.settings = { ...settings, externalBufferMinutes: settings.externalBufferMinutes ?? 30, blockedWeekly: Array.isArray(settings.blockedWeekly) ? settings.blockedWeekly : [], blockedDates: Array.isArray(settings.blockedDates) ? settings.blockedDates : [] };
    for (const key of ['businessName', 'timeZone', 'slotMinutes', 'estimateMinutes', 'bufferMinutes', 'externalBufferMinutes', 'minNoticeHours', 'horizonDays']) $('#settings-form').elements.namedItem(key).value = state.settings[key];
    renderWeekly(settings.weekly || []); $('#date-exceptions').replaceChildren(); (settings.exceptions || []).forEach(appendException); $('#exceptions-empty').hidden = Boolean((settings.exceptions || []).length);
    $('#time-off-rows').replaceChildren(); state.settings.blockedWeekly.forEach(block => appendTimeOff(block, 'weekly')); state.settings.blockedDates.forEach(block => appendTimeOff(block, 'date')); updateTimeOffSummary(); $('#time-off-panel').open = $('#time-off-rows').children.length > 0;
    state.dirty = false; $('#settings-fields').disabled = false; $('#save-settings').disabled = false; $('#reset-settings').disabled = false; $('#reset-settings').textContent = 'Discard changes'; $('#settings-state').textContent = 'All changes saved'; renderBookings();
  }
  async function loadSettings() {
    const epoch = state.sessionEpoch;
    try { const settings = await api('/api/admin/settings'); if (epoch !== state.sessionEpoch || !state.session?.authenticated) return; if (!settings?.timeZone) throw new APIError('Availability could not be read. Please try again.'); renderSettings(settings); message('#settings-message', ''); }
    catch (error) { if (epoch === state.sessionEpoch) { message('#settings-message', error.message, 'error'); $('#settings-state').textContent = 'Availability could not be loaded'; $('#reset-settings').disabled = false; $('#reset-settings').textContent = 'Reload availability'; } }
  }
  function readSettings() {
    const values = new FormData($('#settings-form')); const settings = { businessName: values.get('businessName').trim(), timeZone: values.get('timeZone').trim(), weekly: [], exceptions: [] };
    if (!settings.businessName) throw new Error('Enter your business name.');
    try { new Intl.DateTimeFormat('en-US', { timeZone: settings.timeZone }).format(); } catch { throw new Error('Enter a valid time zone, such as America/New_York.'); }
    for (const key of ['slotMinutes', 'estimateMinutes', 'bufferMinutes', 'externalBufferMinutes', 'minNoticeHours', 'horizonDays']) settings[key] = Number(values.get(key));
    document.querySelectorAll('.weekly-day').forEach(row => {
      if (!row.querySelector('.day-enabled').checked) return;
      const weekday = Number(row.dataset.weekday); const windows = [];
      row.querySelectorAll('.time-window').forEach(window => { const [start, end] = [...window.querySelectorAll('input')].map(input => input.value); if (!start || !end || start >= end) throw new Error(`${weekdays[weekday]} needs an end time later than its start time.`); windows.push({ weekday, start, end }); });
      windows.sort((a, b) => a.start.localeCompare(b.start));
      windows.forEach((window, i) => { if (i && windows[i - 1].end > window.start) throw new Error(`${weekdays[weekday]} has overlapping time windows.`); });
      settings.weekly.push(...windows);
    });
    const dates = new Set();
    document.querySelectorAll('.exception-row').forEach(row => {
      const date = row.querySelector('.exception-date').value;
      if (!date || dates.has(date)) throw new Error('Each date exception needs a different date.'); dates.add(date);
      const exception = { date, closed: row.querySelector('.exception-type').value === 'closed' };
      if (!exception.closed) { [exception.start, exception.end] = [...row.querySelectorAll('.exception-hours input')].map(input => input.value); if (!exception.start || !exception.end || exception.start >= exception.end) throw new Error(`The hours for ${date} need an end time later than the start time.`); }
      settings.exceptions.push(exception);
    });
    const blocks = [...document.querySelectorAll('.time-off-row')].map(row => {
      const [start, end] = [...row.querySelectorAll('.time-off-hours input')].map(input => input.value);
      return { kind: row.querySelector('.time-off-repeat').value, weekday: row.querySelector('.time-off-weekday').value, date: row.querySelector('.time-off-date').value, allDay: row.querySelector('.time-off-duration').value === 'day', start, end };
    });
    Object.assign(settings, timeOffSettings(blocks));
    return settings;
  }
  function clearInvitationLink() {
    state.invitationLink = null; $('#invitation-link').value = ''; $('#invitation-result-description').textContent = ''; $('#invitation-result').hidden = true;
  }
  function resetMembers() {
    state.members = []; state.pendingRemoveId = null; state.memberRevision++; state.loadingMembers = false; state.removingMember = false;
    $('#member-list').replaceChildren(); $('#member-list').setAttribute('aria-busy', 'false'); $('#refresh-members').disabled = false; message('#members-message', '');
  }
  function renderMembers(focusId = '', focusAction = '') {
    const fragment = document.createDocumentFragment(); let focusTarget = null;
    if (!focusId && document.activeElement?.dataset?.memberId) { focusId = document.activeElement.dataset.memberId; focusAction = document.activeElement.dataset.memberAction; }
    if (!state.members.length) fragment.append(node('p', 'empty-state', 'No people could be listed. Refresh to check workspace access.'));
    state.members.forEach(member => {
      const row = node('article', 'member-row'); const details = node('div', 'member-details');
      const heading = node('h3', '', member.email); heading.tabIndex = -1;
      heading.dataset.memberId = member.id; heading.dataset.memberAction = 'heading';
      if (member.id === focusId && focusAction === 'heading') focusTarget = heading;
      const labels = node('div', 'member-labels');
      labels.append(badge(member.role === 'operator' ? 'Operator' : 'Owner', 'neutral'));
      if (member.calendarOwner) labels.append(badge('Calendar account', 'success'));
      if (member.email.toLowerCase() === state.session?.actorEmail?.toLowerCase()) labels.append(badge('You', 'neutral'));
      details.append(heading, labels);
      if (!member.canRemove) details.append(node('p', 'small muted', member.calendarOwner ? 'Keeps the shared booking calendar connected.' : 'Manages invitations and workspace access.'));
      row.append(details);
      function action(label, className, actionName, handler) {
        const button = node('button', className, label); button.type = 'button'; button.disabled = state.removingMember;
        button.dataset.memberId = member.id; button.dataset.memberAction = actionName; button.addEventListener('click', handler);
        if (member.id === focusId && actionName === focusAction) focusTarget = button;
        return button;
      }
      if (member.canRemove && state.session?.canManageAccess) {
        if (state.pendingRemoveId === member.id) {
          const confirmation = node('div', 'member-confirmation'); confirmation.setAttribute('role', 'group'); confirmation.setAttribute('aria-label', `Remove access for ${member.email}`);
          const actions = node('div', 'invitation-confirm-actions');
          actions.append(action(state.removingMember ? 'Removing…' : 'Confirm removal', 'button button-danger', 'confirm', () => removeMember(member)), action('Keep access', 'button button-secondary', 'keep', () => { if (state.removingMember) return; state.pendingRemoveId = null; renderMembers(member.id, 'remove'); }));
          confirmation.append(node('p', 'small muted', 'This person will be signed out and lose portal access. Shared appointments, availability and the connected calendar stay in place.'), actions); row.append(confirmation);
        } else {
          const remove = action('Remove access', 'button button-quiet', 'remove', () => { if (state.removingMember || !state.session?.canManageAccess) return; state.pendingRemoveId = member.id; message('#members-message', ''); renderMembers(member.id, 'confirm'); });
          remove.setAttribute('aria-label', `Remove access for ${member.email}`); row.append(remove);
        }
      } else if (member.id === focusId) focusTarget = heading;
      if (member.id === focusId && focusTarget?.disabled) focusTarget = heading;
      fragment.append(row);
    });
    $('#member-list').replaceChildren(fragment);
    if (state.activePanel === 'access' && focusTarget && !focusTarget.disabled) focusTarget.focus();
  }
  async function loadMembers() {
    if (!state.session?.authenticated || !state.session.canManageAccess || state.loadingMembers || state.removingMember) return false;
    const epoch = state.sessionEpoch; const revision = state.memberRevision;
    state.loadingMembers = true; $('#refresh-members').disabled = true;
    try {
      const data = await api('/api/admin/members');
      if (epoch !== state.sessionEpoch || revision !== state.memberRevision || !state.session?.canManageAccess) return false;
      if (!Array.isArray(data?.members) || data.members.some(item => !item || typeof item.id !== 'string' || !item.id || typeof item.email !== 'string' || !['owner', 'operator'].includes(item.role) || typeof item.calendarOwner !== 'boolean' || typeof item.canRemove !== 'boolean')) throw new APIError('People with access could not be read. Please refresh.');
      state.members = data.members;
      if (state.pendingRemoveId && !state.members.some(item => item.id === state.pendingRemoveId && item.canRemove)) state.pendingRemoveId = null;
      renderMembers(); message('#members-message', ''); return true;
    } catch (error) { if (epoch === state.sessionEpoch && revision === state.memberRevision && state.session?.canManageAccess) message('#members-message', error.message, 'error'); return false; }
    finally { if (epoch === state.sessionEpoch && revision === state.memberRevision && state.session?.canManageAccess) { state.loadingMembers = false; $('#refresh-members').disabled = false; } }
  }
  async function removeMember(member) {
    if (!state.session?.authenticated || !state.session.canManageAccess || state.removingMember || state.pendingRemoveId !== member.id || !state.members.some(item => item.id === member.id && item.canRemove)) return;
    const epoch = state.sessionEpoch; const revision = ++state.memberRevision;
    state.loadingMembers = false; state.removingMember = true; $('#refresh-members').disabled = true; $('#member-list').setAttribute('aria-busy', 'true'); renderMembers(); message('#members-message', '');
    try {
      await api(`/api/admin/members/${encodeURIComponent(member.id)}`, { method: 'DELETE' });
      if (epoch !== state.sessionEpoch || revision !== state.memberRevision || !state.session?.canManageAccess) return;
      state.members = state.members.filter(item => item.id !== member.id); state.pendingRemoveId = null;
      message('#members-message', `${member.email} no longer has portal access. Shared appointments and the calendar are unchanged.`, 'success');
    } catch (error) { if (epoch === state.sessionEpoch && revision === state.memberRevision && state.session?.canManageAccess) message('#members-message', error.message, 'error'); }
    finally {
      if (epoch === state.sessionEpoch && revision === state.memberRevision && state.session?.canManageAccess) {
        const restoreFocus = state.activePanel === 'access' && document.activeElement?.dataset?.memberId === member.id;
        state.removingMember = false; $('#refresh-members').disabled = false; $('#member-list').setAttribute('aria-busy', 'false'); renderMembers();
        if (restoreFocus) { if (state.members.some(item => item.id === member.id)) renderMembers(member.id, 'confirm'); else $('#members-heading').focus(); }
      }
    }
  }
  function invitationTime(value) {
    return Number.isFinite(Date.parse(value)) ? new Intl.DateTimeFormat('en-US', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) : 'Expiry unavailable';
  }
  function renderInvitations(focusId = '', focusAction = '') {
    const fragment = document.createDocumentFragment();
    let focusTarget = null;
    if (!focusId && document.activeElement?.dataset?.invitationId) { focusId = document.activeElement.dataset.invitationId; focusAction = document.activeElement.dataset.invitationAction; }
    if (!state.invitations.length) fragment.append(node('p', 'empty-state', 'No invitations yet. Create a private link to welcome a co-owner.'));
    state.invitations.forEach(invitation => {
      const row = node('article', 'invitation-row'); const details = node('div');
      const status = invitation.status === 'pending' && Date.parse(invitation.expiresAt) <= Date.now() ? 'expired' : invitation.status;
      const heading = node('h3', '', invitation.email); heading.tabIndex = -1;
      details.append(heading, node('p', 'small muted', `${status === 'pending' ? 'Expires' : 'Expiry'} ${invitationTime(invitation.expiresAt)} · your local time`));
      row.append(details, badge(({ pending: 'Awaiting acceptance', used: 'Accepted', revoked: 'Revoked', expired: 'Expired' })[status] || 'Status unavailable', status === 'used' ? 'success' : status === 'pending' ? 'warning' : 'neutral'));
      function action(label, className, actionName, handler) {
        const button = node('button', className, label); button.type = 'button'; button.disabled = state.savingInvitation;
        button.dataset.invitationId = invitation.id; button.dataset.invitationAction = actionName;
        button.addEventListener('click', handler);
        if (invitation.id === focusId && actionName === focusAction) focusTarget = button;
        return button;
      }
      if (status === 'pending') {
        if (state.pendingRevokeId === invitation.id) {
          const confirmation = node('div', 'invitation-confirmation'); confirmation.setAttribute('role', 'group'); confirmation.setAttribute('aria-label', `Revoke invitation for ${invitation.email}`);
          const actions = node('div', 'invitation-confirm-actions');
          actions.append(action(state.savingInvitation ? 'Revoking…' : 'Confirm revocation', 'button button-danger', 'confirm', () => revokeInvitation(invitation)), action('Keep invitation', 'button button-secondary', 'keep', () => { if (state.savingInvitation) return; state.pendingRevokeId = null; renderInvitations(invitation.id, 'revoke'); }));
          confirmation.append(node('p', 'small muted', 'This private link will stop working.'), actions); row.append(confirmation);
        } else {
          const revoke = action('Revoke', 'button button-quiet', 'revoke', () => { if (state.savingInvitation || !state.session?.canManageAccess) return; state.pendingRevokeId = invitation.id; message('#invitation-message', ''); renderInvitations(invitation.id, 'confirm'); });
          revoke.setAttribute('aria-label', `Revoke invitation for ${invitation.email}`); row.append(revoke);
        }
      } else if (invitation.id === focusId) {
        focusTarget = heading;
      }
      fragment.append(row);
    });
    $('#invitation-list').replaceChildren(fragment);
    if (state.activePanel === 'access' && focusTarget && !focusTarget.disabled) focusTarget.focus();
  }
  async function loadInvitations() {
    if (!state.session?.authenticated || !state.session.canManageAccess || state.loadingInvitations || state.savingInvitation) return false;
    const epoch = state.sessionEpoch; const revision = state.invitationRevision;
    state.loadingInvitations = true; $('#refresh-invitations').disabled = true;
    try {
      const data = await api('/api/admin/invitations');
      if (epoch !== state.sessionEpoch || revision !== state.invitationRevision || !state.session?.canManageAccess) return false;
      if (!Array.isArray(data?.invitations) || data.invitations.some(item => !item || typeof item.id !== 'string' || typeof item.email !== 'string' || !['pending', 'used', 'revoked', 'expired'].includes(item.status))) throw new APIError('Invitations could not be read. Please refresh.');
      state.invitations = data.invitations;
      if (state.invitationLink && !state.invitations.some(item => item.id === state.invitationLink.id && item.status === 'pending' && Date.parse(item.expiresAt) > Date.now())) clearInvitationLink();
      if (state.pendingRevokeId && !state.invitations.some(item => item.id === state.pendingRevokeId && item.status === 'pending' && Date.parse(item.expiresAt) > Date.now())) state.pendingRevokeId = null;
      renderInvitations(); message('#invitations-message', ''); return true;
    } catch (error) { if (epoch === state.sessionEpoch && revision === state.invitationRevision && state.session?.canManageAccess) message('#invitations-message', error.message, 'error'); return false; }
    finally { if (epoch === state.sessionEpoch && revision === state.invitationRevision && state.session?.canManageAccess) { state.loadingInvitations = false; $('#refresh-invitations').disabled = false; } }
  }
  function lockInvitations(locked) {
    state.savingInvitation = locked; $('#invitation-fields').disabled = locked; $('#refresh-invitations').disabled = locked; $('#invitation-list').setAttribute('aria-busy', String(locked)); renderInvitations();
  }
  async function createInvitation(event) {
    event.preventDefault();
    if (!state.session?.authenticated || !state.session.canManageAccess || state.savingInvitation || !$('#invitation-form').reportValidity()) return;
    const email = $('#invitation-email').value.trim();
    if (!email) return;
    const epoch = state.sessionEpoch; const revision = ++state.invitationRevision; state.loadingInvitations = false;
    clearInvitationLink(); state.pendingRevokeId = null; lockInvitations(true); message('#invitation-message', ''); $('#create-invitation .button-label').textContent = 'Creating…';
    try {
      const data = await api('/api/admin/invitations', { method: 'POST', body: { email } });
      if (epoch !== state.sessionEpoch || revision !== state.invitationRevision || !state.session?.canManageAccess) return;
      const invitation = data?.invitation; const url = new URL(data?.url);
      if (!invitation || typeof invitation.id !== 'string' || typeof invitation.email !== 'string' || invitation.status !== 'pending' || !Number.isFinite(Date.parse(invitation.expiresAt)) || url.origin !== new URL(window.location.href).origin || !new URLSearchParams(url.hash.slice(1)).get('invite')) throw new APIError('The invitation may have been created, but its private link could not be read. Refresh the history and create a replacement for the same email.');
      state.invitations = state.invitations.map(item => item.status === 'pending' && item.email.toLowerCase() === invitation.email.toLowerCase() ? { ...item, status: 'revoked' } : item);
      state.invitations.unshift(invitation);
      if (state.activePanel === 'access') {
        state.invitationLink = { id: invitation.id, url: url.href }; $('#invitation-link').value = url.href;
        $('#invitation-result-description').textContent = `For ${invitation.email}. Expires ${invitationTime(invitation.expiresAt)} (your local time).`;
        $('#invitation-result').hidden = false; $('#copy-invitation').focus();
      }
      message('#invitation-message', state.activePanel === 'access' ? 'Invitation created. Copy the link and share it privately.' : 'Invitation created. Create a replacement to show a new private link.', 'success');
    } catch (error) { if (epoch === state.sessionEpoch && revision === state.invitationRevision && state.session?.canManageAccess) message('#invitation-message', error instanceof TypeError ? 'The invitation result could not be read. Refresh the history before creating a replacement for the same email.' : error.message, 'error'); }
    finally { if (epoch === state.sessionEpoch && revision === state.invitationRevision && state.session?.canManageAccess) { lockInvitations(false); $('#create-invitation .button-label').textContent = 'Create invitation'; } }
  }
  async function revokeInvitation(invitation) {
    if (!state.session?.authenticated || !state.session.canManageAccess || state.savingInvitation || state.pendingRevokeId !== invitation.id || !state.invitations.some(item => item.id === invitation.id && item.status === 'pending')) return;
    const epoch = state.sessionEpoch; const revision = ++state.invitationRevision; state.loadingInvitations = false;
    lockInvitations(true); message('#invitation-message', '');
    try {
      await api(`/api/admin/invitations/${encodeURIComponent(invitation.id)}`, { method: 'DELETE' });
      if (epoch !== state.sessionEpoch || revision !== state.invitationRevision || !state.session?.canManageAccess) return;
      state.invitations = state.invitations.map(item => item.id === invitation.id ? { ...item, status: 'revoked' } : item);
      if (state.invitationLink?.id === invitation.id) clearInvitationLink();
      message('#invitation-message', 'Invitation revoked. Its link can no longer be accepted.', 'success');
    } catch (error) { if (epoch === state.sessionEpoch && revision === state.invitationRevision && state.session?.canManageAccess) message('#invitation-message', error.message, 'error'); }
    finally { if (epoch === state.sessionEpoch && revision === state.invitationRevision && state.session?.canManageAccess) { const restoreFocus = state.pendingRevokeId === invitation.id && state.activePanel === 'access'; if (state.invitations.some(item => item.id === invitation.id && item.status === 'revoked')) state.pendingRevokeId = null; lockInvitations(false); if (restoreFocus) renderInvitations(invitation.id, 'confirm'); } }
  }
  async function openGoogle(path, body) {
    if (state.connecting || state.configuring || state.disconnecting) return;
    invalidateSessionRefresh(); state.connecting = true; $('#connect-google').disabled = true; $('#signin-google').disabled = true;
    const epoch = state.sessionEpoch;
    try { const data = await api(path, { method: 'POST', body }); if (epoch !== state.sessionEpoch) return; const url = new URL(data?.url); if (url.protocol !== 'https:' || url.hostname !== 'accounts.google.com') throw new APIError('Google sign-in could not be opened. Please try again.'); invitationToken = null; window.location.assign(url.href); }
    catch (error) { if (epoch === state.sessionEpoch) { state.connecting = false; $('#signin-google').disabled = false; renderConnection(); message(state.session?.authenticated && !$('#portal').hidden ? '#calendar-message' : '#page-message', error.message, 'error'); } }
  }
  function connectGoogle() {
    if (!state.session?.authenticated || state.session.role === 'bootstrap' || !state.session.canConnectCalendar || state.session.google?.requiresClientConfiguration) return;
    return openGoogle('/api/admin/google/connect', {});
  }
  function signInGoogle() { return invitationToken ? openGoogle('/api/admin/invitations/accept', { token: invitationToken }) : openGoogle('/api/admin/signin', {}); }
  async function start() {
    const epoch = ++state.sessionEpoch;
    invalidateSessionRefresh();
    $('#loading-view').hidden = false; $('#signin-view').hidden = true; message('#page-message', '');
    try {
      let session;
      if (bootstrapToken) { const token = bootstrapToken; bootstrapToken = null; session = await api('/api/admin/bootstrap', { method: 'POST', body: { token } }); }
      else session = await api('/api/admin/session');
      if (epoch !== state.sessionEpoch) return;
      if (!session || typeof session.authenticated !== 'boolean') throw new APIError('Your session could not be checked. Please try again.');
      state.session = session; state.csrf = session.csrfToken || ''; renderConnection();
      if (!session.authenticated || invitationToken) showSignedOut();
      else if (session.role === 'bootstrap') {
        $('#portal').hidden = true; $('#signin-view').hidden = false; $('#logout').hidden = false;
        $('#signin-google').hidden = !session.google?.configured; $('#signin-google').disabled = Boolean(session.google?.requiresClientConfiguration);
        $('#signin-google .button-label').textContent = 'Finish setup with Google';
        $('#signin-copy').textContent = session.google?.requiresClientConfiguration ? 'Your private setup link is verified. Import the private Google configuration below, then finish setup with Google.' : session.google?.configured ? 'Your private setup link is verified. Sign in with the Google account that will manage this installation. The calendar connection is a separate step.' : 'Your private setup link is verified. Google sign-in still needs configuration from the person setting up this installation.';
      }
      else {
        $('#portal').hidden = false; $('#logout').hidden = false; $('#signin-view').hidden = true;
        const results = await Promise.allSettled([loadBookings(), loadSettings()]);
        if (epoch !== state.sessionEpoch || !state.session?.authenticated) return;
        if (results.some(result => result.status === 'rejected')) message('#page-message', 'Some portal information could not be loaded. Refresh to try again.', 'error');
        schedulePoll();
      }
      const outcomes = { denied: 'Google sign-in was cancelled. Try again, or reopen the private link if you were accepting an invitation.', failed: 'Google could not complete this request. Try signing in again, or reopen your private invitation link to accept it.', wrong_account: 'That Google account does not have access for this request. Use your approved account. If you were accepting an invitation, reopen its private link and choose the exact invited email.', invalid_invitation: 'This invitation is not valid. Ask the operator for a new private link.', invitation_expired: 'This invitation has expired. Ask the operator for a new private link.', invitation_revoked: 'This invitation has been revoked. Ask the operator for a new private link.', invitation_used: 'This invitation has already been accepted. Sign in with your approved Google account.', member_already_exists: 'This Google account already has access to the shared workspace. Sign in with Google to continue; you do not need another invitation.', missing_scopes: 'Google Calendar permissions were not completed. Connect again and allow the requested calendar access.', configuration_required: 'Google sign-in needs a one-time setup on this installation. Ask the person who set up this portal to finish the private Google connection settings, then try again.' };
      if (outcomes[oauthOutcome]) message('#page-message', outcomes[oauthOutcome], oauthOutcome === 'denied' ? '' : 'error');
      if (['connected', 'missing_scopes', 'configuration_required'].includes(oauthOutcome) && state.session?.authenticated) switchPanel('calendar');
      if (oauthOutcome === 'invited' && state.session?.authenticated) message('#page-message', 'Welcome to the shared workspace. You can now manage the same appointments, callbacks and availability.', 'success');
    } catch (error) { if (epoch === state.sessionEpoch) { showSignedOut(); message('#page-message', error.message, 'error'); } }
    finally { if (epoch === state.sessionEpoch || !state.session?.authenticated) $('#loading-view').hidden = true; }
  }
  function schedulePoll() {
    clearTimeout(state.poll);
    if (!state.session?.authenticated || state.session.role === 'bootstrap') return;
    const epoch = state.sessionEpoch;
    state.poll = setTimeout(async () => {
      if (!document.hidden) {
        await refreshSession();
        if (epoch !== state.sessionEpoch || !state.session?.authenticated) return;
        if (state.activePanel === 'bookings') await loadBookings(true);
        if (state.activePanel === 'access' && !$('#invitation-list').contains(document.activeElement)) await loadInvitations();
        if (state.activePanel === 'access' && !$('#member-list').contains(document.activeElement)) await loadMembers();
      }
      if (epoch === state.sessionEpoch) schedulePoll();
    }, 60000);
  }
  function refreshVisibleBookings() {
    if (document.hidden || !state.session?.authenticated || state.session.role === 'bootstrap') return;
    refreshSession();
    if (state.activePanel === 'bookings') loadBookings(true);
    if (state.activePanel === 'access' && !$('#invitation-list').contains(document.activeElement)) loadInvitations();
    if (state.activePanel === 'access' && !$('#member-list').contains(document.activeElement)) loadMembers();
    schedulePoll();
  }
  document.querySelectorAll('[data-panel]').forEach(button => button.addEventListener('click', () => switchPanel(button.dataset.panel, true, button.dataset.kind)));
  $('#session-retry').addEventListener('click', start);
  $('#invitation-form').addEventListener('submit', createInvitation);
  $('#refresh-invitations').addEventListener('click', loadInvitations);
  $('#refresh-members').addEventListener('click', loadMembers);
  $('#hide-invitation').addEventListener('click', () => { clearInvitationLink(); message('#invitation-message', 'Private link hidden.'); $('#invitation-email').focus(); });
  $('#copy-invitation').addEventListener('click', async () => {
    const link = state.invitationLink; const epoch = state.sessionEpoch;
    if (!link || !state.session?.canManageAccess) return;
    try {
      if (!navigator.clipboard?.writeText) throw new Error('Clipboard unavailable');
      await navigator.clipboard.writeText(link.url);
      if (epoch === state.sessionEpoch && state.invitationLink === link) message('#invitation-message', 'Private link copied. Share it only with the invited person.', 'success');
    } catch {
      if (epoch === state.sessionEpoch && state.invitationLink === link) { $('#invitation-link').focus(); $('#invitation-link').select(); message('#invitation-message', 'Select and copy the private link below. Automatic copying is unavailable in this browser.'); }
    }
  });
  $('#replace-google-configuration').addEventListener('click', () => { state.replacingConfiguration = true; message('#configuration-message', ''); renderConnection(); $('#google-configuration-file').focus(); });
  $('#cancel-configuration').addEventListener('click', () => { state.replacingConfiguration = false; $('#google-configuration-file').value = ''; message('#configuration-message', ''); renderConnection(); $('#replace-google-configuration').focus(); });
  $('#google-configuration-file').addEventListener('change', () => { message('#configuration-message', ''); renderConnection(); });
  $('#google-configuration-form').addEventListener('submit', async event => {
    event.preventDefault();
    if (state.configuring || state.connecting || state.disconnecting || !state.session?.authenticated || (!state.session.canManageAccess && state.session.role !== 'bootstrap')) return;
    const file = $('#google-configuration-file').files[0];
    if (!file) { message('#configuration-message', 'Choose the private Google configuration JSON file first.', 'error'); return; }
    const epoch = state.sessionEpoch;
    let payload = null;
    let imported = false;
    invalidateSessionRefresh(); state.configuring = true; renderConnection(); $('#import-google-configuration .button-label').textContent = 'Importing…'; message('#configuration-message', '');
    try {
      if (file.size <= 0 || file.size > 65536) throw new Error('Choose a Google configuration JSON file no larger than 64 KB.');
      let content;
      try { content = await file.text(); } catch { throw new Error('This file could not be read. Choose it again and retry.'); }
      if (epoch !== state.sessionEpoch || !state.session?.authenticated) return;
      payload = parseGoogleConfiguration(content, file.size); content = null;
      try { await api('/api/admin/google/configure', { method: 'POST', body: payload }); }
      catch (error) { throw new Error(error.code === 'configuration_unavailable' ? 'This installation does not support file import. Ask the person who set it up to check its Google settings.' : error.status === 400 || error.status === 409 ? 'This configuration could not be accepted. Check that the file belongs to this installation’s Google client.' : error.status === 403 ? 'Your session changed. Refresh the portal before importing again.' : error.status === 401 ? 'Sign in to the owner portal and import the configuration again.' : 'The configuration could not be imported. Check your connection and try again.'); }
      payload = null;
      if (epoch !== state.sessionEpoch || !state.session?.authenticated) return;
      imported = true; $('#google-configuration-file').value = '';
      const session = await api('/api/admin/session');
      if (epoch !== state.sessionEpoch) return;
      if (!session?.authenticated) { showSignedOut(); return; }
      state.session = session; state.csrf = session.csrfToken || '';
      if (session.google?.requiresClientConfiguration) throw new Error('The file was received, but Google configuration still needs attention. Refresh the portal and check the installation settings.');
      state.replacingConfiguration = false;
    } catch (error) {
      if (epoch === state.sessionEpoch) message('#configuration-message', imported ? 'The file was received, but the updated connection could not be checked. Refresh the portal before connecting.' : error.message, 'error');
    } finally {
      payload = null;
      if (epoch === state.sessionEpoch) {
        state.configuring = false; $('#import-google-configuration .button-label').textContent = 'Import configuration';
        renderConnection();
        if (imported && !state.session?.google?.requiresClientConfiguration && !state.replacingConfiguration) {
          const initialSetup = state.session.role === 'bootstrap';
          message(initialSetup ? '#page-message' : '#calendar-message', initialSetup ? 'Private Google configuration saved. Finish setup with Google to continue.' : state.session.google?.connected && !state.session.google?.error ? 'Private Google configuration saved. Google Calendar is connected.' : 'Private Google configuration saved. You can now connect Google Calendar.', 'success');
          $(initialSetup ? '#signin-google' : $('#connect-google').hidden ? '#replace-google-configuration' : '#connect-google').focus();
        }
      }
    }
  });
  $('#connect-google').addEventListener('click', connectGoogle); $('#signin-google').addEventListener('click', signInGoogle);
  $('#refresh-bookings').addEventListener('click', () => { loadBookings(); refreshSession(); });
  $('#booking-filter').addEventListener('change', renderBookings); $('#booking-search').addEventListener('input', renderBookings);
  $('#close-booking').addEventListener('click', () => dialog.close());
  dialog.addEventListener('close', () => { state.selectedId = null; });
  $('#booking-update-form').addEventListener('submit', event => { event.preventDefault(); const booking = state.bookings.find(item => item.id === state.selectedId); if (!booking) return; const payload = { adminNotes: $('#detail-notes').value }; if (booking.status !== 'cancelled') payload.status = $('#detail-status').value; mutateBooking('PATCH', '', payload, 'Changes saved.'); });
  $('#cancel-booking').addEventListener('click', () => { if (window.confirm('Cancel this appointment request? Its calendar event will be removed when synchronization completes. Scheduling again requires a new request.')) mutateBooking('PATCH', '', { status: 'cancelled' }, 'Cancellation saved. Check the calendar status above for removal progress.'); });
  $('#retry-booking').addEventListener('click', () => mutateBooking('POST', '/retry', {}, 'Calendar synchronization queued. Refresh requests to check its progress.'));
  $('#settings-form').addEventListener('input', dirty); $('#settings-form').addEventListener('change', dirty);
  $('#add-exception').addEventListener('click', () => { const input = appendException(); dirty(); input.focus(); });
  $('#add-time-off').addEventListener('click', () => { $('#time-off-panel').open = true; const input = appendTimeOff(); dirty(); input.focus(); });
  $('#settings-form').addEventListener('invalid', event => { if (event.target.closest('#time-off-panel')) $('#time-off-panel').open = true; }, true);
  $('#reset-settings').addEventListener('click', () => { if (!state.settings) { loadSettings(); return; } if (!state.dirty || window.confirm('Discard your unsaved availability changes?')) { renderSettings(state.settings); message('#settings-message', ''); } });
  $('#settings-form').addEventListener('submit', async event => {
    event.preventDefault(); if ($('#save-settings').disabled || !$('#settings-form').reportValidity()) return;
    let settings; try { settings = readSettings(); } catch (error) { message('#settings-message', error.message, 'error'); return; }
    const epoch = state.sessionEpoch;
    $('#settings-fields').disabled = true; $('#save-settings').disabled = true; $('#reset-settings').disabled = true; $('#settings-state').textContent = 'Saving availability…'; message('#settings-message', '');
    try { const data = await api('/api/admin/settings', { method: 'PUT', body: settings }); if (epoch !== state.sessionEpoch || !state.session?.authenticated) return; renderSettings(data?.timeZone ? data : settings); message('#settings-message', 'Availability saved. New requests will use these hours.', 'success'); }
    catch (error) { if (epoch === state.sessionEpoch) { message('#settings-message', error.message, 'error'); $('#settings-state').textContent = 'Changes not saved'; } }
    finally { if (epoch === state.sessionEpoch) { $('#settings-fields').disabled = false; $('#save-settings').disabled = false; $('#reset-settings').disabled = false; } }
  });
  $('#disconnect-google').addEventListener('click', async () => {
    if (state.disconnecting || state.configuring || state.connecting || !state.session?.canConnectCalendar) return;
    if (!window.confirm('Disconnect Google Calendar? Online times will be unavailable until you reconnect. Existing calendar events will remain.')) return;
    const epoch = state.sessionEpoch;
    invalidateSessionRefresh(); state.disconnecting = true; $('#disconnect-google').disabled = true;
    try { await api('/api/admin/google/disconnect', { method: 'POST', body: {} }); if (epoch !== state.sessionEpoch) return; const session = await api('/api/admin/session'); if (epoch !== state.sessionEpoch) return; if (!session?.authenticated) { showSignedOut(); return; } state.session = session; state.csrf = session.csrfToken || ''; state.disconnecting = false; renderConnection(); message('#calendar-message', 'Google Calendar disconnected. Online booking is unavailable until you reconnect.'); }
    catch (error) { if (epoch === state.sessionEpoch) { state.disconnecting = false; renderConnection(); message('#calendar-message', error.message, 'error'); } }
    finally { if (epoch === state.sessionEpoch) { state.disconnecting = false; $('#disconnect-google').disabled = false; } }
  });
  $('#logout').addEventListener('click', async () => {
    if (state.dirty && !window.confirm('Sign out and discard your unsaved availability changes?')) return;
    const epoch = state.sessionEpoch;
    $('#logout').disabled = true;
    try { await api('/api/admin/logout', { method: 'POST', body: {} }); if (epoch === state.sessionEpoch) showSignedOut(); }
    catch (error) { if (epoch === state.sessionEpoch) message('#page-message', error.message, 'error'); }
    finally { if (epoch === state.sessionEpoch) $('#logout').disabled = false; }
  });
  window.addEventListener('beforeunload', event => { if (state.dirty && state.session?.authenticated) { event.preventDefault(); event.returnValue = ''; } });
  window.addEventListener('pagehide', () => { invitationToken = null; bootstrapToken = null; showSignedOut(); });
  window.addEventListener('pageshow', event => { if (event.persisted) { showSignedOut(); start(); } });
  window.addEventListener('focus', refreshVisibleBookings);
  document.addEventListener('visibilitychange', () => { if (document.hidden) clearTimeout(state.poll); else refreshVisibleBookings(); });
  start();
})();
