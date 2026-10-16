### Quoted `WorkingDirectory=` made systemd refuse the autostart unit

`service install` wrote `WorkingDirectory="/usr/local/bin"` into the systemd
user unit. systemd parses that setting with `config_parse_working_directory`,
which runs the whole remainder of the line through `path_simplify_and_warn`
without unquoting (unlike `ExecStart=`), so the quotes were taken as part of
the value and the unit was rejected as not absolute:

```
WorkingDirectory= path is not absolute: "/usr/local/bin"
Unit configuration has fatal error, unit will not be started.
```

The path is now emitted unquoted, with only a literal `%` doubled for specifier
expansion; whitespace is not a separator for this setting, so a spaced path
still survives. Closes #329.

The autostart tests now hand each rendered config to a real parser instead of
matching substrings: `systemd-analyze verify` for the unit, `plutil`/`xmllint`
for the launchd plist, `sh -n` for the OpenRC and runit scripts, and
`desktop-file-validate` for the XDG autostart entry. Each check skips where its
tool is absent rather than passing silently.
