### Fixes

- **providers**: allow models carrying the `-free` suffix in `IsOpencodeFreeTierModel` on OpenCode providers (`opencode`, `opencode-zen`, `opencode-go`), ensuring preview and uncataloged free models route to `/inference/v1` on `opencode-zen` connections without manual catalog additions (fixes #327, PR #328).
