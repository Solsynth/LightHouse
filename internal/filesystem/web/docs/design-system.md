# SnFS console design system

The console in `web/` is the web client for the Solar Network File System:
file explorer, storage inspector, file previewer, command palette, and the
embeddable file picker.

This is the working reference for anyone adding or styling screens in SnFS. It
documents the design decisions encoded in `web/src/assets/main.css`,
`web/src/locales/`, and the shared components. It follows the Solar Network
console (`internal/stargate/web`) so the two surfaces read as one product, and
borrows the information architecture of Google Drive: a location rail, a
search line, a browsable object list, and a details column.

## 1. Principles

1. **Chrome is islands.** The header, rail, footer, and dialogs are sheets of
   paper on the page ground; the working surface between them is the ground
   itself. Separation comes from the gap between sheets and the tone of a
   block — never from a border, divider, or rule.
2. **Blocks, not lines.** Lists are bars with their own corner and a 2 px
   gutter. Groups are wells. A selected row is a tinted block. No horizontal
   or vertical hairlines are used to separate content.
3. **One brand signal.** Periwinkle-indigo (`primary`) is the only decorative
   colour. It marks the active location, the selected object, focus, and the
   primary action. Surface variants (`base-100`, `base-200`, `base-300`,
   `tint`) carry all structure. Semantic tones appear only where
   state demands them: an error, a completed transfer, a warning.
4. **A calm, precise workspace.** The interface reports file systems, storage
   metrics, and transfers with instrument-level clarity. No hero metrics or
   decorative chrome that does not convey data.
5. **People read names, machines print data.** Nunito is the interface and
   voice. IBM Plex Mono is reserved for machine-printed strings: paths, hashes,
   MIME types, storage keys, share links, and source text. Figures are set with
   tabular numerals where they are compared in a column.
6. **Shared geometry with the fleet.** Corner radii follow the Solar Network
   scale:
   - Mark: 8 px
   - Chip / status / well: 12 px
   - Button / segmented control / field: 16 px
   - Card / island: 20 px
   - Sheet / dialog / picker frame: 28 px

## 2. Layout

The console is chrome islands around a flat working surface:

```
┌─────────────────────────────────────────────────────────────────┐
│  header island: lockup · search · actions                       │
├───────────────────┬─────────────────────────────────────────────┤
│  rail island      │  content surface (the page ground)          │
│  locations        │  toolbar (crumbs · sort · filter · view)    │
│  pools            │  object bars / grid cards / columns         │
│  ┌─────────────┐  │                                             │
│  │ quota well  │  │                                             │
│  └─────────────┘  │                                             │
├───────────────────┴─────────────────────────────────────────────┤
│  status + language island                                       │
└─────────────────────────────────────────────────────────────────┘
```

- The gutter between islands is 8 px on small screens and 10 px above.
- The header, rail, and footer are `base-200` sheets. The content area is the
  page ground: toolbars and object views sit directly on it, and rows, cards,
  and wells carry the tonal blocks.
- Content is left-aligned. The rail, toolbar, and status line share one
  horizontal rhythm; object names lead, metadata trails.
- Below `md` the rail becomes a rounded drawer over a dimmed ground, and the
  header collapses to controls only.

## 3. Color

daisyUI themes are declared in `web/src/assets/main.css`. The ramp is the
Solar Network neutral ramp — a near-neutral lavender-gray, not blue-gray and
not warm — with indigo as the single brand signal.

| Theme | Selector | Scheme | Ground |
| --- | --- | --- | --- |
| `snfs` (default) | `data-theme="snfs"` | dark | `#171618` |
| `snfs-light` | `data-theme="snfs-light"` | light | `#faf7f9` |

### Surfaces and text

| Role | Token | Dark | Light |
| --- | --- | --- | --- |
| Page ground | `--color-base-100` | `#171618` | `#faf7f9` |
| Chrome sheet (header, rail, footer) | `--color-base-200` | `#1d1d20` | `#f6f2f6` |
| Row / well step | `--color-base-300` | `#2e2e31` | `#e4e1e5` |
| Brand-tinted block | `--surface-tint` (`bg-tint`) | `#262833` | `#e8e9f5` |
| Text content | `--color-base-content` | `#edeaee` | `#1f1f22` |
| Soft line (focus, scrollbar) | `--console-line` | `#4b4b4f` | `#d3d1d5` |
| Secondary text | `--console-dim` | `#c8c5ca` | `#4b4b4f` |
| Faint text | `--console-faint` | `#97959b` | `#636367` |

### Signal and state

| Role | Dark | Light | Use |
| --- | --- | --- | --- |
| `primary` | `#bac3ff` | `#515b92` | Active location, selection, focus, primary action |
| `success` | `#a8d1a8` | `#315936` | Upload complete, healthy node |
| `warning` | `#fdba55` | `#6f4800` | Quota pressure, expiring token |
| `error` | `#ffb4ab` | `#9f352d` | Transfer failure, offline node, destructive action |
| `info` | `#a1cce8` | `#2d5f79` | Informational status |

