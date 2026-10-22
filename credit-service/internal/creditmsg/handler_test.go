package creditmsg_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/creditmsg"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/wallet"
)

const errand = "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c10"

// fakeCommands records the last call and returns err.
type fakeCommands struct {
	err       error
	messageID string
	reserve   *wallet.ReserveCommand
	adjust    *wallet.AdjustCommand
	release   *wallet.ReleaseCommand
	transfer  *wallet.TransferCommand
}

func (f *fakeCommands) Reserve(_ context.Context, id string, c wallet.ReserveCommand) (wallet.Result, error) {
	f.messageID, f.reserve = id, &c
	return wallet.Result{}, f.err
}

func (f *fakeCommands) Adjust(_ context.Context, id string, c wallet.AdjustCommand) (wallet.Result, error) {
	f.messageID, f.adjust = id, &c
	return wallet.Result{}, f.err
}

func (f *fakeCommands) Release(_ context.Context, id string, c wallet.ReleaseCommand) (wallet.Result, error) {
	f.messageID, f.release = id, &c
	return wallet.Result{}, f.err
}

func (f *fakeCommands) Transfer(_ context.Context, id string, c wallet.TransferCommand) (wallet.Result, error) {
	f.messageID, f.transfer = id, &c
	return wallet.Result{}, f.err
}

func message(messageID, typ string, version int, data string) []byte {
	return fmt.Appendf(nil, `{"messageId": %q, "type": %q, "occurredAt": "2026-10-14T09:00:00Z",
		"errandId": %q, "errandVersion": %d, "data": %s}`, messageID, typ, errand, version, data)
}

func TestHandleReserve(t *testing.T) {
	fake := &fakeCommands{}
	body := message(errand+":reserve", "credit.reserve", 1,
		fmt.Sprintf(`{"errandId": %q, "errandVersion": 1, "requesterId": "auth0|req1", "amount": 5}`, errand))

	require.NoError(t, creditmsg.NewHandler(fake).Handle(context.Background(), "credit.reserve", body))
	require.Equal(t, errand+":reserve", fake.messageID)
	require.Equal(t, &wallet.ReserveCommand{
		OrderID: errand, ErrandVersion: 1, RequesterSubject: "auth0|req1", Amount: 5,
	}, fake.reserve)
}

func TestHandleAdjust(t *testing.T) {
	fake := &fakeCommands{}
	body := message(errand+":adjust:2", "credit.adjust", 2,
		fmt.Sprintf(`{"errandId": %q, "errandVersion": 2, "requesterId": "auth0|req1", "newAmount": 8, "seq": 4}`, errand))

	require.NoError(t, creditmsg.NewHandler(fake).Handle(context.Background(), "credit.adjust", body))
	require.Equal(t, &wallet.AdjustCommand{
		OrderID: errand, ErrandVersion: 2, RequesterSubject: "auth0|req1", NewAmount: 8, Seq: 4,
	}, fake.adjust)
}

func TestHandleRelease(t *testing.T) {
	fake := &fakeCommands{}
	body := message(errand+":release", "credit.release", 4,
		fmt.Sprintf(`{"errandId": %q, "errandVersion": 4, "requesterId": "auth0|req1"}`, errand))

	require.NoError(t, creditmsg.NewHandler(fake).Handle(context.Background(), "credit.release", body))
	require.Equal(t, &wallet.ReleaseCommand{
		OrderID: errand, ErrandVersion: 4, RequesterSubject: "auth0|req1",
	}, fake.release)
}

func TestHandleTransfer(t *testing.T) {
	fake := &fakeCommands{}
	body := message(errand+":transfer", "credit.transfer", 7,
		fmt.Sprintf(`{"errandId": %q, "errandVersion": 7, "requesterId": "auth0|req1", "courierId": "auth0|cou1", "amount": 8}`, errand))

	require.NoError(t, creditmsg.NewHandler(fake).Handle(context.Background(), "credit.transfer", body))
	require.Equal(t, &wallet.TransferCommand{
		OrderID: errand, ErrandVersion: 7, RequesterSubject: "auth0|req1", CourierSubject: "auth0|cou1", Amount: 8,
	}, fake.transfer)
}

