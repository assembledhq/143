package lifecyclecensus

import (
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// censusConfig supports a single explicit URI target. Normalize that target
// before pgx parses it so alias ordering, dropped empty hosts, repeated slashes
// and libpq environment/service defaults cannot select an implicit database.
// Credentials and TLS configuration remain pgx-owned; they are never reported.
func censusConfig(databaseURL string) (*pgx.ConnConfig, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("database configuration required")
	}
	target, err := url.Parse(databaseURL)
	if err != nil || (target.Scheme != "postgres" && target.Scheme != "postgresql") {
		return nil, errors.New("invalid database configuration")
	}
	query, err := url.ParseQuery(target.RawQuery)
	if err != nil {
		return nil, errors.New("invalid database configuration")
	}
	invalidTarget := errors.New("explicit database host and name required")
	for _, key := range []string{"host", "port", "dbname", "database"} {
		if values, present := query[key]; present && (len(values) != 1 || strings.TrimSpace(values[0]) == "") {
			return nil, invalidTarget
		}
	}
	if strings.Contains(target.Host, ",") || strings.HasSuffix(target.Host, ":") || (query.Has("dbname") && query.Has("database")) {
		return nil, invalidTarget
	}
	host := target.Hostname()
	if query.Has("host") {
		host = query.Get("host")
	}
	if strings.TrimSpace(host) == "" || strings.Contains(host, ",") {
		return nil, invalidTarget
	}
	// pgx strips ALL leading slashes, not only the first.
	database := strings.TrimLeft(target.Path, "/")
	if query.Has("dbname") {
		database = query.Get("dbname")
	}
	if query.Has("database") {
		database = query.Get("database")
	}
	if strings.TrimSpace(database) == "" {
		return nil, invalidTarget
	}
	port := target.Port()
	if port != "" && query.Has("port") {
		return nil, invalidTarget
	}
	if query.Has("port") {
		port = query.Get("port")
	}
	if port == "" {
		port = "5432" // PostgreSQL default, never inherited from PGPORT/service.
	}
	parsedPort, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsedPort == 0 {
		return nil, invalidTarget
	}
	query.Del("database")
	query.Set("host", host)
	query.Set("port", port)
	query.Set("dbname", database)
	target.RawQuery = query.Encode()
	config, err := pgx.ParseConfig(target.String())
	if err != nil {
		return nil, errors.New("invalid database configuration")
	}
	if config.Host != host || config.Port != uint16(parsedPort) || config.Database != database {
		return nil, invalidTarget
	}
	// TLS negotiation may add fallbacks, but none may change the selected target.
	for _, fallback := range config.Fallbacks {
		if fallback.Host != host || fallback.Port != uint16(parsedPort) {
			return nil, invalidTarget
		}
	}
	return config, nil
}
