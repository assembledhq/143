package models_test

import (
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestListResponseMarshalJSONEncodesNilDataAsEmptyArray(t *testing.T) {
	t.Parallel()

	body, err := json.Marshal(models.ListResponse[string]{})
	require.NoError(t, err, "ListResponse should marshal successfully")
	require.JSONEq(
		t,
		`{"data":[],"meta":{}}`,
		string(body),
		"ListResponse should encode nil data as an empty array",
	)
}
