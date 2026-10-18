# Solarpass console design system

The console in `web/` is the account surface for Solar Network: sign-in,
recovery, profile, security, developer apps, activity, and administration. It
is served by Stargate itself, so the same document describes the deployment
and the development server.

This is the working reference for anyone adding a screen. It documents the
choices already encoded in `web/src/assets/main.css`,
`web/src/locales/`, and the shared components — not an aspirational style.

## 1. Principles

1. **A calm instrument, not a dashboard.** The interface reports state and
   gets out of the way. No hero metrics, no decorative gradients, no chrome
   that does not carry information.
2. **One signal color.** Periwinkle (`primary`) marks the action or current
   location. Semantic colors appear only when state needs them; brass
   (`accent`) marks privilege and secondary emphasis.
3. **Machine data and human explanation are typeset differently.** Monospace
   is for identifiers, values, tables, labels, and chrome. Sans is the voice:
   guidance, descriptions, and empty states.
4. **Blocks, not rules.** Content areas are separated by surface color and
   spacing. Horizontal hairlines survive only where they encode structure:
   the shell's chrome bands, ledger rows, table headers, modal sections, and
   the boxed tab strip.
5. **Boldness in one place.** The centered card, the blue-shifted palette, and
   the terminal typography carry the identity. Everything else stays quiet.

## 2. Color

daisyUI themes are declared in `web/src/assets/main.css`:

| Theme | Selector | Scheme |
| --- | --- | --- |
| `stargate` (default) | `data-theme="stargate"` | dark |
| `stargate-light` | `data-theme="stargate-light"` | light |

### Surfaces and text

| Role | Token | Dark | Light |
| --- | --- | --- | --- |
| Page | `--color-base-100` | `#0a0d16` | `#f3f5fa` |
| Panel | `--color-base-200` | `#10141f` | `#eaeef6` |
| Raised | `--color-base-300` | `#171c2b` | `#dee3ee` |
| Text | `--color-base-content` | `#ccd2e2` | `#1c2334` |
| Hairline | `--console-line` | `#232a3c` | `#ccd3e1` |
| Soft hairline | `--console-line-soft` | `#1a2030` | `#dee3ed` |
| Secondary text | `--console-dim` | `#9aa3b8` | `#4d5770` |
| Faint text | `--console-faint` | `#7d86a0` | `#646e86` |

`line`, `line-soft`, `faint`, and `dim` are exposed to Tailwind as
`border-line`, `border-line-soft`, `text-faint`, and `text-dim`. They resolve
through the active theme, so shared components never hardcode a theme color.

### Signal and state

| Role | Dark | Light | Use |
| --- | --- | --- | --- |
| `primary` | `#7577ba` | `#54589f` | primary action, current tab, focus ring |
| `accent` | `#c7b08a` | `#8a6a3b` | elevated sessions, trusted state |
| `success` | `#8db39a` | `#3d6d4f` | `OK` status, verified state |
| `warning` | `#c7b08a` | `#8a6a3b` | pending or expiring state |
| `error` | `#c08579` | `#a4554a` | destructive action, `ERROR` status |
| `info` | `#8fa6c9` | `#3f678c` | neutral live indicator |

Contrast (measured in the browser on the light theme): body `14.37:1`, dim
`6.61:1`, faint `4.68:1`, primary button text `6.05:1`. Faint is the floor for
small text; do not lighten it further.

Theme choice follows the OS on first visit, then persists in
`localStorage['solarpass.theme']`. The toggle lives in the header on every
page.

## 3. Typography

| Face | Weights | Role |
| --- | --- | --- |
| IBM Plex Mono | 400, 500 | interface chrome, labels, data, tables, buttons |
| IBM Plex Sans | 400, 500, 600 | page titles, prose (`.voice`), display headings |

| Token | Size | Use |
| --- | --- | --- |
| page title | 24 px (20 px on mobile) | one per route, `h1.title` |
| title | 13 px / 600 | modal and header names |
| body | 13 px mono | default |
| voice | 13.5 px sans | descriptions, hints, empty states |
| small | 12 px | dense prose in footers |
| `text-2xs` | 11 px | labels, chips, metadata, table headers |

Labels use uppercase, `0.14em` tracking, faint color. Table headers use
uppercase `10 px` with `0.14em` tracking. Numeric columns use
`tabular-nums`. Identifiers are truncated in dense tables and expose the full
value through `title`.

## 4. Layout

The shell is one centered card; the page has no separate sidebar or top-level
chrome beyond the card header.

