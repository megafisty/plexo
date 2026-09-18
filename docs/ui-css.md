# UI CSS composition

Where the client's styles live, what is source vs generated, and how they reach
the browser. Read this before touching `ui/` styling so you do not have to
re-derive the layering from the repo.

## At a glance

| File | Kind | Role |
| --- | --- | --- |
| `ui/index.html` | source | Links `base.css` then `themes.css`; that order is the whole cascade |
| `ui/base.scss` | source (entry) | `@use`s the three base inputs; compiles to `base.css` |
| `ui/vendor/open-props.min.css` | vendored | Open Props design tokens |
| `ui/vendor/open-props-normalize.min.css` | vendored | reset/normalize |
| `ui/plexo.css` | source | **All component classes + non-color primitives/tokens** |
| `ui/themes.scss` | source (entry) | `@use`s the two palettes; compiles to `themes.css` |
| `ui/theme/dark.css` | source | Dark palette (`:root` default) |
| `ui/theme/light.css` | source | Light palette (`:root[data-theme="light"]`) |
| `ui/base.css` | **generated, committed** | normalize + Open Props + `plexo.css`, inlined flat |
| `ui/themes.css` | **generated, committed** | dark + light palettes, inlined flat |

The two `.scss` files are thin entry points; the substance is the plain-CSS
`plexo.css` and `theme/*.css`. Editing a generated `.css` by hand is wrong —
regenerate instead.

## Build

`./ui/build.sh` runs `tsc` for TypeScript and Dart Sass for CSS:

```sh
sass --no-source-map --style=compressed base.scss base.css
sass --no-source-map --style=compressed themes.scss themes.css
```

`@use "…" as *` **inlines** each plain-CSS file, so the output is one flat sheet
with **no `@import` and no `@layer`** — the target runtime (QtWebKit) predates
cascade layers, and flat sheets avoid an extra request. Inputs loaded as `.css`
are treated as plain CSS, so `plexo.css`/`theme/*.css` must use flat selectors
(no Sass nesting or variables). Scaffolding/colors come from custom properties,
not Sass.

All output is committed so `go build` needs no JS/CSS toolchain on a fresh
checkout. `ui/embed.go` embeds `index.html base.css themes.css app vendor
sound`; at runtime `internal/web` prefers the on-disk `./ui` directory when it
exists, so a dev checkout can serve edits without rebuilding the binary.

## Cascade order

`base.css` (first):

1. `vendor/open-props.min.css` — scale tokens: `--size-*`, `--radius-*`,
   `--font-size-*`, `--font-lineheight-*`, `--font-weight-*`, `--ease-*`.
2. `vendor/open-props-normalize.min.css` — reset.
3. `plexo.css`:
   - `:root` — Plexo's non-color primitives (`--plexo-font`,
     `--plexo-radius-sm/lg`, `--plexo-focus-ring`) and **overrides of the Open
     Props `--font-size-*` tokens** (the app's type scale is retuned in this one
     block).
   - `* { box-sizing }`, `body`, `:focus-visible`.
   - Component classes in roughly document order: layout helpers, card, form,
     chatspace, conversation sidebar/pane, message list, composer, roster,
     dialogs, buttons/fields, settings, etc.

`themes.css` (second): `theme/dark.css` then `theme/light.css`. These set
**`--plexo-*` color tokens only** — never component selectors. Dark is the
`:root` default; light opts in via `<html data-theme="light">`; the attribute
selector wins over `:root` by specificity, so order does not matter for the
override. Swapping or adding a palette means adding a file to `themes.scss`.

## Rules

- Put component styles in `ui/plexo.css`; put colors in `ui/theme/*.css`. A
  component never hardcodes a color or a scale step — use `var(--plexo-*)` for
  color and the Open Props scale vars for spacing/type/radius/borders/motion.
- Keep selectors flat (plain CSS): no `@layer`, no `@import`.
- After edits, run `./ui/build.sh` and commit the regenerated `base.css` /
  `themes.css` (and `app/*.js` for TypeScript).
- Adding an asset directory (e.g. a sound) means adding it to the `//go:embed`
  list in `ui/embed.go` and referencing it relative to the app root.
