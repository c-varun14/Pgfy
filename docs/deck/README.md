# Pitch deck

`deck.html` is the seven-slide deck used in the demo video, styled with the dashboard's own dark theme tokens, fonts and brand mark. Open it in a browser, press F11 for full screen, and navigate with →, Space or a click (← back, Home, End); `deck.html?slide=N` opens slide N. Slide 1 reveals its three lines one key press at a time. Everything is local: no network requests.

`pgfy-pitch.pptx` holds the same seven slides for PowerPoint or Keynote. Rebuild it with `npm install pptxgenjs && node build-pptx.mjs` in a scratch directory containing this file (the dependency is intentionally not part of the project).

The spoken script and shot list are in [../demo-runbook.md](../demo-runbook.md).
