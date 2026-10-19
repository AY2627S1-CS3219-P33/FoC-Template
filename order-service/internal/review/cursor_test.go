package review

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

func TestCursorRoundTrip(t *testing.T) {
	for _, reason := range []errand.ReviewReason{"", errand.ReviewCourierIssue, errand.ReviewDeliveryOverdue} {
		value := encodeCursor(42, reason)
		require.LessOrEqual(t, len(value), maxCursorLength)

		after, ok := decodeCursor(value, reason)
		require.True(t, ok)
		require.Equal(t, int64(42), after)
	}
}

func TestCursorIsBoundToReasonFilter(t *testing.T) {
	value := encodeCursor(42, errand.ReviewCourierIssue)

	_, ok := decodeCursor(value, "")
	require.False(t, ok)
	_, ok = decodeCursor(value, errand.ReviewRequesterDispute)
	require.False(t, ok)
}

func TestCursorRejectsMalformedValues(t *testing.T) {
	encode := func(raw string) string { return base64.RawURLEncoding.EncodeToString([]byte(raw)) }
	for name, value := range map[string]string{
		"empty":       "",
		"not base64":  "!!!",
		"padded":      base64.URLEncoding.EncodeToString([]byte(`{"a":1}`)),
		"not json":    encode("42"),
		"zero ID":     encode(`{"a":0}`),
		"negative ID": encode(`{"a":-1}`),
		"too long":    strings.Repeat("A", maxCursorLength+1),
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := decodeCursor(value, "")
			require.False(t, ok)
		})
	}
}
