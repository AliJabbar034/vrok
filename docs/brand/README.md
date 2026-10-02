# vrok marks

The mark in use is **B, open locker**: the locker door from the site's hero,
standing open on a lit interior with the "v" inside. **E, source point** is
kept as the alternative: a "v" fanning out from one dot, your machine, to
whoever has the link.

Each folder has the SVG source and PNGs rendered from it at 1024, 512, 256,
128, 64, 32 and 16 px, with transparent corners.

| Folder            | Mark        |
| ----------------- | ----------- |
| `b-open-locker/`  | in use      |
| `e-source-point/` | alternative |

Colours: enamel `#0C3A3F`, deep enamel `#072A2E`, enamel chip `#2B7E85`,
signal `#FFC400`, aluminium `#B9C2C1`, ink `#EDF6F5`.

## Switching to E

The mark lives in five places. Copy the E SVG over the four SVGs and its
1024 px PNG over the README logo:

```sh
E=docs/brand/e-source-point/vrok-e-source-point.svg
for f in docs/logo.svg site/assets/mark.svg \
         web/viewer/static/mark.svg web/viewer/static/favicon.svg; do
  cp "$E" "$f"
done
cp docs/brand/e-source-point/vrok-e-source-point-1024.png docs/logo.png
```

The viewer's files are embedded in the binary, so rebuild vrok afterwards.