```
┌─ viewport ───────────────────────────────────────────┐
│        ┌─ card (width by route) ──────────────────┐  │
│        │ brand · language · theme · identity      │  │
│        ├──────────────────────────────────────────┤  │
│        │ nav (signed in)                          │  │
│        ├──────────────────────────────────────────┤  │
│        │ page header (title, description, action) │  │
│        │                                          │  │
│        │ panels / forms / ledgers                 │  │
│        │                                          │  │
│        │ status bar (OK/ERROR + message)          │  │
│        └──────────────────────────────────────────┘  │
│        © 2026 Solar Network        [☾]  [Language ▾]  │
└──────────────────────────────────────────────────────┘
```

Width tiers are decided by route metadata in `web/src/router/index.ts`:

| Tier | Width | Routes |
| --- | --- | --- |
| flow | 460 px | sign-in, create account, recovery, captcha, provider callback, authorize, sudo |
| account | 880 px | profile, factors, passkeys, contacts, linked identities, security mode, public profiles |
| wide | 1120 px | sessions, developers, activity |
| full | viewport (full bleed) | all admin pages |

Admin routes set `meta.fullBleed`. The shell then drops its 16 px gutter and
card border so the admin surface reaches the viewport edges, while the header,
page padding, and footer keep their normal spacing.

Public profiles set `meta.edgeToEdge`: the shell keeps the card but drops the
page content padding, so the profile banner runs to the card's inner edges.
The profile view reapplies 20 px padding to the avatar and panels.

`/auth/sudo` renders the same factor flow as the elevation modal, so another
client can open it in a browser or popup, elevate the session, and return.
It accepts only same-origin `next` paths and posts `solarpass:sudo` to the
opener on success.

The card floats in a 16 px viewport gutter; content padding is 20 px, 28 px
from the `sm` breakpoint, and panel padding is 16 px. The 4 px spacing unit is
used throughout; prefer gaps over nested borders.

Public profiles (`/@name`) stay on the 880 px card and hide the nav tabs; the
profile background is a banner that runs to the card's inner edges above the
avatar, while the name and panels keep the normal 20 px inset. The site keeps
the card shell everywhere except admin pages.

When the signed-in account has a profile background, it is painted as a fixed
backdrop behind the shell: cover-scaled, blurred 64 px, at 30% opacity under a
70% base scrim. It is never fetched when the profile has no background, and a
failed load falls back to the plain base surface.

**Never nest a bordered card inside the page card.** Content sits directly on
the page surface; panels use `bg-base-200/30` to group a region, and the
status bar and table footers are separated by spacing, not rules.

## 5. Components

### Header

Brand mark is a CSS mask over `currentColor` (so it repaints with the theme),
then `SOLARPASS`, then `Solar Network` on wider screens. With a session, the
right side holds the 24 px account avatar (no border; falls back to a person
glyph when no picture exists) and the elevated indicator. The avatar is a
dropdown with **Public Profile** and **Sign Out**; it closes on select, Escape,
or an outside click. Signed out the header is just the brand.

### Footer

The footer sits outside the card, aligned to the card's current width, and
holds the copyright (`© {year} Solar Network`), the theme toggle, and the
language dropdown. Language is a native `<select>` styled as a compact inline
field: the catalog will grow beyond two entries, and a select scales without
redesigning the header. It is present on every page, signed in or not.

### Navigation

The main nav is a single horizontal line of 11 px links with a Carbon 16 icon.
The active section is `text-base-content`; the rest are `text-faint`. The
account-settings entry is labelled **Settings** with the sliders icon; its
contained tabs are Sessions, Sign-In Factors, Passkeys, Contacts, Linked
Identities, and Security Mode.

`SecurityNav` is a contained tab box: 1 px border, hairline dividers between
tabs, active tab filled with `base-300`, inactive tabs faint with a hover tint.
It uses `aria-current="page"`. Do not use underlined tabs.

### Public profile

`GET /@name` (SPA route `public-profile`) renders the public projection:
background banner, avatar, display name and handle, bio, public contacts,
links, and pronouns/location/time zone/join date.
The nav tabs are hidden; the page keeps the 880 px card, with the background
banner running to the card's inner edges and the avatar and panels inset by
20 px. A missing account gets the not-found header rather than a blank page.

### Page header

`PageHeader` renders the route title, an optional description in the voice
face, and an actions slot. It has no bottom rule; spacing separates it from
the content.

### ConsolePanel

