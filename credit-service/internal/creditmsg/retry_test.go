package creditmsg

import (
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"
)

func TestAttemptCountsParksRequeuesAndTheCurrentDelivery(t *testing.T) {
	for name, tc := range map[string]struct {
		headers amqp.Table
		want    int
	}{
		"first delivery":                 {nil, 1},
		"requeued once":                  {amqp.Table{"x-acquired-count": int64(1)}, 2},
		"parked twice":                   {amqp.Table{retryCountHeader: int32(2)}, 3},
		"parked twice, then redelivered": {amqp.Table{retryCountHeader: int32(2), "x-acquired-count": int64(1)}, 4},
		"other integer types":            {amqp.Table{retryCountHeader: int64(4), "x-acquired-count": int32(1)}, 6},
		"a header of the wrong type":     {amqp.Table{retryCountHeader: "many", "x-acquired-count": 1.5}, 1},
		"dead-lettered copy keeps count": {amqp.Table{retryCountHeader: int32(7), "x-death": []any{amqp.Table{"count": int64(7)}}}, 8},
	} {
		d := amqp.Delivery{Headers: tc.headers}
		require.Equal(t, tc.want, attempt(d), name)
	}
}

func TestRetryPublishingCopiesTheCommandAndRaisesTheCount(t *testing.T) {
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	d := amqp.Delivery{
		RoutingKey: "credit.transfer", Body: []byte(`{"x":1}`),
		ContentType: "application/json", DeliveryMode: amqp.Transient,
		MessageId: "errand:transfer", CorrelationId: "corr", Type: "credit.transfer", AppId: "order-service", Timestamp: at,
		Headers: amqp.Table{
			"trace":               "abc",
			retryCountHeader:      int32(2),
			"x-acquired-count":    int64(1),
			"x-death":             []any{amqp.Table{"count": int64(2)}},
			"x-first-death-queue": "credit-service.commands.retry",
		},
	}

	p := retryPublishing(d, 3)

	require.Equal(t, d.Body, p.Body)
	require.Equal(t, "application/json", p.ContentType)
	require.Equal(t, "errand:transfer", p.MessageId, "the identity the stored result is keyed on")
	require.Equal(t, "corr", p.CorrelationId)
	require.Equal(t, "credit.transfer", p.Type)
	require.Equal(t, "order-service", p.AppId)
	require.Equal(t, at, p.Timestamp)
	require.EqualValues(t, amqp.Persistent, p.DeliveryMode, "a parked command survives a broker restart")
	require.Equal(t, amqp.Table{"trace": "abc", retryCountHeader: int32(3)}, p.Headers,
		"custom headers are kept, broker-managed x- headers are dropped and the count is raised")
	require.Equal(t, int32(2), d.Headers[retryCountHeader], "the delivery is not modified")
}
