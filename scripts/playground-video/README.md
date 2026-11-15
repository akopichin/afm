# Playground video

The video uses screenshots captured from a real AFM run with the bundled
synthetic agents. It includes an actual budget hold, a UI budget update, and an
actual process stop/restart. The edit compresses waiting time and uses motion
between captured screens; the report image and prices remain labelled simulated.

Use a Python environment with `playwright`, `Pillow` and `imageio-ffmpeg`, an
installed AFM 3.1.4+, and Chrome. Install `gifsicle` for lossless GIF optimization. No provider account is required. Like the
playground launcher, the capture script loads normal global AFM configuration;
use an environment without external lifecycle hooks when recording.

```bash
python scripts/playground-video/capture.py \
  --out /tmp/afm-video-take \
  --browser '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
python scripts/playground-video/render.py \
  --captures /tmp/afm-video-take --out docs/assets \
  --font /path/to/regular.ttf --bold-font /path/to/bold.ttf
```

Choose a fresh capture output directory and a free `--port` (default 18981).
The capture script stops only the AFM processes it started. Captures, run data
and logs stay in its output directory for inspection. It never publishes them.
Use `--afm /path/to/afm` to select a different installed binary.

The renderer publishes only a 21-second looping GIF preview. A 65-second
intermediate edit is encoded in a temporary directory outside the repository
and removed after GIF conversion. No MP4, poster or chapter file is written to
the output directory. If `gifsicle` is available, lossless optimization preserves
the rendered frames, resolution and timings.

Captions are burned in, so the story works without sound. Source screenshots,
runtime logs, temporary encodings and browser profiles should not be committed.
Font files are supplied by the recording environment and are not redistributed.
