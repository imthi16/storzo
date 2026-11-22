---
name: Storzo Console
description: Dark data-center console for self-hosted S3 storage; capacity and health read like a rack front panel, with one green signal color.
colors:
  bg: "#070d0b"
  side: "#0a110e"
  well: "#050a08"
  rack: "#0d1512"
  rack-2: "#121c18"
  rack-3: "#18241f"
  line: "#1c2a24"
  line-2: "#2b3d35"
  text: "#dfeae4"
  text-2: "#9bb0a5"
  text-3: "#7a9186"
  label: "#3ddc84"
  label-ink: "#04140a"
  label-hover: "#63e79a"
  data: "#4cc9b0"
  ok: "#3ddc84"
  warn: "#f0a63a"
  fail: "#ef5b5b"
  danger: "#b93f44"
typography:
  display:
    fontFamily: "Geist Variable, system-ui, -apple-system, Segoe UI, Roboto, sans-serif"
    fontSize: "24px"
    fontWeight: 600
    lineHeight: 1.2
    letterSpacing: "-0.02em"
  title:
    fontFamily: "Geist Variable, system-ui, sans-serif"
    fontSize: "13px"
    fontWeight: 600
  body:
    fontFamily: "Geist Variable, system-ui, sans-serif"
    fontSize: "13px"
    fontWeight: 500
  telemetry:
    fontFamily: "JetBrains Mono Variable, ui-monospace, SF Mono, Menlo, Consolas, monospace"
    fontSize: "26px"
    fontWeight: 500
    letterSpacing: "-0.02em"
  label-mono:
    fontFamily: "JetBrains Mono Variable, ui-monospace, monospace"
    fontSize: "12px"
    fontWeight: 400
rounded:
  xs: "2px"
  sm: "3px"
  md: "4px"
spacing:
  xs: "6px"
  sm: "12px"
  md: "16px"
  lg: "18px"
  xl: "28px"
components:
  button-primary:
    backgroundColor: "{colors.label}"
    textColor: "{colors.label-ink}"
    rounded: "{rounded.sm}"
    padding: "10px 14px"
  button-primary-hover:
    backgroundColor: "{colors.label-hover}"
  button-secondary:
    backgroundColor: "transparent"
    textColor: "{colors.text}"
    rounded: "{rounded.sm}"
    padding: "10px 14px"
  button-danger:
    backgroundColor: "{colors.danger}"
    textColor: "#ffffff"
    rounded: "{rounded.sm}"
    padding: "10px 14px"
  panel:
    backgroundColor: "{colors.rack}"
    textColor: "{colors.text}"
    rounded: "{rounded.md}"
    padding: "16px 18px"
  nav-item-active:
    backgroundColor: "{colors.rack-3}"
    textColor: "{colors.text}"
    rounded: "{rounded.sm}"
    padding: "10px 10px 10px 18px"
  chip-active:
    textColor: "{colors.label}"
    rounded: "{rounded.sm}"
    padding: "6px 12px"
---

# Design System: Storzo Console

This file describes what the shipped console (`frontend/src/style.css` and the page-scoped `activity.css`, `overview.css`, `credentials.css`, `a11y.css`) actually uses. It replaces an earlier draft that specified a yellow accent and a blue-black ground; the product ships a **green** accent, so green is now the source of truth. When the CSS and this file disagree, fix the file or the CSS in the same change.

## Overview

**Creative north star: the rack front panel.** A near-black green ground carries "rack" panels separated by hairlines. Capacity and health read the way they do on hardware: segmented bar graphs and status LEDs, ruled tables, tabular monospaced numbers. Dark only (`color-scheme: dark`), because the product is an operator tool used next to terminals and dashboards in a data center context.

Density is high and flat. Depth comes from tonal steps (`bg`, `rack`, `rack-2`, `rack-3`) and 1px lines; there are no drop shadows. Color is scarce and assigned by job: **green** is the one primary action and the current-selection marker, **teal** carries data, **green/amber/red** state only.

The product does not claim production readiness (see `PRODUCT.md`), and the UI follows it: open items are shown as open, not as checked.

## Colors

- **bg** `#070d0b`: page ground. **side** `#0a110e`: sidebar. **well** `#050a08`: inset wells.
- **rack** `#0d1512`: panels and the login card. **rack-2** `#121c18`: hover rows. **rack-3** `#18241f`: raised and active surfaces, toasts.
- **line** `#1c2a24` and **line-2** `#2b3d35`: hairlines and control borders.
- **text** `#dfeae4`, **text-2** `#9bb0a5`, **text-3** `#7a9186`: primary, secondary and tertiary text. `text-3` on `rack` is about 5:1.
- **label** `#3ddc84` with ink `#04140a`: the single accent. Primary button, active nav marker, active filter chip, focus ring, caret. Hover `#63e79a`.
- **data** `#4cc9b0`: bars, links to data, the image category.
- **ok / warn / fail** `#3ddc84` / `#f0a63a` / `#ef5b5b`: status only (LEDs, allow and deny chips, health). `danger` `#b93f44` is the destructive button fill.
- **File-type categories** (`frontend/src/constants.ts`): image `#32a188`, video `#2d6ca8`, audio `#25a4b1`, document `#5d60a9`, archive `#619d64`, other `#7d549a`. Cool hues only; nothing red or amber, because those mean status. They pass the dataviz palette checks for **adjacent** categories on `rack` (distance and 3:1 contrast), not for every pair, so the legend text and counts always carry identity too.

