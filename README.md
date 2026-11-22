# cast-hbo-ad-skipper

Skips the ad breaks in HBO Max live sports streams on Chromecast by restarting the stream the moment an ad starts.

HBO Max stitches ads into live streams, and restarting a stream drops you back at the live edge without the ad. This service watches every Chromecast on your network and does that restart for you, usually within a second or two of the ad starting. Inspired by [CastSponsorSkip](https://github.com/gabe565/CastSponsorSkip).

## How it works

The service connects to each Chromecast over the Cast protocol (port 8009), the same way a phone does. It uses its own small Cast connection (`internal/castconn`), which has deadlines on every read and write and keepalive PINGs, so it notices a dead connection and reconnects.

1. **Detecting an ad.** When an ad break starts, the HBO Max receiver adds a `breakStatus` to the standard Cast `MEDIA_STATUS` (and lists the break in `media.breaks`, with `isEmbedded: true`). The receiver pushes that status as soon as the break starts, so the service doesn't have to poll quickly to catch it.
2. **Restarting the stream.** The service sends the receiver a new `LOAD` request with the media the phone originally loaded (`contentId` and `customData`, including HBO's playback token), minus the stream URL the receiver had already resolved. The receiver then asks HBO for a fresh playback session and continues at the live edge without the ad. Expect about 5 seconds of buffering instead of a break of several minutes.

Guard rails:

- Only HBO Max sessions are touched (app ID `3A2AA214`).
- Only `LIVE` streams are restarted. Restarting on-demand content would lose your position.
- Paused streams are left alone.
- After a restart, there is a 30 s cooldown before the service will restart again. This prevents restart loops if the new session also opens with an ad.

## Running with Docker Compose

```sh
docker compose up -d
docker compose logs -f
```

`docker-compose.yml` runs the image that CI publishes to `ghcr.io/dennispaagman/cast-hbo-ad-skipper` from `main`. Run `docker compose pull` to update it. To run a local build instead, layer `docker-compose.dev.yml` on top:

```sh
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d --build
```

The compose file uses `network_mode: host`, because finding Chromecasts with mDNS needs multicast on your LAN. If you can't use host networking (for example Docker Desktop with host networking turned off), list your devices explicitly:

```yaml
environment:
  HBO_AD_SKIPPER_DEVICES: 192.168.1.79
```

## Running without Docker

```sh
mise install        # Go + golangci-lint, pinned in .mise.toml
mise run build
./bin/cast-hbo-ad-skipper
```

Start with `-dry-run` to see which ads would be skipped, without restarting anything.

## Configuration

Every flag can also be set as an environment variable: add the `HBO_AD_SKIPPER_` prefix, use upper case, and replace dashes with underscores (for example `-dry-run` becomes `HBO_AD_SKIPPER_DRY_RUN`).

| Flag | Default | Description |
|---|---|---|
| `-devices` | (empty) | Comma-separated device addresses (`host` or `host:port`). Disables mDNS discovery. |
| `-dry-run` | `false` | Log detected ads without restarting the stream. |
| `-log-level` | `info` | `debug`, `info`, `warn` or `error`. |

Example log output when an ad is skipped:

```
level=INFO msg="Ad break started." device=Woonkamer break_id=break_57951413.1794148841560 clip_id=break_ad_id_96468542
level=INFO msg="Restarting stream to skip the ad." device=Woonkamer title="Europees Kampioenschap | Zeddam"
level=INFO msg="Stream restarted without the ad." device=Woonkamer took=4.9s
```

## Debugging with `castprobe`

`cmd/castprobe` is the tool that was used to work out how the HBO receiver signals ads. It connects to one device and prints a one-line summary of every status update. Whenever the status structure changes, it also writes a full JSON dump to a separate file.

```sh
go run ./cmd/castprobe -addr 192.168.1.79 -full full.log | tee timeline.log
```

If HBO changes how its receiver reports ads, this is the first thing to run. The dumps contain HBO session tokens, so don't share them.

## Development

```sh
mise run fmt     # gofumpt + goimports via golangci-lint
mise run lint    # golangci-lint
mise run test    # go test -race
mise run check   # lint + test
```

GitHub Actions (`.github/workflows/ci.yml`) checks formatting, lints, runs the tests and builds a multi-arch image (`linux/amd64`, `linux/arm64`). On pushes to `main` and on `v*` tags it publishes the image to `ghcr.io/<owner>/<repo>`.

## Caveats

- This relies on how the HBO Max Cast receiver currently behaves: the `breakStatus` signal, accepting a `LOAD` from a sender other than the phone, and a restart skipping the ad. Any of these can change without notice.
- The service works with real Chromecast devices that cast HBO Max. It does not cover the native HBO Max app on Google TV or Android TV, because that app doesn't use the Cast protocol.
