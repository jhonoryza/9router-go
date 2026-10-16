### Fixes

- **translator**: concatenate every `system` and `developer` message with `\n\n` into Responses `instructions` during Chat Completions to Responses translation, instead of silently dropping all but the first (fixes #332, porting decolua/9router#4813).
