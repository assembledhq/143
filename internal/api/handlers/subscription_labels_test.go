package handlers

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

func TestGeneratedSubscriptionLabel(t *testing.T) {
	t.Parallel()

	login := "alice-gh"
	user := &models.User{
		ID:          uuid.MustParse("00000000-0000-0000-0000-000000000123"),
		OrgID:       uuid.MustParse("00000000-0000-0000-0000-000000000456"),
		Name:        "Alice Smith",
		Email:       "alice@example.com",
		GitHubLogin: &login,
	}

	tests := []struct {
		name     string
		attempt  int
		maxLen   int
		expected string
	}{
		{
			name:     "uses base label on first attempt",
			attempt:  1,
			maxLen:   100,
			expected: "Alice Smith",
		},
		{
			name:     "appends numeric suffix on later attempts",
			attempt:  2,
			maxLen:   100,
			expected: "Alice Smith 2",
		},
		{
			name:     "truncates base to leave room for suffix",
			attempt:  12,
			maxLen:   8,
			expected: "Alice 12",
		},
		{
			name:     "forces minimum base length when suffix exceeds max length",
			attempt:  12,
			maxLen:   2,
			expected: "A 12",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := generatedSubscriptionLabel(user, tt.attempt, tt.maxLen)

			require.Equal(t, tt.expected, actual, "generatedSubscriptionLabel should return the expected label")
		})
	}
}

func TestInferredSubscriptionLabelBase(t *testing.T) {
	t.Parallel()

	login := "alice-gh"
	blankLogin := "   "

	tests := []struct {
		name     string
		user     *models.User
		expected string
	}{
		{
			name: "prefers normalized user name",
			user: &models.User{
				Name:        "  Alice   Smith  ",
				Email:       "alice@example.com",
				GitHubLogin: &login,
			},
			expected: "Alice Smith",
		},
		{
			name: "falls back to github login",
			user: &models.User{
				Name:        "   ",
				Email:       "alice@example.com",
				GitHubLogin: &login,
			},
			expected: "alice-gh",
		},
		{
			name: "falls back to email local part",
			user: &models.User{
				Name:        "",
				Email:       " alice@example.com ",
				GitHubLogin: &blankLogin,
			},
			expected: "alice",
		},
		{
			name: "falls back to raw email when no local part can be split",
			user: &models.User{
				Name:  "",
				Email: "alice",
			},
			expected: "alice",
		},
		{
			name:     "defaults to subscription when no user metadata exists",
			user:     nil,
			expected: "Subscription",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := inferredSubscriptionLabelBase(tt.user)

			require.Equal(t, tt.expected, actual, "inferredSubscriptionLabelBase should return the expected fallback label")
		})
	}
}

func TestTruncateSubscriptionLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		label    string
		maxLen   int
		expected string
	}{
		{
			name:     "returns normalized label when within limit",
			label:    "  Team A  ",
			maxLen:   100,
			expected: "Team A",
		},
		{
			name:     "trims whitespace after truncation",
			label:    "Team A ",
			maxLen:   5,
			expected: "Team",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := truncateSubscriptionLabel(tt.label, tt.maxLen)

			require.Equal(t, tt.expected, actual, "truncateSubscriptionLabel should return the expected truncated label")
		})
	}
}

func TestResolveSubscriptionLabel(t *testing.T) {
	t.Parallel()

	login := "alice-gh"
	sentinelErr := errors.New("db unavailable")
	user := &models.User{
		ID:          uuid.MustParse("00000000-0000-0000-0000-000000000789"),
		OrgID:       uuid.MustParse("00000000-0000-0000-0000-000000000999"),
		Name:        "",
		Email:       "alice@example.com",
		GitHubLogin: &login,
	}

	tests := []struct {
		name             string
		requested        string
		maxLen           int
		initiate         func(label string) error
		expectedLabel    string
		expectedAttempts []string
		expectedErr      error
		expectedTaken    *db.ErrCredentialLabelTaken
	}{
		{
			name:      "uses requested label when provided",
			requested: "  Team A  ",
			maxLen:    100,
			initiate: func(label string) error {
				return nil
			},
			expectedLabel:    "Team A",
			expectedAttempts: []string{"Team A"},
		},
		{
			name:      "retries with generated labels after label collisions",
			requested: "",
			maxLen:    100,
			initiate: func(label string) error {
				if label == "alice-gh" {
					return &db.ErrCredentialLabelTaken{Label: label, ExistingStatus: "active"}
				}
				return nil
			},
			expectedLabel:    "alice-gh 2",
			expectedAttempts: []string{"alice-gh", "alice-gh 2"},
		},
		{
			name:      "returns non label errors immediately",
			requested: "",
			maxLen:    100,
			initiate: func(label string) error {
				return sentinelErr
			},
			expectedAttempts: []string{"alice-gh"},
			expectedErr:      sentinelErr,
		},
		{
			name:      "returns last label taken error after exhausting retries",
			requested: "",
			maxLen:    100,
			initiate: func(label string) error {
				return &db.ErrCredentialLabelTaken{Label: label, ExistingStatus: "active"}
			},
			expectedAttempts: func() []string {
				attempts := make([]string, 0, autoGeneratedLabelRetryLimit)
				for attempt := 1; attempt <= autoGeneratedLabelRetryLimit; attempt++ {
					attempts = append(attempts, generatedSubscriptionLabel(user, attempt, 100))
				}
				return attempts
			}(),
			expectedErr: &db.ErrCredentialLabelTaken{
				Label:          generatedSubscriptionLabel(user, autoGeneratedLabelRetryLimit, 100),
				ExistingStatus: "active",
			},
			expectedTaken: &db.ErrCredentialLabelTaken{
				Label:          generatedSubscriptionLabel(user, autoGeneratedLabelRetryLimit, 100),
				ExistingStatus: "active",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			attempts := make([]string, 0)
			label, err := resolveSubscriptionLabel(tt.requested, user, tt.maxLen, func(label string) error {
				attempts = append(attempts, label)
				return tt.initiate(label)
			})

			if tt.expectedErr != nil {
				require.Error(t, err, "resolveSubscriptionLabel should return an error")
				require.Empty(t, label, "resolveSubscriptionLabel should not return a label on error")
				if tt.expectedTaken != nil {
					var labelErr *db.ErrCredentialLabelTaken
					require.True(t, errors.As(err, &labelErr), "resolveSubscriptionLabel should return a label-taken error")
					require.Equal(t, tt.expectedTaken.Label, labelErr.Label, "resolveSubscriptionLabel should return the last attempted label in the error")
					require.Equal(t, tt.expectedTaken.ExistingStatus, labelErr.ExistingStatus, "resolveSubscriptionLabel should preserve the existing status in the error")
				} else {
					require.True(t, errors.Is(err, tt.expectedErr), "resolveSubscriptionLabel should return the expected error")
				}
			} else {
				require.NoError(t, err, "resolveSubscriptionLabel should not return an error")
				require.Equal(t, tt.expectedLabel, label, "resolveSubscriptionLabel should return the expected label")
			}

			require.Equal(t, tt.expectedAttempts, attempts, "resolveSubscriptionLabel should try the expected labels")
		})
	}
}
