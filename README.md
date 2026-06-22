# server-monitor

A live Linux system monitor in Go. The server runs on the machine you want to
watch and serves a self-contained dashboard built for a **Raspberry Pi 4 with a
7" touchscreen** acting as a dedicated viewer.

- **`GET /`** — the embedded dashboard (no internet/CDN needed; assets are
  compiled into the binary).
- **`GET /ws`** — a WebSocket streaming a full system snapshot as JSON every tick.

## Run

```sh
go build -o server-monitor .
./server-monitor                 # listens on :8080, 2s interval
./server-monitor -addr :9000 -interval 1s
```

On the Pi, open a browser (kiosk/fullscreen) at `http://<server-ip>:8080/`.

### Flags

| flag | default | meaning |
|------|---------|---------|
| `-addr` | `:8080` | listen address |
| `-interval` | `2s` | snapshot/stream cadence |

## Dashboard

Tap the left rail to switch tabs, all touch-sized for the 7" panel:

- **Overview** — CPU / memory / root-FS / load gauges, CPU & network history.
- **CPU** — total + per-core utilisation, model, temperature, history graph.
- **Memory** — RAM (used/cache/free) and swap, with history.
- **Storage** — every filesystem: usage bars plus live read/write rates.
- **GPU** — NVIDIA (via `nvidia-smi`) or integrated/AMD via sysfs; fields the
  host doesn't expose are shown as unavailable rather than faked.
- **Network** — throughput graph and per-interface rates, addresses, totals.
- **Processes** — top processes by CPU with user, state, and RSS.
- **System** — host, kernel, arch, boot time, virtualization, load.

## Layout

- `main.go` — HTTP server, embedded static assets, WebSocket streaming loop.
- `sysinfo/` — stateful collector (`gopsutil` based); computes network and disk
  IO **rates** between ticks. `gpu.go` holds the layered GPU detection.
- `static/index.html` — the dashboard (vanilla JS, canvas gauges/charts, no deps).

GPU utilisation for integrated Intel GPUs generally isn't exposed without
privileged tools (`intel_gpu_top`); the dashboard reflects that honestly.