Semantic colours are used as tints (`bg-error/12`, `bg-success/12`) or as text
inside a tinted chip — never as a second decorative palette. No hue outside
this list is spent on files, folders, or regions.

## 4. Typography

| Face | Weights | Role |
| --- | --- | --- |
| Nunito | 400, 600, 700 | Interface, object names, headings, guidance |
| IBM Plex Mono | 400, 500, 600 | Paths, hashes, MIME, storage keys, share links, source |

- Body is Nunito at 13.5 px with 1.55 leading.
- Guidance, dialog descriptions, and empty state notes use `.voice`.
- Page headings use `.title` (Nunito bold, −0.01em tracking).
- Section labels use `.eyebrow` (uppercase 11 px, tracking 0.1em, faint).
- Machine strings opt in with `font-mono`; they are never the body default.

## 5. Surfaces and utility classes

| Class | Meaning |
| --- | --- |
| `.island-quiet` | Chrome sheet (header, rail, footer): `rounded-card bg-base-200` |
| `.ledger` | Row-bar table: 2 px border spacing, tinted cells, no rules |
| `.seg` / `.seg-item` | Segmented control; the pressed item takes `base-300` |
| `.menu-trigger` / `.menu-choice` | Toolbar dropdown trigger and its menu rows |
| `.chip` | Inline token for tags, filters, counts |
| `.note` | Tinted status block (`note-error`, `note-success`, `note-warning`) |

Rules of thumb:

- On the page ground, rows and cards are `base-200`; nested wells step to
  `base-300` or `base-100`. Inside a `base-200` chrome sheet, nested rows step
  to `base-300`.
- Hover steps one tone up; selection mixes `primary` at 12–18 % into the
  surface, so selection never introduces a second hue.
- Sticky table headers use the ground's solid fill (`bg-base-100`) so rows
  scroll underneath without a shadow or rule.

## 6. Forms and controls

All text fields share the `TextField` component (`src/components/TextField.vue`)
and the raw `.input` skin for one-off chrome:

- **Tonal well, not an outline.** The fill defines the field at rest; hover
  steps it up; focus is an inset 2 px ring in the signal colour so the layout
  never moves.
- **Inset floating label.** At rest the caption sits on the value line. On
  focus or with a value it rises inside the top of the box.
- **Generous height.** 52 px (`md`), 48 px (`sm`, default), 36 px (`xs`).
- **Suffix icons and actions** sit on the trailing edge without adding a border.
- **Validation** tints the inset ring and caption in the error tone; helper
  text sits below the well.

Buttons are tonal rectangles with no border at rest. A press sinks 1 px and
shrinks a hair. `btn-outline` is the soft variant: a tonal fill that steps up
under the pointer, not a hairline rectangle. Destructive actions use a tinted
error fill, not a red outline.

Checkboxes and switches are tonal controls: unchecked is `--field-fill`,
checked is the brand colour. Toggles are capsules (`rounded-full`), never
squared.

## 7. Object views

Four browsing surfaces share the same rules: bars or cards with their own
corner, no dividers, tonal selection.

| View | Composition |
| --- | --- |
| List | `.ledger`; row bars with a 2 px gutter; sticky tonal header |
| Grid | `base-200` cards; rounded media well; name and metadata below |
| Column | Miller columns of row blocks; each column lists both subfolders and files for the folder to its left, with no column-sheet background |
| Waterfall | Masonry of tonal cards, media ratio clamped between 0.35:1 and 4:1 |

Folders are set in the same face as files; kind and size are the only metadata
that appear by default. A public-by-link object gets a small brand glyph, not a
badge block.

## 8. Embeddable picker

The SnFS file picker (`/picker`) uses the same island vocabulary in a compact
frame:

- Header with brand mark, title, guidance, and a quiet close control.
- Breadcrumb and search as separate rounded tonal bands.
- Rows as tonal bars with a circular selection control on the leading edge.
- Footer as an island holding the selection summary and the two actions.
- Drop-to-upload is a tinted veil over the whole frame, not a dashed outline.
- The SDK overlay (`src/lib/picker/embed.ts`) draws a 28 px sheet on a dimmed,
  blurred ground, with the frame inset 6 px and a 20 px document corner. It
  handles `auto`, `dark`, and `light` theme options over the `snfs-picker`
  message protocol.

## 9. Motion

- One curve family: `ease-out` `cubic-bezier(0.33, 1, 0.68, 1)`; 180 ms is the
  default, 120 ms for tonal hover, 260 ms for arrivals.
- Motion only explains a change: a page arriving, a row settling, a transfer
  advancing, a menu opening. Nothing loops for decoration.
- All animation collapses under `prefers-reduced-motion: reduce`.

## 10. Accessibility

- Visible keyboard focus is a 2 px brand outline with a 2 px offset, or an
  inset ring inside fields.
- Selection is conveyed by `aria-selected` as well as tint; the tint is never
  the only signal.
- Text contrast follows the surface tables above; semantic text is placed on a
  tint of its own hue.
- Scroll regions keep the scrollbar gutter stable so rows do not shift.
