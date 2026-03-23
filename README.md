# Monitor CLI

This project now targets Wails v3 alpha.

## Development

Run `task dev` or `wails3 dev -config ./build/config.yml`.

## Build

Run `task build` for a local build or `task package` for packaged output.

## CI

GitHub Actions can build and release Linux and Windows artifacts with `.github/workflows/build.yml`.
Run it manually from the Actions tab via `workflow_dispatch`.
