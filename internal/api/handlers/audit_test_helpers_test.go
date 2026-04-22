package handlers

import (
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
)

func expectAuditInsert(mock pgxmock.PgxPoolIface) {
	mock.ExpectQuery("INSERT INTO audit_logs").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), time.Now()))
}

func newAuditEmitterForTest(mock pgxmock.PgxPoolIface) *db.AuditEmitter {
	return db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.Nop())
}
