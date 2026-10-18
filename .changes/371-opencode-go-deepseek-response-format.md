### Fixes

- **opencode-go**: drop `response_format` for DeepSeek models in `ForwardOpencodeGo` using anchored prefix matching (`strings.HasPrefix(strings.ToLower(cleanModel), "deepseek")`), preventing upstream relay HTTP 400 rejection.
