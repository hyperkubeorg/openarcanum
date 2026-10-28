// A voice channel's text chat, in the right-hand panel (docs/protocol.md
// §4.3), like Discord's text in voice: opened with the Chat button on the
// voice channel or the call stage, whether or not you're in the call. It
// takes the place of the member list while open and has its own message
// list and composer (chat.js PANES.side); messages, unread counts and
// permissions are the same as a text channel's.
'use strict';

const voiceChat = {
  // The open voice channel, if it still exists and is visible.
  channel() {
    const s = cstate();
    return s && st.voiceChat ? s.channels.find((c) => c.id === st.voiceChat && c.kind === 'voice') || null : null;
  },

  shown() {
    const summary = st.communities.find((c) => c.id === st.cid);
    return st.view === 'community' && !!summary && featureOn(summary, 'voice') && !!this.channel();
  },

  async open(channelId) {
    // The chat shows beside the channel or the call, not beside a page.
    leavePage(false);
    if (st.voiceChat !== channelId) {
      st.voiceChat = channelId;
      chat.sideScrolled = false;
      // The "New" marker goes after what was read before opening it.
      const c = chanById(channelId);
      chat.newSince[key(st.cid, channelId)] = c && c.unread ? c.last_read || 0 : undefined;
    }
    this.render();
    renderSidebar();
    voiceUI.refreshStageHead();
    focusComposer('side');
    const cid = st.cid;
    await loadHistory(cid, channelId);
    if (st.cid === cid && st.voiceChat === channelId) markRead(cid, channelId);
  },

  close() {
    const was = st.voiceChat;
    st.voiceChat = null;
    if (st.replyTo && st.replyTo.channel_id === was) st.replyTo = null;
    renderMembers();
    renderSidebar();
    voiceUI.refreshStageHead();
    // Focus goes back to what opened it: the channel's Chat button.
    const back = was && $(`.chan.voice[data-channel="${was}"] .chat-btn`);
    if (back) back.focus();
  },

  toggle(channelId) {
    if (st.voiceChat === channelId && this.shown() && !pageOpen()) this.close();
    else this.open(channelId).catch(toastError);
  },

  // The panel goes back to being the member list.
  leavePanel(panel) {
    if (!panel.classList.contains('voice-chat')) return;
    panel.classList.remove('voice-chat');
    panel.setAttribute('aria-label', t('Members'));
    delete panel.dataset.channel;
    clear(panel);
  },

  whoText(people) {
    return people.length ? t('In the call: {names}', { names: people.map((p) => p.name || t('Someone')).join(', ') })
      : t('Nobody is in the call right now.');
  },

  // Who's in the call changed: only that line (and the Join button) change.
  refreshWho() {
    const c = this.channel();
    const who = $('#members .vc-who');
    if (!c || !who) return;
    who.textContent = this.whoText(voiceUI.roomOf(st.cid, c.id));
    const inCall = voiceUI.current && voiceUI.current.cid === st.cid && voiceUI.current.channel === c.id;
    if (!!$('#members .vc-head .primary') === (!inCall && !!c.can.connect_voice)) return;
    this.render();
  },

  // Draws the panel. What's being typed (and where the cursor is)
  // survives a redraw, as in the main view.
  render() {
    const panel = $('#members');
    const c = this.channel();
    const summary = st.communities.find((x) => x.id === st.cid);
    if (!c || !summary) return;
    const was = document.activeElement;
    const keep = was && was.id === PANES.side.input && panel.contains(was) && panel.dataset.channel === String(c.id)
      ? { start: was.selectionStart, end: was.selectionEnd } : null;
    const oldList = document.getElementById(PANES.side.list);
    const scroll = oldList && panel.dataset.channel === String(c.id) ? oldList.scrollTop : null;
    keepingFocus(panel, () => {
      clear(panel);
      panel.classList.remove('hidden');
      panel.classList.add('voice-chat');
      panel.dataset.channel = String(c.id);
      panel.setAttribute('aria-label', t('Chat in {channel}', { channel: c.name }));
      const inCall = voiceUI.current && voiceUI.current.cid === st.cid && voiceUI.current.channel === c.id;
      const people = voiceUI.roomOf(st.cid, c.id);
      const canJoin = !inCall && c.can.connect_voice;
      panel.appendChild(h('div', { class: 'vc-head' },
        h('span', { class: 'muted' }, icon('volume-2')), h('span', { class: 'title', text: c.name }), h('div', { class: 'spacer' }),
        canJoin ? h('button', { class: 'btn small primary', text: t('Join voice'), onclick: () => voiceUI.join(st.cid, c.id) }) : null,
        h('button', { class: 'icon-btn', title: t('Close chat'), 'aria-label': t('Close chat'), icon: 'x', onclick: () => this.close() })));
      panel.appendChild(h('div', { class: 'vc-who small muted', 'aria-live': 'polite', text: this.whoText(people) }));
      const list = h('div', { class: 'messages', id: PANES.side.list, role: 'log', 'aria-live': 'polite', 'aria-label': t('Messages in {channel}', { channel: c.name }) });
      list.addEventListener('scroll', () => { if (list.scrollTop < 60) loadOlder('side'); });
      panel.appendChild(list);
      panel.appendChild(jumpToLatest(list));
      panel.appendChild(h('div', { class: 'typing small muted', id: PANES.side.typing, 'aria-live': 'polite' }));
      renderMessages(false, 'side');
      if (scroll !== null) list.scrollTop = scroll;
      renderTyping();
      panel.appendChild(composer(c, summary, 'side'));
    });
    if (keep) {
      const input = document.getElementById(PANES.side.input);
      if (input) {
        input.focus();
        try { input.setSelectionRange(keep.start, keep.end); } catch (e) { /* no selection to restore */ }
      }
    }
  },
};
