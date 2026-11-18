package db

import (
	"net/url"
	"strings"
)

// LifecycleMigrationDatabaseURL adds passive operator attribution to recognized
// PostgreSQL URIs for golang-migrate's postgres/libpq driver. Other inputs retain
// that driver's existing parsing, source ordering and diagnostics. This is not a
// pgx DSN normalizer or a database-target validator.
func LifecycleMigrationDatabaseURL(raw, build string) string {
	if !strings.HasPrefix(raw, "postgres://") && !strings.HasPrefix(raw, "postgresql://") {
		return raw
	}
	target, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if rendered := target.String(); !strings.HasPrefix(rendered, "postgres://") && !strings.HasPrefix(rendered, "postgresql://") {
		// Preserve legacy non-URI handling when empty authority/path/userinfo
		// loses URI framing in the driver's existing filter.
		return raw
	}
	query := target.Query()
	for key := range query {
		if strings.EqualFold(key, "application_name") {
			delete(query, key)
		}
	}
	query.Set("application_name", LifecycleOperatorConnectionApplicationName(build))
	target.RawQuery = query.Encode()
	return target.String()
}
