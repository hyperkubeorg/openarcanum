# OpenArcanum design language

The desktop client's interface (`clients/shared/ui`: `index.html`,
`styles.css`, `js/*.js`) is plain JavaScript and one stylesheet. This page is
the system that stylesheet implements. Every surface is built from the same
tokens and components; a new screen should need no new CSS beyond its own
layout. When this page and the code disagree, fix one of them.

Dark is the default theme (the switch is on every screen before unlock and in
Settings → Appearance). Light and dark share every rule below; only the colour
tokens change.

## Tokens

All tokens are CSS custom properties on `:root` (light) and
`:root[data-theme="dark"]` (also used for `system` when the desktop is dark).

### Colour roles

| Role | Tokens | Use |
| --- | --- | --- |
| Chrome | `--rail`, `--bg3` | The community rail and user panel (darkest); the sidebar and member list |
| Content | `--bg` | The message view; pages (Settings, Community Admin), their section list included |
| Raised | `--bg2` | Modals, menus, toasts, the message box, cards in messages |
| Grouped | `--section-bg`, `--foot-bg` | Boxes of settings (form sections, tables, cards) and their footers / table heads |
| Text | `--fg`, `--fg2`, `--muted` | Primary, secondary, and tertiary text (all at 4.5:1 or better) |
| Lines | `--line` | Dividers and box borders |
| Accent | `--accent`, `--accent-hover`, `--accent-soft`, `--accent-text`, `--link` | The primary action, selection marks, toggled-on state, links |
| Danger | `--danger`, `--danger-hover`, `--danger-soft`, `--danger-text` | Destructive actions and errors; also Community Admin (red text) |
| Status | `--ok*`, `--warn*` | Online, live, saved; warnings and notices |
| States | `--hover`, `--sel`, `--focus` | Translucent fills that work on any surface; the focus ring |
| Controls | `--btn-*`, `--field-*`, `--switch-off`, `--pill-bg`, `--code-bg` | Secondary buttons, form fields (border at 3:1 or better), switches, pills, code |

Role colours for member names come from `--role-color`, clamped per theme to
keep 4.5:1.

### Scales

- **Spacing**: `--s1` 4px, `--s2` 8px, `--s3` 12px, `--s4` 16px, `--s5` 24px,
  `--s6` 32px. Gaps inside a control are 4 to 8; between controls 8 to 12; between
  boxes 24; dialog and page padding 24 to 32.
- **Radii**: `--r-xs` 4px (marks: inline code, menu rows, badges), `--r-sm` 6px
  (controls: buttons, fields, list rows), `--r-md` 8px (boxes: sections,
  cards, menus, toasts, tables), `--r-lg` 12px (dialogs, the lock card). Pills
  are fully round.
