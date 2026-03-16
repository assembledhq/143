package db

import (
	"strings"

	"github.com/jackc/pgx/v5"
)

// whereClause accumulates WHERE conditions and named args for building
// dynamic SQL queries. It eliminates the error-prone pattern of manually
// concatenating " AND ..." fragments.
type whereClause struct {
	conditions []string
	args       pgx.NamedArgs
}

func newWhereClause() *whereClause {
	return &whereClause{args: pgx.NamedArgs{}}
}

func (w *whereClause) add(condition string, name string, value interface{}) {
	w.conditions = append(w.conditions, condition)
	w.args[name] = value
}

// addArg registers an additional named argument without appending a new
// condition. Use this when a single condition references multiple parameters.
func (w *whereClause) addArg(name string, value interface{}) {
	w.args[name] = value
}

func (w *whereClause) build() (string, pgx.NamedArgs) {
	if len(w.conditions) == 0 {
		return "", w.args
	}
	return " WHERE " + strings.Join(w.conditions, " AND "), w.args
}

// escapeLike escapes SQL LIKE meta-characters (%, _) so that user-supplied
// values are matched literally.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
