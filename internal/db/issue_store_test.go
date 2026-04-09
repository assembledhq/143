			},
			expected: 1,
		},
		{
			name: "uses composite cursor for recency pagination",
			filters: IssueFilters{
				CursorLastSeenAt: ptrTime(now),
				CursorID:         ptrUUID(issueID1),
			},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM issues WHERE org_id .+ AND \\(last_seen_at, id\\) < \\(@cursor_last_seen_at, @cursor_id\\) .+ ORDER BY last_seen_at DESC, id DESC LIMIT 50").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(issueColumns).
							AddRow(
								issueID2, orgID, "ext-2", "linear", nil, nil,
								"Issue Two", nil, json.RawMessage(`{}`), "open", now, now,
								3, 1, "medium", []string{"perf"}, "fp-2",
								now, now, nil,
							),
					)
			},
			expected: 1,
		},
		{
			name:    "returns empty when no issues exist",
			filters: IssueFilters{},
	}
}

func ptrTime(t time.Time) *time.Time {
	return &t
}

func ptrUUID(id uuid.UUID) *uuid.UUID {
	return &id
}

func TestIssueStore_GetByID(t *testing.T) {
	t.Parallel()
