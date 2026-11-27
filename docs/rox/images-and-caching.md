---
sidebar:
  order: 11
---

# Images and caching

rox builds and caches several things so that later launches are fast: a base image, a per-project
image, a mirror of each project's branch, an agent layer and, on Fly, a remote-built image.

- **Base image (container).** `rox:linux` is built from the embedded Containerfile with your engine
  the first time, and rebuilt when its toolkit label (`io.rox.toolkit`) differs from the binary's.
  A custom `--image` is left alone.
- **Project image.** When the branch has a `.rox.sh`, rox bakes the project into an image once
  (clone, run `.rox.sh` as `ROX_PHASE=build`) and launches from it, so later `rox up`s skip the setup.
  Containers are named `rox:PROJECT`; Tart images `rox-PROJECT-BACKEND`. The image is reused while the
  `.rox.sh` file, the base image, the repository URL and the checkout folder are unchanged
  (containers record them as image labels; Tart images in `~/.local/state/rox/images/`); the launch then
  updates the image's checkout instead of cloning. `--rebuild` forces a rebuild; `--no-prebuild`,
  `--no-hook`, `--local`, `--no-project`, `prebuild: false`, and the `fly` and `railway`
  backends skip images. If the bake fails, the error suggests `--no-prebuild`.
- **Branch mirror.** To read `.rox.sh` and `.rox.json`, rox fetches the branch into a bare mirror in
  `~/.cache/rox/repos/` (one per remote URL, one hashed ref per branch), under a lock.
- **Agent layer.** See [Agents](agents.md).
- **Fly image.** Built on Fly's builder, tagged `registry.fly.io/APP:rox-TOOLKIT`, rebuilt when the
  toolkit changes.

The flags are in [Creating guests](creating-guests.md#rox-up); `.rox.sh` is in
[Project settings](project-settings.md#roxsh).
