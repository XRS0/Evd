# Backend Architecture (DDD + Clean Architecture)

This backend is organized as layered, dependency-inverted modules.

## Layer map

- `internal/domain/*`
  - Pure domain models and states.
  - No infrastructure or transport dependencies.

- `internal/application/*`
  - Use-case orchestration.
  - Depends on domain types and **ports** (interfaces).
  - Does not import concrete infrastructure adapters.

- `internal/infrastructure/*`
  - Concrete adapters: filesystem, ffmpeg, transmission.
  - Implements application ports.
  - Contains all external IO/runtime integrations.

- `internal/transport/http`
  - HTTP handlers and router.
  - Depends on application interfaces and DTO shaping.

- `cmd/server`
  - Composition root.
  - Wires infrastructure adapters into application services and transport.

## Dependency rules

Allowed direction is strictly inward:

`transport -> application -> domain`

`infrastructure -> application (ports) + domain`

`cmd/server -> all layers for composition only`

Forbidden:

- `application -> infrastructure`
- `domain -> application/infrastructure/transport`

## Media bounded context

Core use case service: `internal/application/media/Service`

Ports:

- `VideoRepository` (`internal/application/media/ports.go`)
- `Converter` (`internal/application/media/ports.go`)

Adapters:

- `filesystem.Store` implements repository operations
- `ffmpeg.Converter` implements conversion/stream operations

Capabilities:

- video listing
- HLS and MP4 conversion orchestration
- direct mp4 streaming
- background MP4 prewarm for downloaded videos

## Torrent bounded context

Core use case service: `internal/application/torrent/Service`

Port:

- `Gateway` (`internal/application/torrent/ports.go`)

Adapter:

- `transmission.Client`

Capabilities:

- list torrents
- upload `.torrent`
- enable sequential download for early playback

## HTTP transport

Entry points are implemented in `internal/transport/http`:

- `handlers.go` — use-case invocation and response formatting
- `router.go` — route registration
- `stream.go` — range and growing-file stream helpers

## Composition root

`cmd/server/main.go` is the only active runtime entrypoint.

Legacy monolithic server (`backend/main.go`) is now guarded by build tag `legacy` and excluded from normal builds.

## Operational notes

- MP4 prewarm runs in background with bounded queue and conservative concurrency.
- Conversion marker files:
  - HLS: `.transcoded`
  - MP4: `.mp4transcoded`
- Docker image builds from `cmd/server` binary only.