`section` with `border border-line bg-base-200/30`, a title/note header, a
body, and an optional footer for pagination or notes. Header and footer have
no horizontal rules. Use it for every grouped region; do not re-implement its
shell inline.

### Ledger

`table.ledger` is the only table style: hairline row separators, a stronger
header rule, no zebra striping, hover tinting the row, right-aligned last
column. First and last cells carry 8 px outer padding by default. Wrap tables
in `.table-bleed` inside a panel to let row rules meet the panel edge; cell
text then keeps 16 px to match the panel header. Every table inside a
`ConsolePanel` is bled — a table whose rules stop short of the panel edge
reads as a nested card, which the layout does not allow.

### Loading

`LoadingSkeleton` stands in for content that has not resolved: the same row
height, column count and spacing as the loaded state, so the reader sees that
something is arriving and the real content lands where the placeholder was.

| Variant | Shape |
| --- | --- |
| `columns` (default) | table row: one bar per column, split by `:columns` ratios |
| `rows` | list row: title plus chips, then a description line |

Set `header` for tables so the column labels have a shorter bar. Bars are
`aria-hidden`, and the sweep is one opacity animation that the reduced-motion
rule already neutralizes. While a surface is loading, a count or page range is
not a fact: gate the panel `note` and the pager label on `!loading` rather than
printing `0 total` or `Page 1 of 1`.

### Forms

Labels sit above the field (`.field-label`, uppercase 11 px). Inputs, selects,
and textareas are full width, 40 px high, 2 px radius, `base-200` fill, hairline
border, and periwinkle focus outline. Placeholders are only for examples
(`name@example.com`) or format hints (`6-digit code`); do not repeat the label.

### Factors and passkeys

The factor picker offers only types that are not already configured; the API
also rejects a second factor of the same type. Passkeys are credentials rather
than a single factor: the Passkeys tab lists any number of them with add,
rename, and delete, while the passkey method's enable/disable toggle stays in
Sign-In Factors. A passkey can only be registered after the passkey factor and
a recovery code are enabled.

### Sign-in methods

The account field and its primary action come first. Every other way to sign
in — passkey, QR, and each social provider — sits under `Or continue with` in
a two-column tile grid of outline buttons, so nine ways cost the same vertical
space as four and none of them outranks another. A tile carries only a mark
and a name; the phone-only variants are named for the action, not the
machinery ("Scan to Sign In", not "QR login").

`ProviderMark` draws a provider's brand silhouette from
`web/src/lib/providers.ts` in `currentColor`, so it repaints with the theme and
never ships a second color. A provider with no mark in the catalog — including
one a newer server lists — falls back to its initial, so the grid never has a
blank tile. Brand names are not translated; only the sentence around them is.

The provider list comes from `GET /api/auth/providers`, which answers only the
providers whose credentials are configured and only when the social module is
mounted. The console therefore never offers a button that ends in an error.

### QR sign-in

The QR step replaces the form inside the same flow card. The code is rendered
locally from the server's `qr_data` payload, never through an image service:
the payload is a bearer credential and a third-party rasterizer would see it.
The tile stays white with dark modules in both themes because a camera reads
the code, not the theme.

While it waits, the panel reports one state at a time — waiting, scanned,
declined, expired — with a countdown beside it. Approval is the only state that
redeems the challenge, and the watcher stops before the exchange so a second
poll can never spend the same code twice.

### Linked identities

The Connections panel lists what is already attached. A second panel,
**Available Providers**, offers the rest: `POST`-free links to
`/api/auth/login/{provider}?flow=connect&returnUrl=…`, which the API reads as
"link this identity to the signed-in account" rather than "sign in". A linked
provider disappears from that panel, and the return trip lands on the same page
with the connection already listed and one `OK` line naming it.

### Buttons

| Class | Use |
| --- | --- |
| `btn btn-primary` | the single committing action on a surface |
| `btn btn-outline` | alternative flows (passkey, provider) |
| `btn btn-ghost` | secondary/cancel |
| `btn btn-error` | destructive confirmation |

All buttons are 2 px radius, mono, 12 px, no shadow. One primary action per
view.

### IconButton

`IconButton` is the standard row/list action: 26 × 24 px, Carbon 16 icon,
`text-dim`, hover to `base-content`, danger tone to `error`. It requires a
`label`, used for both `aria-label` and `title` (the native tooltip). Use it
for table rows, list items, and panel header refresh actions. Text actions are
reserved for forms and first-class choices.

### CopyButton

A compact chip with only an icon and a tooltip; it swaps to a checkmark after
copying. The accessible name includes what is being copied.

