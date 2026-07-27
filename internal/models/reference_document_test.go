package models

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestReferenceContextSetPinMemberContract(t *testing.T) {
	t.Parallel()

	documentID := uuid.New()
	member := ReferenceContextSetPinMember{
		PinID:               uuid.New(),
		ReferenceDocumentID: documentID,
	}

	field, ok := reflect.TypeOf(member).FieldByName("ReferenceDocumentID")
	require.True(t, ok, "pin member should expose the neutral reference document field")
	require.Equal(t, "reference_document_id", field.Tag.Get("db"), "database tag should match the renamed column")

	raw, err := json.Marshal(member)
	require.NoError(t, err, "pin member should serialize as JSON")

	var payload map[string]string
	require.NoError(t, json.Unmarshal(raw, &payload), "serialized pin member should decode as a string map")
	require.Equal(t, documentID.String(), payload["reference_document_id"], "JSON should expose the neutral reference document key")
	require.NotContains(t, payload, "document_id", "JSON should not expose the obsolete document key")
}