- **Type** (rem, so the Text size setting scales everything and nothing
  compounds): `--fs-xs` .75rem (overlines, timestamps, badges), `--fs-sm`
  .85rem (descriptions, hints, small buttons), `--fs-md` 1rem (body, box
  titles), `--fs-lg` 1.125rem (card titles), `--fs-xl` 1.375rem (dialog and
  page titles), `--fs-2xl` 1.625rem (the lock screens' title).
- **Control heights** (minimums; they grow with larger text): `--h-sm` 28px
  (small buttons), `--h-md` 36px (buttons, fields), `--h-row` 32px (list rows,
  menu rows, navigation rows), `--h-bar` 48px (the bar at the top of each
  column).
- **Icons** (Lucide, line icons via `icon()` in `dom.js`; never emoji in the
  interface): 16px in text and small buttons, 18px in buttons and list rows,
  20px in headers, 24px on the rail and on files.

## Components

### Buttons

| Kind | Class | Looks | Use |
| --- | --- | --- | --- |
| Primary | `.btn.primary` | Accent fill | The one main action of a dialog, form or screen (Save, Create, Join) |
| Secondary | `.btn` | Neutral fill with a border | Everything else |
| Danger (commit) | `.btn.danger` | Red fill | Confirms something destructive: the confirmation dialog's button, Leave, Ban |
| Danger (start) | `.btn.small.danger` | Red outline, red text | A destructive action in a list (Delete, Kick, Revoke) that opens a confirmation |
| Link | `.btn.link` | Text in the link colour | A way back or out (Back, Cancel inline, Remove avatar) |
| Icon | `.icon-btn` | A 32px square, no fill until hovered | Toolbars, headers, rows; named by its `title` |
| Split | `.split` | An icon button and a narrow arrow (`.split-more`) joined, one hover fill | A control with options of its own: the voice bar's mute, deafen, camera and screen. The icon acts; the arrow (or a right-click on the icon) opens its menu |

- Sizes: default 36px; `.small` 28px for buttons inside rows, tables, banners
  and notices; `.block` is full width (the screens before unlock).
- A toggle shows its state with `aria-pressed` (or `.on` for an icon button):
  pressed is the soft accent fill with accent text, for icon and labelled
  buttons alike.
- Order: Cancel then the primary, right-aligned. A destructive action that
  isn't the confirmation (Delete conversation next to Close) sits apart on the
  left of the footer.
- Disabled is half opacity with a not-allowed cursor; nothing else greys out.

### Fields

- Inputs, selects and text areas: 36px, `--field-bg`, a 1px `--field-border`
  at 3:1 or better, `--r-sm`; hover darkens the border; focus is the focus
  colour plus a 1px ring.
- A stacked field's label (`field()`) is an overline above it: small capitals,
  bold, `--fg2`, never styled like an input. A group of checkboxes (Actions,
  Permissions, Exempt roles) carries the same label (`.group-label`). Hints go
  under the field in
  `--muted`; an error goes under that in `--danger-text`, as text that can be
  selected.
- Fields side by side in a `.row` share the width and line up at their
  labels.
- The message box is one component: the box shows hover and focus; the text
  area inside draws no ring of its own.

### Choices

- **Switch**: a setting that saves at once (every checkbox in a
  `settingRow`). On is the accent.
- **Checkbox / radio**: a choice that is sent with a form (Features, Create
  invite, role permissions), 16px, the accent.
- **Segmented control** (`.theme-switch`, `.tri`): one of a few values side
  by side: the selected segment is raised (or red / green for deny / allow).

### Sections and rows

- A **form section** (`formSection()`) is a box: `--section-bg`, a border,
  `--r-md`, optional title, its rows or fields, and, if it saves, a **footer**
  (`formFoot()`): a divider, a tinted strip, any error on the left and the
  actions on the right. Sections are 24px apart, so a Save button plainly
  belongs to the fields above it.
- Every section of a page (user Settings and Community Admin, see
  **Pages**) is made of: the section title, an optional **intro**
  (`sectionIntro()`), an optional **toolbar** (`toolbar()`, the primary
  action first), and boxes. Lists of things (roles, members, channels,
  reports) are rows in a box.
- A **setting row** has what it is (and a description in `--muted`) on the
  left and its control on the right, with a line between rows.
- **Tables** (`table.list`) are boxes too: overline headings on the footer
  tint, lines between rows.
- **Cards** (`.card`) are small boxes of related content in a flow;
  **notices** (`.notice`) are boxed notes with a coloured edge (warn by
  default); **banners** (`.banner`) are strips across the top of a view with
  a coloured edge and small buttons.

### Lists and navigation

- List rows (channels, DMs, members, settings sections, menu rows) share one
  behaviour: muted text at rest; `--hover` fill and full text under the
  pointer; `--sel` fill and full text when selected; bold with a dot when
  unread.
- The rail marks the open community (and DMs) with a tall bar on the left, an
  unread one with a short one; the selected and hovered icon becomes a rounded
  square in the accent.
- Group headings (channel categories, member groups, settings groups, menu
  groups, table heads) are overlines: small capitals in `--muted`.
- Tabs are underlined in the accent when selected.

### Pages

A **page** is a place of its own that fills the centre of the window, in
place of the channel or conversation and the member list: user Settings and
Community Admin (and any screen too big for a dialog). The rail and the
channel list stay on its left, with the voice bar and the user panel; a
call goes on underneath. Build one with `showPage()` in `dom.js`:

```js
showPage({
  title,          // the page's heading (h1) in the bar, and the window's title
  context,        // optional: what it's about, after the title (a community's name)
  icon,           // optional: an icon before the title
  nav,            // optional: [{ id, label, group, render(el, show), badge() }]
  initial,        // optional: the section to open on
  render,         // without nav: render(el) fills the page
  onClose,        // runs once when the page closes, however it closes
  cls,            // optional: classes for the page element
}) // → { el, close(), show(id), current(), refreshBadges(), open() }
```

- **The bar** at the top is the column's 48px bar (`.main-head`): the icon,
  the title, the context after a line, and **Close** (a small secondary
  button with an X, "Close (Esc)") on the right.
- **Sections** (`nav`) are list rows down the left of the page (208px, on
  `--bg`, a line on their right), under overline group headings, the open
  one selected (`aria-current="page"`), with the moderator badge where
  something waits (`badge()`; a group heading shows its sections' total).
  The open section is beside them: its title (h2, 1.375rem), then the
  section's intro, toolbar and boxes, at most 760px wide, scrolling on its
  own. The window's title is the section, the page and its context.
- **Getting back**: **Close**, Escape (when no dialog or menu is open over
  the page; those close first) and anything that goes somewhere else: a
  channel, a community, Direct messages, joining a call's stage, a link to a
  message. Close and Escape put back the view underneath and give the
  focus back to what opened the page; opening a page moves the focus to its
  title. Code that shows another view itself calls `leavePage()` first.
- One page at a time: showing a page replaces the open one. While a page is
  open the channel list marks nothing as open, and the member list (or a
  voice channel's chat) is hidden.
- A page isn't modal: it doesn't trap the focus. What needs an answer
  before anything else (a confirmation, a small form such as Create role or
  Edit channel) stays a dialog, on top of the page.

### Menus, dialogs, toasts

- **Menus**: `--bg2`, a border, `--r-md`, 6px padding, 32px rows. The row
  under the pointer or keyboard takes the `--sel` fill. Destructive entries are
  red text with a soft red fill; Community Admin is red bold text, a place to
  go rather than an action. Separators are lines; groups have an overline.
  A person has one menu wherever they're shown (`userMenu()` in
  `people.js`): reaching them first, then a line, then what you may do to
  them; a right-click opens it, or the Menu key on their focused name.
  A choice in a group (a device, an input mode) and an on/off entry (Mute
  for me, Share sound) show a tick when chosen or on. Sliders (a volume, a
  sensitivity) go at the top. A menu opened from the bottom of the window
  (the voice bar) opens above its button.
- **Dialogs**: `--bg2`, `--r-lg`, title 1.375rem, 24px padding, the same
  footer as a form section. Escape and the backdrop close a dialog unless it
  must be answered. A dialog is for a question or a small form; a place to
  work in (settings, administration) is a page.
- **Toasts**: raised boxes at the bottom right with a coloured edge. Info
  toasts go by themselves (a click dismisses one); an error stays until its
  close button is pressed, and its text can be selected and copied. New
  messages are never toasts: they are badges.

### Badges and pills

- **Count badges** (red, round): unread mentions and unread DMs.
- **Moderator badge** (red, square): what waits for review, on the rail, the
  community menu, Community Admin's navigation and its sections.
- **Pills** label a state (owner, active, request, revoked) in a translucent
  neutral, green, amber or red.
- **Live badge** (red fill, white small capitals, square): someone is
  sharing their screen, next to their name in the channel list and on the
  screen's block on the call stage. Red because it asks for attention; the
  fill keeps 4.5:1 on any background, video included.

### The call stage

- One block per person (their avatar, or their camera in its place) and
  one per shared screen. Someone else's video is a cover (avatar or a
  monitor, and "Click to watch") until clicked: the whole block is its
  button. A watched video can be brought to the front (large, the others
  in a strip below) and put back with a second click or Escape.
- A block's own controls (Stop watching; a stream's mute and volume) sit at
  its top right on a dark strip, above the block's button. Names sit at the
  bottom left on a dark strip.
