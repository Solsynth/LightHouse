# SnFS console design system

The console in `web/` is the web client for the Solar Network File System:
file explorer, storage node inspector, file previewer, command palette, and the
embeddable file picker.

This is the working reference for anyone adding or styling screens in SnFS. It
documents the design decisions encoded in `web/src/assets/main.css`,
`web/src/locales/`, and the shared components, aligned with the Solar Network
console design system.

## 1. Principles

1. **A calm, precise workspace.** The interface reports file systems, storage
   metrics, and transfers with instrument-level clarity. No hero metrics or
   decorative chrome that does not convey data.
2. **One signal color.** Periwinkle (`primary`) marks the active action,
   selected file row, or current filter. Semantic colors appear only when state
   demands them; brass (`accent`) marks privilege and quotas.
3. **Machine data and human explanation are typeset differently.** IBM Plex Mono
   is used for file names, paths, hashes, sizes, and machine coordinates. IBM
   Plex Sans is reserved for the human voice — guidance, titles, hints, and
   dialog descriptions.
4. **Blocks, not rules.** Content areas are grouped by subtle surface tone and
   spacing. Thin hairlines exist only where structure is encoded: table headers,
   ledger rows, and modal dialog sections.
5. **Shared geometry with the fleet.** Corner radii follow the tightened
   desktop scale:
   - Mark: 4 px
   - Chip / status / skeleton: 6 px
   - Button / segmented control: 6 px
   - Field / input: 10 px
   - Card / panel / well: 12 px
   - Sheet / modal / dialog: 16 px
6. **A field is one rounded outline with an inset caption.** Every control is
   a filled, fully rounded shape drawn by a 1 px border on all four sides with
   an 8–10 px radius (`--radius-input`). The caption rises into a compact label
   inside the top of the box on focus or value, keeping the outer border clean
   and unbroken. Focus thickens that outline to 2 px in the signal colour, and
   an error tints it in the error tone. Suffix icons or actions can sit on the
   trailing edge.

## 2. Color

daisyUI themes are declared in `web/src/assets/main.css`:

| Theme | Selector | Scheme | Ground |
| --- | --- | --- | --- |
| `snfs` (default) | `data-theme="snfs"` | dark | Ink-blue |
| `snfs-light` | `data-theme="snfs-light"` | light | Cool paper |

### Surfaces and text

| Role | Token | Dark | Light |
| --- | --- | --- | --- |
| Page ground | `--color-base-100` | `#0a0d16` | `#f3f5fa` |
| Panel / container | `--color-base-200` | `#10141f` | `#eaeef6` |
| Raised surface | `--color-base-300` | `#171c2b` | `#dee3ee` |
| Text content | `--color-base-content` | `#ccd2e2` | `#1c2334` |
| Hairline | `--console-line` | `#232a3c` | `#ccd3e1` |
| Soft hairline | `--console-line-soft` | `#1a2030` | `#dee3ed` |
| Secondary text | `--console-dim` | `#9aa3b8` | `#4d5770` |
| Faint text | `--console-faint` | `#7d86a0` | `#646e86` |

### Signal and state

| Role | Dark | Light | Use |
| --- | --- | --- | --- |
| `primary` | `#7577ba` | `#54589f` | Primary action, focus ring, active row |
| `accent` | `#c7b08a` | `#8a6a3b` | Quota allocation, elevated session |
| `success` | `#8db39a` | `#3d6d4f` | Upload complete, healthy node |
| `warning` | `#c7b08a` | `#8a6a3b` | Storage warning, expiring token |
| `error` | `#c08579` | `#a4554a` | Transfer failure, offline node |
| `info` | `#8fa6c9` | `#3f678c` | Informational status |

## 3. Typography

| Face | Weights | Role |
| --- | --- | --- |
| IBM Plex Mono | 400, 500, 600 | File listings, sizes, hashes, machine paths, metadata |
| IBM Plex Sans | 400, 500, 600 | Titles, descriptions, dialogs, guidance, empty states |

- Default body is IBM Plex Mono at 13 px with tabular numerals (`tnum`).
- Guidance, dialog descriptions, and empty state notes use the `.voice` utility (IBM Plex Sans 13.5 px).
- Page headings use `.title` (IBM Plex Sans medium tracking).
- Section labels use `.eyebrow` (uppercase 11 px, tracking 0.18em, faint text).

## 4. Geometry and Corner Radii

| Token | Value | Tailwind Class | Use |
| --- | --- | --- | --- |
| `--radius-mark` | 4 px | `rounded-mark` | Brand marks, file glyph icons |
| `--radius-chip` | 6 px | `rounded-chip` | Status badges, mime tags, skeletons |
| `--radius-control` | 6 px | `rounded-control` | Buttons, segmented views, toolbar toggles |
| `--radius-input` | 10 px | `rounded-input` / `--radius-field` | Inputs, selects, textareas |
| `--radius-card` | 12 px | `rounded-card` / `--radius-box` | Panels, menus, inspector drawers |
| `--radius-sheet` | 16 px | `rounded-sheet` | Modal dialogs, picker frame host |

## 5. Forms and Inputs

All text input fields share the `TextField` component (`src/components/TextField.vue`):

- **Clean Full Outline**: 1 px border with 10 px corner radius (`--radius-input`) on all four sides.
- **Inset Floating Label**: At rest, the label sits vertically centered on the value line. When focused or holding a value, it rises inside the top of the box.
- **Generous Height**: Comfortable heights (48 px default `sm`, 52 px `md`, 36 px `xs`) ensuring value text is never cramped beneath the risen label.
- **Signal Focus**: Focus thickens the outline to 2 px in the signal color via an inset ring so layout never jumps.
- **Suffix Icons & Actions**: Optional trailing icon component (`suffixIcon` prop) or trailing slots (`#suffix`, `#action`) for passkeys, password reveal, copy buttons, or clear actions. Fields without a suffix icon maintain balanced padding.
- **Validation**: `invalid` turns the outline and caption to the error tone with helper text in `hint`.

### Props

| Prop | Meaning |
| --- | --- |
| `label` | Visible caption; omit for bare controls |
| `as` | `'input'` (default), `'textarea'`, `'select'` |
| `size` | `'md'` (52 px), `'sm'` (48 px, default), `'xs'` (36 px) |
| `bare` | Drops caption and reserved caption line; name with `aria-label` |
| `invalid` | Error outline and caption tint |
| `hint` | Optional helper text below the field |
| `hintTone` | `'warning'` tints helper text without setting invalid |
| `suffixIcon` | Optional trailing icon component |
| `controlClass` | Classes for inner control element (e.g. `font-mono`) |
| `rootClass` | Classes for root wrapper (e.g. layout margins) |

## 6. Embeddable Picker Frame

The SnFS file picker (`/picker`) adheres to the same dark/light themes and geometry. When embedded across origins, it handles tokens via the `snfs-picker` window message protocol and adapts seamlessly to `auto`, `dark`, or `light` theme options.