### Status bar

The footer of the shell, rendered after any notice: uppercase `OK` in
`text-success` or `ERROR` in `text-error`, followed by the message in `dim`.
Never lowercase the marker.

### Modals

`ConfirmAction` and `ElevationSheet` are the only surfaces that keep internal
horizontal rules: a header block, body, and an action footer. They are modal,
so their boundaries are functional.

## 6. Motion

Motion explains a change, never decorates:

| Motion | Duration | Where |
| --- | --- | --- |
| step swap | 160 ms, 6 px rise | sign-in identify ↔ challenge |
| row enter/leave | 160 ms | factor rows |
| notice | 160 ms | status bar |
| card height | 220 ms | shell region resize |

All of it is disabled under `prefers-reduced-motion: reduce`. Do not add
scroll-triggered animation or ambient movement.

## 7. Iconography

Carbon 16 icons only, inheriting `currentColor`. Icons are `aria-hidden` and
always paired with text or an `IconButton` label. New icons must be added to
`web/src/types/carbon-icons-vue.d.ts`, which types only the glyphs the console
imports.

## 8. Language and copy

Locales: English (`en`) and Simplified Chinese (`zh-CN`). Catalogs live in
`web/src/locales/<locale>/<area>.ts`; each module exports unique top-level
namespaces, merged by `import.meta.glob` in `web/src/lib/i18n.ts`. Return
the reader to the same screen with the same state when switching languages.

- The locale follows the browser on first visit, then persists in
  `localStorage['solarpass.locale']`. The footer dropdown is the switch.
- `html lang` and the document title update with the locale.
- Use `useI18n()` in components; `translate()` only outside setup.
- Formatters (`relative`, `remaining`, `compactNumber`, enum labels) are
  locale-aware. Do not format dates or numbers by hand.
- Literal `@`, `|`, and braces are message syntax in vue-i18n. Escape an email
  placeholder as `name{'@'}example.com`.

### Voice

Write from the reader's side of the screen. Name what a control does, keep the
same verb through the flow, and state outcomes plainly.

- Sentence case in English; full-width punctuation in Chinese sentences.
- No trailing punctuation on labels, buttons, or tabs.
- Errors say what happened and what to do next; they do not apologize.
- Empty states invite the next action.
- Do not expose implementation details in user copy. Say "signed out", not
  "cookies cleared"; "verification code", not "token"; "this browser", not
  "this session's JWT".
- Terminology: account 账号, sign in 登录, sign out 退出登录, session 会话,
  passkey 通行密钥, verification code 验证码, elevated 已提权.

## 9. Accessibility

- Visible `:focus-visible` outline in primary, 2 px offset.
- Icon-only controls carry both `aria-label` and `title`.
- Active tabs use `aria-current="page"`; modal dialogs use `aria-modal` and a
  labelled title.
- Tables use `scope="col"` and a screen-reader-only actions header.
- Body text is 13 px; the smallest text (11 px) is limited to labels and
  metadata and meets 4.5:1 on both themes.
- Hit targets are at least 24 px; row actions are spaced in a 26 px control.
- Layout works from 360 px wide; tables scroll horizontally rather than
  crushing columns.

## 10. Implementation map

| Concern | File |
| --- | --- |
| tokens, themes, base chrome | `web/src/assets/main.css` |
| theme/locale persistence | `web/src/lib/prefs.ts`, `web/src/stores/ui.ts` |
| i18n instance and helpers | `web/src/lib/i18n.ts` |
| message catalogs | `web/src/locales/{en,zh-CN}/*.ts` |
| date/number/enum presentation | `web/src/lib/format.ts`, `web/src/lib/enums.ts` |
| shell and status bar | `web/src/App.vue` |
| width tiers and titles | `web/src/router/index.ts` |
| shared surfaces | `web/src/components/` |

## 11. Adding a screen

1. Register the route with a `titleKey` and the right width tier (`narrow`,
   `wide`, or `full`; default is account width).
2. Build the page from `PageHeader` and `ConsolePanel`; do not add a nested
   bordered card.
3. Use the ledger for tabular data and `.table-bleed` inside panels.
4. Use `IconButton` with a label for row actions; use native `title` for
   tooltips.
5. Add copy to both locale modules with identical key shapes; run the
   formatters instead of hand-formatting values.
6. Give tables a periodic pager (icon buttons) when the API supports
   `take`/`offset`; show the page count and live total.
7. Run `make -C web check` (type check, lint, tests, production build).