- No row of controls under the stage: the call is run from the voice bar.

- **Status dots** (a 12px dot on an avatar's lower right, ringed in the
  surface colour) show a person's status: `--ok` online, `--warn` idle,
  `--danger` do not disturb, `--muted` offline, and a `--muted` ring for
  your own Invisible. Colour is never the only sign: the row's tooltip and
  its screen-reader description say the status in words, and custom text
  shows as a muted line under the name (`status.js`).

### Empty states

- A view with nothing to show: centred heading, one line of why, and the way
  forward as a primary button (`.empty`).
- A list with nothing in it: one muted line in a dashed box where the rows
  would be (`emptyNote()`).

### Focus

- Every focusable element shows a 2px ring in `--focus` on keyboard focus.
  Inside lists, rows and icon buttons the ring is drawn inside, so neighbours
  never hide it. Controls that appear on hover (message actions, a voice
  member's options) also appear on keyboard focus.

## Telegraphing

- **Clickable** things have a pointer cursor and a visible change on hover:
  buttons change fill, rows and icon buttons take the hover fill, links
  underline. Text that isn't clickable never changes on hover.
- **Selected** things have a fill and full-strength text (rows), a bar on the
  rail, an underline (tabs), or a raised segment; never colour alone.
- **On** (a toggle) is the accent: a switch, or the soft accent fill on a
  button.
- **Red** means destructive or attention. Filled red commits; outlined red
  starts a destructive action; red text in a menu is destructive, except
  Community Admin (bold), which leads to administration. Red badges count
  what's new or waiting.
- **Disabled** is half opacity and a not-allowed cursor; the reason, when
  there is one, is in the control's title.
- **Actions sit with what they affect**: a section's Save in its own footer, a
  row's actions on the row, a message's actions on the message.
