
	tests := []struct {
		name         string
		rawQuery     string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expectedLen  int
		assertBody   func(t *testing.T, body []byte)
	}{
		{
			name: "returns issues for org successfully",
			},
			expectedCode: http.StatusOK,
			expectedLen:  1,
			assertBody:   func(t *testing.T, body []byte) {},
		},
		{
			name: "returns empty list when no issues exist",
			},
			expectedCode: http.StatusOK,
			expectedLen:  0,
			assertBody:   func(t *testing.T, body []byte) {},
		},
		{
			name:     "returns opaque next cursor for recency pagination",
			rawQuery: "?limit=1",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now().UTC().Truncate(time.Nanosecond)
				issueID := uuid.New()
				mock.ExpectQuery("SELECT .+ FROM issues WHERE org_id .+ ORDER BY last_seen_at DESC, id DESC LIMIT 1").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(issueColumns).AddRow(
							issueID, orgID, "ext-1", "sentry", nil, nil,
							"Paged Issue", nil, json.RawMessage(`{}`), "open", now, now,
							5, 2, "high", []string{"bug"}, "fp123",
							now, now, nil,
						),
					)
			},
			expectedCode: http.StatusOK,
			expectedLen:  1,
			assertBody: func(t *testing.T, body []byte) {
				var resp models.ListResponse[models.Issue]
				err := json.Unmarshal(body, &resp)
				require.NoError(t, err, "response body should be valid JSON")
				require.Equal(t, encodeIssueCursor(resp.Data[0].LastSeenAt, resp.Data[0].ID), resp.Meta.NextCursor, "list response should return an opaque cursor based on the last issue sort key")
			},
		},
		{
			name:         "rejects invalid cursor",
			rawQuery:     "?cursor=not-a-valid-cursor",
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedLen:  0,
			assertBody: func(t *testing.T, body []byte) {
				require.Contains(t, string(body), "INVALID_CURSOR", "handler should reject malformed cursors before querying the database")
			},
		},
		{
			name:         "omits next cursor for priority sort",
			rawQuery:     "?limit=1&sort=priority",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now().UTC().Truncate(time.Nanosecond)
				issueID := uuid.New()
				mock.ExpectQuery("SELECT .+ FROM issues i .+ ORDER BY ps.score DESC NULLS LAST, i.id DESC LIMIT 1").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(issueColumns).AddRow(
							issueID, orgID, "ext-1", "sentry", nil, nil,
							"Priority Issue", nil, json.RawMessage(`{}`), "open", now, now,
							5, 2, "high", []string{"bug"}, "fp123",
							now, now, nil,
						),
					)
			},
			expectedCode: http.StatusOK,
			expectedLen:  1,
			assertBody: func(t *testing.T, body []byte) {
				var resp models.ListResponse[models.Issue]
				err := json.Unmarshal(body, &resp)
				require.NoError(t, err, "response body should be valid JSON")
				require.Empty(t, resp.Meta.NextCursor, "priority-sorted issue lists should not advertise unsupported cursor pagination")
			},
		},
	}


			tt.setupMock(mock, orgID)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/issues"+tt.rawQuery, nil)
			ctx := middleware.WithOrgID(req.Context(), orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			var resp models.ListResponse[models.Issue]
			err = json.Unmarshal(w.Body.Bytes(), &resp)
			if tt.expectedCode == http.StatusOK {
				require.NoError(t, err, "response body should be valid JSON")
				require.Equal(t, tt.expectedLen, len(resp.Data), "should return expected number of issues")
			}
			tt.assertBody(t, w.Body.Bytes())
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
