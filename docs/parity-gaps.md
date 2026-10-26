# Discord features not built

OpenArcanum's community features follow Discord's where that fits a server
the community runs itself. These Discord features are not built.

| Discord feature | In OpenArcanum |
|---|---|
| Announcement channels and channel following | Only text and voice channels. |
| Stage channels, forum channels, threads | Not built; replies quote and jump to the original instead of opening a thread. |
| Role icons, gradient role colors | Roles have one color. |
| Permission-sync warnings ("this channel is out of sync, sync now?") when moving a channel | Moving a synced channel takes the new category's permissions; an unsynced one keeps its own. The channel dialog shows whether it's synced and offers "Sync permissions with the category". |
| Drag-and-drop ordering of channels, categories and roles | **Move up** and **Move down** buttons instead. |
| Per-channel "Manage Channel", "Manage Permissions" and "Create Invite" overrides; private-channel creation wizard | Overrides cover View Channel and the text and voice permissions; managing channels, roles and invites is community-wide. |
| "View server as role" preview | Not built. |
| Role-specific member counts and role search in the editor | Not built. |
| Invite pause ("disable invites") and vanity URLs | Revoke individual invites; the join policy covers closing the community. |
| Slowmode for thread creation, and per-role slowmode bypass other than Manage Messages/Channels | No threads; the exemption follows Discord's Manage Messages/Channels rule. |
| Message forwarding (to another channel or DM) | Copy text works. Forwarding would re-post content across communities with different admins and retention, so it needs its own design. |
| Polls | Not built. |
| Voice messages (recorded audio clips) | Audio files can be attached like any file. |
| Stickers, super reactions | Not built. |
| Suppressing or removing a link preview after posting ("remove embed") | Write the link as `<https://…>` to suppress it before posting; editing the link fetches it again. |
| Embeds sent by clients or bots (rich embeds, editing embeds) | There are no bots; previews are only made by the community server. |
| Search filters (`from:`, `in:`, `has:`, date ranges) | Search is full-text over readable channels; filtering by author, channel, attachment or date isn't built. |
| GIF picker, emoji picker with search, recent emoji | Reactions offer six quick emoji, the community's custom emoji and free entry; the custom emoji button next to the composer lists the community's emoji. |
| Mark unread from a message, saved or bookmarked messages | "Mark as read" exists per channel and per community. |
| Reply without pinging (reply mention toggle) | Replies don't mention the original author at all. |
| Timestamp markup (`<t:…>`), subtext (`-#`), masked-link warnings | Links always show their URL next to their label. |
| Scheduled messages, message reminders | Not built. |
| AutoMod ML filters (Discord's "commonly flagged words" presets and spam detection) | Keyword, regex, mention-spam and link rules only (`docs/admin-guide.md` "AutoMod"). Server-side ML would need a model on every community server. |
| AutoMod "block member profile names" and custom block messages | Rules check message text and captions; the block reason is fixed text naming the rule. |
| AutoMod exempting "Manage Server" holders automatically | Nobody is exempt unless a rule lists their role or channel (`docs/protocol.md` §4.8). |
| Safety alerts (raid detection, DM-spam alerts) | Raid revoke and invite revocation exist; there's no automatic detection. |
| Verification levels beyond membership age (verified email, phone, "member for 10 minutes on Discord") | OpenArcanum has no accounts; member screening by membership age is the only level. |
| Membership screening rules (a rules screen to accept before posting), onboarding questionnaires | Screening is by membership age only. |
| Server insights and analytics | Not built. |
| Moderator view of a member's message history ("view messages from user") and ban appeals | Not built; moderators search and use the audit log. |
| Timed-out members kept out of voice channels | Discord stops a timed-out member joining voice. OpenArcanum lets them join and listen, but the call is listen-only for them (no speaking, camera or screen share, enforced by the relay) until the timeout ends, matching the rule that a timed-out member can still read (`docs/admin-guide.md` "Timeouts"). |
