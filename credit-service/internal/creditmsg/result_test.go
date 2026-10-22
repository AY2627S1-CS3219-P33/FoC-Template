package creditmsg_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/creditmsg"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/wallet"
)

func TestBuildResult(t *testing.T) {
	at := time.Date(2026, 10, 14, 9, 0, 1, 123456789, time.FixedZone("x", 8*3600))
	reply := func(command string, version int64) wallet.Reply {
		return wallet.Reply{CommandMessageID: command, OrderID: errand, ErrandVersion: version}
	}
	for name, c := range map[string]struct {
		reply  wallet.Reply
		result wallet.Result
		want   string
	}{
		"reserved": {
			reply(errand+":reserve", 1), wallet.Result{Type: wallet.ResultReserved, Amount: 5},
			`{"messageId": "` + errand + `:reserve:credit.reserved", "correlationId": "` + errand + `:reserve",
			 "type": "credit.reserved", "occurredAt": "2026-10-14T01:00:01Z",
			 "errandId": "` + errand + `", "errandVersion": 1, "data": {"amount": 5}}`,
		},
		"reservation refused": {
			reply(errand+":reserve", 1), wallet.Result{Type: wallet.ResultReservationRefused, Code: "INSUFFICIENT_CREDITS"},
			`{"messageId": "` + errand + `:reserve:credit.reservation_refused", "correlationId": "` + errand + `:reserve",
			 "type": "credit.reservation_refused", "occurredAt": "2026-10-14T01:00:01Z",
			 "errandId": "` + errand + `", "errandVersion": 1, "data": {"code": "INSUFFICIENT_CREDITS"}}`,
		},
		"adjusted": {
			reply(errand+":adjust:2", 2), wallet.Result{Type: wallet.ResultAdjusted, Amount: 8, Seq: 4},
			`{"messageId": "` + errand + `:adjust:2:credit.adjusted", "correlationId": "` + errand + `:adjust:2",
			 "type": "credit.adjusted", "occurredAt": "2026-10-14T01:00:01Z",
			 "errandId": "` + errand + `", "errandVersion": 2, "data": {"amount": 8, "seq": 4}}`,
		},
		"adjust refused": {
			reply(errand+":adjust:2", 2), wallet.Result{Type: wallet.ResultAdjustRefused, Code: "RESERVATION_NOT_ACTIVE", Seq: 4},
			`{"messageId": "` + errand + `:adjust:2:credit.adjust_refused", "correlationId": "` + errand + `:adjust:2",
			 "type": "credit.adjust_refused", "occurredAt": "2026-10-14T01:00:01Z",
			 "errandId": "` + errand + `", "errandVersion": 2, "data": {"code": "RESERVATION_NOT_ACTIVE", "seq": 4}}`,
		},
		"released has empty data": {
			reply(errand+":release", 4), wallet.Result{Type: wallet.ResultReleased},
			`{"messageId": "` + errand + `:release:credit.released", "correlationId": "` + errand + `:release",
			 "type": "credit.released", "occurredAt": "2026-10-14T01:00:01Z",
			 "errandId": "` + errand + `", "errandVersion": 4, "data": {}}`,
		},
		"operation failed": {
			reply(errand+":release", 8), wallet.Result{Type: wallet.ResultOperationFailed, Code: "RESERVATION_ALREADY_TRANSFERRED"},
			`{"messageId": "` + errand + `:release:credit.operation_failed", "correlationId": "` + errand + `:release",
			 "type": "credit.operation_failed", "occurredAt": "2026-10-14T01:00:01Z",
			 "errandId": "` + errand + `", "errandVersion": 8, "data": {"code": "RESERVATION_ALREADY_TRANSFERRED"}}`,
		},
	} {
		env, err := creditmsg.BuildResult(c.reply, c.result, at)
		require.NoError(t, err, name)
		got, err := json.Marshal(env)
		require.NoError(t, err, name)
		require.JSONEq(t, c.want, string(got), name)
	}
}
