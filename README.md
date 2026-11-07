# 9router-go - Fork jhonoryza/9router-go

Fork dari [luqman-v1/9router-go](https://github.com/luqman-v1/9router-go) (Go port dari [decolua/9router](https://github.com/decolua/9router)).

**Semua penjelasan produk, instalasi, dan dokumentasi feature ada di README upstream:**
👉 https://github.com/luqman-v1/9router-go#readme

README ini hanya berisi hal-hal spesifik fork ini: apa yang berbeda dari upstream, cara sync, dan catatan operasional - juga berlaku sebagai konteks untuk AI agent yang bekerja di repo ini di masa depan.

---

## Apa yang berbeda di fork ini

### 1. Cache-only live catalog (`/v1/models` tidak pernah fetch) - DIVERGENCE permanen

Upstream (`LIVE_MODEL_RESOLVERS` di `src/app/api/v1/models/route.js`) men-fetch katalog provider **di dalam request path**. Fork ini mengubah `resolveLiveCatalog` (`internal/handlers/chat/live_catalog.go`) menjadi **cache-only**:

- Baca hanya dari in-memory store + KV `liveCatalogs` (hasil refresh terakhir).
- Discovery hanya terjadi via `POST /api/models/live-catalog/refresh` (dashboard) atau daily background job `StartBackgroundLiveCatalogRefresh` (`internal/handlers/chat/live_catalog_refresh.go`).
- Alasan: provider lambat/mati tidak boleh stall `/v1/models` (dulu kiro timeout 30s per koneksi membuat `/v1/models` butuh waktu menit di ribuan koneksi).
- Di `live_catalog_refresh.go`, fetch di-detach dengan `context.WithoutCancel` (upstream #364) supaya pembatalan satu request dashboard tidak mematikan shared flight.

**Konsekuensi saat merge upstream (WAJIB ikuti preseden ini):**
- Setiap hunk upstream yang menambah/mengubah blok `var fetch` switch di `resolveLiveCatalog` → buang (cache-only tidak fetch di request path).
- Upstream tests yang mengasumsikan fetch-in-request-path → **skip dengan komentar `DIVERGENCE`** (pola ada di `live_catalog_pinned_test.go` dan `live_catalog_handler_test.go`). Jangan reimplement desain upstream.
- Pinned-connections skip (upstream `d329f9f4`, cek `pinnedEnabledModels`) TETAP diadopsi - taruh sebelum cache lookup.

### 2. PR/porting kita yang harus tetap ada setelah merge

- **PR #1** (`901715f`, merged upstream): cache-only catalog - sumber divergence di atas.
- **Deterministic JSON marshaling** (`canonicalChatBody` + `Deterministic(true)` di lane DeepSeek, PR #296 kita) - upstream menggeneralisasi ke semua provider (`acddcddc`/`04f3111b`/`12a7a680`). Keduanya harus tetap ada: sender-side (kita) + comparator side (upstream `2d90859` `sameTail` via `reflect.DeepEqual`) menutup dua hole berbeda dari satu root cause (map key order JSON).
- **#4791** - jangan reintroduce hash-derived `ses_` prefix. **#4805** - free-tier reasoning placeholder hanya catalog-driven.
- `TranslateOpenCodeSessionID` sejak v1.9.15 deterministik via sha256 (upstream) - test lama yang minta wall-clock timestamp sudah dibuang; test cache-eviction juga (cache hilang di desain upstream).
- Field fork di `internal/handlers/dashboard/routes.go`: `ChatHandler *chat.ChatHandler` (untuk live-catalog refresh wiring di `internal/handlers/router.go`) - saat upstream menambah field baru di struct yang sama (mis. `Resilience`), pertahankan keduanya, begitu juga wiring-nya di `router.go`.

### 3. Docker image amd64-only

`.github/workflows/docker-publish.yml` build & push `jhonoryza/9router-go:latest` ke Docker Hub - **linux/amd64 saja** (constraint deployment). PR dibuat ke fork ini saja, **tidak pernah ke upstream**.

### 4. Upstream test yang perlu di-skip (terdokumentasi, jangan "diperbaiki")

| Test | Alasan |
|---|---|
| `TestLiveCatalogFlightSurvivesLeaderCancellation` | deadlocks under cache-only (`<-entered` tak pernah fired) |
| `TestResolveLiveCatalogPinnedEnabledModelsSkipsFetch`, `TestResolveLiveCatalogCachesFailure` | asumsi fetch-in-request-path; cache-only tidak pernah fetch |
| `TestHandleModels_AnthropicLiveCatalog*` | sama, fetch-in-request-path |

---

## Alur sync rutin

```bash
cd ~/9router-go
export PATH=/usr/local/go/bin:$PATH GOTOOLCHAIN=local
git fetch upstream
git merge upstream/main --no-edit     # resolve konflik sesuai bagian "Apa yang berbeda"
mkdir -p web/dist && touch web/dist/.gitkeep   # stub //go:embed untuk build lokal
go build ./... && go vet ./...
go test ./internal/handlers/chat/ ./internal/handlers/dashboard/ ./internal/handlers/oauth/ \
        ./internal/proxy/ ./internal/translator/ ./internal/db/ ./internal/providers/... -count=1
git push origin main                  # CI docker-publish jalan otomatis
```

Konflik yang hampir selalu muncul: `internal/handlers/chat/live_catalog.go`, `internal/handlers/chat/live_catalog_handler_test.go`, `internal/handlers/dashboard/routes.go`, `internal/handlers/router.go`, `internal/proxy/opencode.go`, `internal/proxy/executor/transform.go` (reasoning-inject scope fork #4805 vs upstream #5290 → pertahankan logika fork: provider rule wins + catalog-driven placeholder).

CI hijau → deploy (lihat bawah) → cek banner versi via `docker logs 9router-go | head -2` dan `/health`.

---

## Deployment (VPS pribadi)

- Host: VPS pribadi (detail host tidak dicatat di repo publik — lihat catatan operator / memory agent)
- Container `9router-go`: image `jhonoryza/9router-go:latest`, ports `20128:20130` + `20129:20130`, env `ALLOW_PRIVATE_NETWORKING=true` (warp egress & opencode-free-bridge via docker bridge IP host), volume `infra_9router_data:/root/.9router`, **network `netssd2`** (network traefik - WAJIB; network lain = traefik 504), restart unless-stopped.
- **Jalur publik:** `ai.labkita.my.id` → Cloudflare proxied A record ke IP public VPS → port 80 traefik → label `Host('ai.labkita.my.id')` → container port 20130. Setelah re-create container selalu verify: `curl -H 'Host: ai.labkita.my.id' http://localhost/` di host, lalu `curl https://ai.labkita.my.id/health`.
- Deploy:
  ```bash
  ssh <host> "docker pull jhonoryza/9router-go:latest && docker stop 9router-go && docker rm 9router-go && docker run -d --name 9router-go -p 20128:20130 -p 20129:20130 -e ALLOW_PRIVATE_NETWORKING=true -v infra_9router_data:/root/.9router --restart unless-stopped --network netssd2 -l 'traefik.enable=true' -l 'traefik.http.routers.ai9router.entrypoints=web' -l 'traefik.http.routers.ai9router.rule=Host(\`ai.labkita.my.id\`)' -l 'traefik.http.services.ai9router.loadbalancer.server.port=20130' jhonoryza/9router-go:latest && sleep 5 && curl -s http://127.0.0.1:20128/health && docker image prune -f"
  ```
- Dashboard & API keys: kredensial tidak dicatat di repo publik (lihat catatan operator / memory agent).
- **zcode provider (#861, v1.9.16-exp.5):** koneksi via device-flow login baru per akun (dashboard → ZCode). TIDAK bisa dipindah dari koneksi `glm` yang ada - endpoint & kredensial beda (glm = coding-plan API key berbayar; zcode = plan JWT Start/Weekend gratisan).
- Provider tambahan fork: `ocb/` opencode-free-bridge (custom node `openai-compatible-chat-82e07aa5-396a-4d67-b459-cef0f844d720`, systemd user service port 20140 di host). Combo `buff` (fallback): `cbai/deepseek-v4.1-flash` → `ocb/beam-free` → `oc/beam-free` → `ocz/beam-free`.

---

## Riwayat sync (terbaru dulu)

| Versi fork | Catatan |
|---|---|
| v1.9.16-exp.5 (`d23d99e6`) | +zcode #861, Copilot GPT-6.x /responses #960, Claude via Copilot native #958, agy fingerprint #964, govulncheck bump #965. 4 konflik, cache-only dipertahankan |
| v1.9.15 (`6e508b3b`) | ~200 commits: CodeBuddy back-off #647, sticky session #770, deterministic JSON semua provider, Cloudflare Tunnel #812, combo-to-combo fallback #807, response timeout #744, persona #601, routing headers, opencode v2 gate/direct-dial/rotation, provider baru (Nous, SenseNova, DaoXE, GLM OAuth, Grok-web, ElevenLabs, Batch & Files). 6 konflik; `TranslateOpenCodeSessionID` diadopsi dari upstream |
| v1.9.14 stable | merge 3 commit (#430, exp.8, release) |
| Sebelumnya | v1.9.12 → v1.9.13 → exp.1-exp.8, full-merge dengan divergence cache-only dipertahankan di setiap merge |

---

## Aturan main (agent & manusia)

1. **PR hanya ke fork ini** (`jhonoryza/9router-go`), tidak pernah ke upstream. Sinkronisasi berjalan sebagai merge langsung ke `main` (bukan PR).
2. **Jangan edit `CHANGELOG.md`** - fragment `.changes/<pr>-<slug>.md` hanya untuk kontribusi ke upstream; fork ini merge langsung.
3. **`AGENTS.md` upstream adalah spec engineering** (provider isolation, zero-panic, SQLite N+1, small-hardware budget) - berlaku penuh di fork.
4. **Divergence cache-only tidak boleh diregresi.** Kalau merge menyentuh live catalog, ikut preseden di atas & jalankan test suite chat (durasi normal ~90-100s).
5. Test `DIVERGENCE` jangan dibuat hijau dengan reimplement upstream design.
6. Versi banner mengikuti tag upstream yang dimerge (`scripts/bump-version.sh` upstream); fork tidak bump sendiri.
7. Credential (password dashboard, API keys, DOCKERHUB_TOKEN, cookie) tidak pernah di-commit atau di-log; ditulis `[REDACTED]` di chat.
8. Build env lokal: `export PATH=/usr/local/go/bin:$PATH GOTOOLCHAIN=local`; bun di `$HOME/.bun/bin`; `web/dist/.gitkeep` sebelum build.
9. Jika ada test timing-sensitif gagal satu kali, rerun dulu sebelum diagnosa (preseden: flake ms-boundary lama).
