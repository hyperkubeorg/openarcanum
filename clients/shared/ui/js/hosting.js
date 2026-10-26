// Communities hosted on this computer (openarcanum/hosting.py) and what the
// maintainer nodes say about a community's server (docs/protocol.md §7.4):
// the "Offline · last seen …" and "Shut down" wording used by the rail,
// the channel header and Find a community; the hosted community's entries
// in the community menu and its banner; the warning when quitting would take
// hosted communities offline.
'use strict';

// "3 minutes ago", "2 days ago".
function agoText(ms) {
  const s = Math.max(0, Math.round((Date.now() - ms) / 1000));
  if (s < 60) return t('just now');
  const m = Math.round(s / 60);
  if (m < 60) return tn('{n} minute ago', '{n} minutes ago', m);
  const hrs = Math.round(m / 60);
  if (hrs < 48) return tn('{n} hour ago', '{n} hours ago', hrs);
  const d = Math.round(hrs / 24);
  return tn('{n} day ago', '{n} days ago', d);
}

// What to say about a community this device can't reach, from the
// maintainers' report on its server; '' when there's nothing to add.
function serverStatusLine(summary) {
  const s = summary && summary.server_status;
  if (summary && summary.connection === 'shut_down') {
    return tn('Shut down: its server was offline for over {n} day', 'Shut down: its server was offline for over {n} days', (s && s.reap_after_days) || 7);
  }
  if (!s || summary.connection === 'online' || summary.connection === 'pending') return '';
  if (s.state === 'offline' && s.pinned && !s.stopping && s.relays_attached === 0) {
    return t("Offline: its server is up, but none of its own relays can be reached (it doesn't move to other relays)");
  }
  if (s.state === 'offline') return s.last_seen ? t('Offline · last seen {ago}', { ago: agoText(s.last_seen) }) : t('Offline');
  if (s.state === 'degraded') {
    return t('Its server reaches {attached} of its {wanted} relays', { attached: s.relays_attached, wanted: s.relays_wanted });
  }
  return '';
}

function hostedStateLabel(hosted) {
  return { running: t('Running'), starting: t('Starting…'), restarting: t('Restarting…'), stopped: t('Stopped'), failed: t("Can't run") }[hosted.state] || hosted.state;
}

// The community menu: a hosted community's server, and starting or stopping it.
function hostingMenuItems(summary) {
  const hosted = summary.hosted;
  if (!hosted) return [];
  const id = summary.id;
  const act = (method, done) => api(method, id).then(() => { if (done) toast(done); return refreshCommunities(); }, toastError);
  const running = hosted.state !== 'stopped' && hosted.state !== 'failed';
  return [
    { group: t('Hosted on this computer: {state}', { state: hostedStateLabel(hosted) }), items: [
      running
        ? { label: t('Stop hosting'), run: () => stopHostingDialog(summary) }
        : { label: t('Start hosting'), run: () => act('hosting_start', t('{community} is starting on this computer.', { community: summary.name })) },
      summary.connection === 'shut_down' ? { label: t('Bring it back online'), run: () => act('hosting_reregister', t('{community} is registering with the maintainer nodes again.', { community: summary.name })) } : null,
    ] },
  ];
}

function stopHostingDialog(summary) {
  modal({ title: t('Stop hosting {community}?', { community: summary.name }), body: h('div', { class: 'stack' },
    h('p', { text: t("Members can't reach it until you start it again (the community menu → Start hosting). It stays stopped when OpenArcanum starts.") }),
    h('p', { class: 'small muted', text: t('After 7 days offline, the maintainer nodes shut it down.') })),
  actions: [{ label: t('Cancel') }, { label: t('Stop hosting'), kind: 'danger', run: async () => {
    await api('hosting_stop', summary.id);
    await refreshCommunities();
  } }] });
}

// Above the channel view: a hosted community that isn't serving, or one the
// maintainers shut down.
function hostingBanner(summary) {
  const hosted = summary.hosted;
  const days = (summary.server_status && summary.server_status.reap_after_days) || 7;
  if (summary.connection === 'shut_down') {
    return h('div', { class: 'banner bad shut-down' },
      h('span', { class: 'spacer', text: hosted
        ? tn('This community was shut down: this computer was offline for over {n} day. Bring it back online to register it with the maintainer nodes again.',
          'This community was shut down: this computer was offline for over {n} days. Bring it back online to register it with the maintainer nodes again.', days)
        : tn('This community was shut down (its server has been offline for over {n} day).', 'This community was shut down (its server has been offline for over {n} days).', days) }),
      hosted ? h('button', { class: 'btn small', text: t('Bring it back online'), onclick: () => api('hosting_reregister', summary.id).then(refreshCommunities, toastError) }) : null);
  }
  if (hosted && (hosted.state === 'stopped' || hosted.state === 'failed' || hosted.state === 'restarting')) {
    return h('div', { class: 'banner warn hosted-banner' },
      h('span', { class: 'spacer', text: hosted.state === 'stopped'
        ? t('This computer hosts this community, and hosting is stopped: members can\'t reach it.')
        : hosted.error || t("This computer hosts this community, but its server isn't running.") }),
      hosted.state === 'stopped' ? h('button', { class: 'btn small', text: t('Start hosting'), onclick: () => api('hosting_start', summary.id).then(refreshCommunities, toastError) }) : null);
  }
  return null;
}

on('hosting', () => { refreshCommunities().catch(() => {}); });

// Quitting (the tray menu, Ctrl+Q, closing the window without a tray) while
// this computer hosts communities: they go offline, so ask first (app.py).
on('confirm_quit', ({ count }) => {
  if ($('.quit-hosting')) return;
  const names = (st.communities || []).filter((c) => c.hosted && c.hosted.state !== 'stopped').map((c) => c.name);
  const m = modal({ title: t('Quit OpenArcanum?'), body: h('div', { class: 'stack quit-hosting' },
    h('p', { text: tn('This computer hosts {n} community. It goes offline until you open OpenArcanum again.', 'This computer hosts {n} communities. They go offline until you open OpenArcanum again.', count) }),
    names.length ? h('p', { class: 'small', text: names.join(', ') }) : null,
    h('p', { class: 'small muted', text: t('To keep them up with the window closed, close it to the tray instead (Settings → Appearance → Keep running in the tray). After 7 days offline, the maintainer nodes shut a community down.') })),
  actions: [{ label: t('Cancel') }, { label: t('Quit anyway'), kind: 'danger', run: () => api('quit_app') }] });
  return m;
});