**Rules:** green is for the primary action and the "you are here" marker, not decoration. Never use ok/warn/fail for a category. Never color alone: every status chip carries its word (allow, deny, connected).

## Typography

- **Geist Variable** for UI text: page title 24px/600 with -0.02em, panel titles and buttons 13px/600, body 13px/500.
- **JetBrains Mono Variable** for anything that is data: telemetry numbers (26px), keys, bucket names, byte counts, timestamps (`2026-11-15 17:48:38`, 24-hour, local time), chip text.
- Floor: nothing under 12px. Numbers are tabular. Long mono strings truncate with an ellipsis on one line.

## Layout

A fixed sidebar (navigation, storage-connected LED, signed-in user) and a content column with a breadcrumb bar. Pages are stacks of panels in a 12-column feel: the Overview puts four telemetry cells in one ruled strip, then paired panels; Buckets is a two-pane list and object table; Activity is one ruled list with a source filter. Responsive behavior is structural: below 820px the sidebar becomes a drawer behind a menu button and grids collapse to one column; below 520px rows stack their metadata under the title (the Activity timestamp moves below the event). No horizontal page scroll at 320px.

Navigation: Overview, Buckets & objects, Activity log, Credentials, Settings. The app routes by `location.hash`, so a plain `#anchor` link changes the page; the "Skip to content" link focuses `<main>` in script instead.

## Elevation & Depth

None by shadow. Layers are `bg` < `rack` < `rack-2` < `rack-3`, each separated by a 1px `line`. Dialogs sit on a 82%-black backdrop; the toast is `rack-3` with a `line-2` border.

## Shapes

Small, hard radii: 2px for marks, 3px for buttons, chips and inputs, 4px for panels. Status LEDs are 8px circles. Segmented bars are square segments with 2px gaps.

## Components

- **Primary button:** `label` fill, `label-ink` text, 3px radius, 10px 14px padding, 13px/600. One per view region.
- **Secondary:** transparent, `line-2` border, hover `rack-3`. **Danger:** `danger` fill, white text, used only for delete.
- **Panel:** `rack` fill, `line` border, 4px radius, 16px 18px padding, a 13px/600 title with an icon at the right.
- **Telemetry cell:** label (13px, `text-2`), big mono number, one-line caption, separated from neighbours by 1px hairlines.
- **Segmented bar:** the capacity and file-type graphs. A `role="img"` with a text summary in `aria-label`; the legend repeats the values.
- **Chip (filter):** outlined; active is `label` text on a 10% `label` tint with a `label` border. Used for the Activity source filter.
- **Gateway row (Activity):** title (`S3 read`, `S3 write`, `S3 delete`), method chip, allow or deny chip (`ok` or `fail`, text included), HTTP status chip, then `bucket / key`; timestamp at the right (below the title on narrow screens). The accessible name spells out method, allowed or denied, status and path.
- **Status LED:** 8px dot, green online, red offline, amber while unknown; always next to a word.
- **Settings "Before production":** open items shown as empty circles in a neutral tone with the note that they are open, not completed. Never a green check for something that is not done.
- **Focus:** visible `:focus-visible` ring in `label`; hit areas at least 24px (32px up to 520px wide).
- **Credentials (`credentials.css`):** one issue panel (bucket, prefix that must end in `/` with the error in text, ro/rw radios, TTL radios). A preset past the server maximum (1 hour) stays visible, disabled, and says why. The one-time secret sits in a well with a `warn` border and the words "shown once"; copy buttons do not write the secret to localStorage, the URL, or logs. Env and MCP blocks are mono. Issued rows show the words Active, Expired, or Revoked next to the color. Revoke uses the danger button and the shared confirm dialog. Below 820px the form is one column; below 520px secret rows and the issued list wrap.

## Do's and Don'ts

**Do**
- Keep one green accent and use it for the primary action and the current location only.
- Show a status with its word and an icon, not only a color.
- Use mono for data, sans for prose; keep numbers tabular.
- Show real empty states in one sentence (for example "No uploads in the last 14 days") instead of a flat empty chart.
- Say what a number counts (the upload chart counts console uploads plus successful S3 `PUT` requests through the gateway).

**Don't**
- Don't add shadows, gradients, glows or glass panels.
- Don't use red, amber or green for a data category.
- Don't claim production readiness, a finished check, or S3 parity the project has not shown.
- Don't make everything a card; panels are ruled regions, not floating tiles.
- Don't use a hash anchor for in-page jumps (it changes the route).
