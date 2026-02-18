# providers package

## Shell Safety Convention

All commands executed inside sandbox containers run via `sh -c`, so any
user-controlled value interpolated into a command string is a potential
shell injection vector.

### Rules

1. **Always** wrap interpolated values in single quotes and pass them through
   the `shellEscape()` helper (defined in `docker.go`). This escapes embedded
   single quotes by replacing `'` with `'\''`.

   ```go
   // GOOD
   cmd := fmt.Sprintf("cat '%s'", shellEscape(path))

   // BAD – bare %s allows injection
   cmd := fmt.Sprintf("cat %s", path)
   ```

2. **Never** use bare `fmt.Sprintf` with unquoted `%s` for shell arguments,
   even if the value "should" be safe. Defense-in-depth means quoting
   everything.

3. When adding new methods that exec commands in a container, follow the
   existing pattern in `CloneRepo`, `ReadFile`, and `WriteFile`.