func TestHandleIgnoresUnknownFields(t *testing.T) {
	fake := &fakeCommands{}
	body := []byte(fmt.Sprintf(`{"messageId": "m", "type": "credit.release", "occurredAt": "2026-10-14T09:00:00Z",
		"errandId": %q, "errandVersion": 1, "extra": true,
		"data": {"errandId": %q, "errandVersion": 1, "requesterId": "auth0|req1", "later": 1}}`, errand, errand))

	require.NoError(t, creditmsg.NewHandler(fake).Handle(context.Background(), "credit.release", body))
	require.NotNil(t, fake.release)
}

func TestHandleRejectsBadMessages(t *testing.T) {
	data := fmt.Sprintf(`{"errandId": %q, "errandVersion": 1, "requesterId": "auth0|req1", "amount": 5}`, errand)
	other := "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c99"
	for name, c := range map[string]struct {
		routingKey string
		body       []byte
	}{
		"not json":              {"credit.reserve", []byte("{")},
		"empty body":            {"credit.reserve", nil},
		"unknown routing key":   {"credit.refund", message("m", "credit.refund", 1, data)},
		"type differs from key": {"credit.reserve", message("m", "credit.release", 1, data)},
		"no message id":         {"credit.reserve", message("", "credit.reserve", 1, data)},
		"version zero":          {"credit.reserve", message("m", "credit.reserve", 0, data)},
		"data missing":          {"credit.reserve", message("m", "credit.reserve", 1, "null")},
		"data wrong shape":      {"credit.reserve", message("m", "credit.reserve", 1, `[1]`)},
		"amount not integer":    {"credit.reserve", message("m", "credit.reserve", 1, fmt.Sprintf(`{"errandId": %q, "errandVersion": 1, "requesterId": "a", "amount": 5.5}`, errand))},
		"data errand differs":   {"credit.reserve", message("m", "credit.reserve", 1, fmt.Sprintf(`{"errandId": %q, "errandVersion": 1, "requesterId": "a", "amount": 5}`, other))},
		"data version differs":  {"credit.reserve", message("m", "credit.reserve", 1, fmt.Sprintf(`{"errandId": %q, "errandVersion": 2, "requesterId": "a", "amount": 5}`, errand))},
	} {
		fake := &fakeCommands{}
		err := creditmsg.NewHandler(fake).Handle(context.Background(), c.routingKey, c.body)
		require.ErrorIs(t, err, creditmsg.ErrUndecodable, name)
		require.Equal(t, creditmsg.Reject, creditmsg.DispositionOf(err), name)
		require.Nil(t, fake.reserve, name)
		require.Empty(t, fake.messageID, name)
	}
}

func TestDispositionOf(t *testing.T) {
	require.Equal(t, creditmsg.Ack, creditmsg.DispositionOf(nil))
	require.Equal(t, creditmsg.Reject, creditmsg.DispositionOf(creditmsg.ErrUndecodable))
	require.Equal(t, creditmsg.Reject, creditmsg.DispositionOf(fmt.Errorf("x: %w", wallet.ErrInvalid)))
	require.Equal(t, creditmsg.Reject, creditmsg.DispositionOf(fmt.Errorf("x: %w", wallet.ErrConflict)))
	require.Equal(t, creditmsg.Requeue, creditmsg.DispositionOf(errors.New("connection refused")))
	require.Equal(t, creditmsg.Requeue, creditmsg.DispositionOf(context.DeadlineExceeded))
}

func TestHandlePassesServiceErrorsOn(t *testing.T) {
	fake := &fakeCommands{err: wallet.ErrInvalid}
	body := message("m", "credit.release", 1,
		fmt.Sprintf(`{"errandId": %q, "errandVersion": 1, "requesterId": "auth0|req1"}`, errand))

	err := creditmsg.NewHandler(fake).Handle(context.Background(), "credit.release", body)
	require.ErrorIs(t, err, wallet.ErrInvalid)
	require.Equal(t, creditmsg.Reject, creditmsg.DispositionOf(err))
}
