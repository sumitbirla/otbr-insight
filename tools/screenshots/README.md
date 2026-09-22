# Screenshots

Regenerates the images in [`docs/`](../../docs) that the top-level README embeds.

```sh
OTBR_URL=http://openthread-br.local:8081 ./tools/screenshots/capture.sh
```

Needs Google Chrome, Node 22 or newer (the driver uses the global `WebSocket`,
which is why there are no npm dependencies), and a working `make build`.

## Why the data is fake and the app is not

The images are captured from the **real** application — real assets, real CSS,
the real decoder — served by a throwaway instance on port 8099. Only the data is
substituted, and that is not a convenience:

- A published mesh view would carry the network name, PAN ID, extended PAN ID
  and every device's extended address. The repo's rule is to sanitise at the
  point of capture, and a screenshot is a capture.
- A real energy scan takes the border router **off channel for ten seconds**,
  which makes it briefly deaf to its own sleepy children. That is a reasonable
  thing to do when someone presses the button, and an unreasonable thing to do
  as a build step.

## Layout

| | |
|---|---|
| `shoot.mjs` | Chrome DevTools Protocol driver: navigate, evaluate a scene, screenshot |
| `scenes/channels.js` | Channel noise, with a canned measurement chosen to show all three verdicts |
| `scenes/report.js` | Device report, fed through the real decode endpoint |
| `fixtures/diagnostics-export.json` | A sanitised Matter diagnostics export |
| `sanitise.py` | Produces that fixture from a real export |
| `capture.sh` | Starts the instance and Chrome, runs both scenes, cleans up |

## Refreshing the fixture

If the report view grows to read attributes the current fixture lacks, sanitise
a newer export rather than editing the fixture by hand:

```sh
python3 tools/screenshots/sanitise.py ~/Downloads/matter-<device>.json
```

That replaces network identity, addresses, certificates, the controller's node
ID, the time zone and the commissioning timestamps with documentation values,
and keeps what makes the screenshot worth looking at: vendor and product names,
versions, counters and sensor readings. Read the diff before committing it.

## Adding a scene

A scene is evaluated in the page after it loads. It sets `location.hash`, calls
`syncNavigation()`, and hands data to whichever render function the view uses —
`app.js` is a classic script, so its top-level functions are global. A scene that
needs a data file embeds `__FIXTURE__`, which the driver replaces with the
contents of `FIXTURE` as a JSON string literal.

`HEIGHT` is the CSS viewport height and crops the shot; `FULL=1` captures the
whole scrollable page instead. Prefer a crop — a full-page capture of the report
is 2300 CSS px tall and renders as a sliver in a README.
