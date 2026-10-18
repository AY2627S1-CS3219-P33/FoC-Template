package review

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

const (
	// maxSettingSeconds keeps a duration in seconds convertible to a
	// time.Duration.
	maxSettingSeconds = math.MaxInt64 / int64(time.Second)
	// maxSettingCount keeps a count within the int32 the schema stores.
	maxSettingCount = math.MaxInt32
	// maxUserIDLength is the userId parameter's maxLength.
	maxUserIDLength = 255
)

// ---------------------------------------------------------------------------
// GET/PUT /admin/settings
// ---------------------------------------------------------------------------

// settingsBody is the Settings schema: durations in whole seconds.
type settingsBody struct {
	PickupAfter               int64                     `json:"pickupAfter"`
	DeliveryAfter             int64                     `json:"deliveryAfter"`
	VerificationAfter         int64                     `json:"verificationAfter"`
	VerificationReminderAfter int64                     `json:"verificationReminderAfter"`
	WithdrawalRestriction     withdrawalRestrictionBody `json:"withdrawalRestriction"`
	CancellationFlag          cancellationFlagBody      `json:"cancellationFlag"`
	ReservationTimeout        int64                     `json:"reservationTimeout"`
	EditTimeout               int64                     `json:"editTimeout"`
	OutboxConfirmTimeout      int64                     `json:"outboxConfirmTimeout"`
}

type withdrawalRestrictionBody struct {
	Count    int64 `json:"count"`
	Window   int64 `json:"window"`
	Duration int64 `json:"duration"`
}

type cancellationFlagBody struct {
	Count  int64 `json:"count"`
	Window int64 `json:"window"`
}

func settingsResponse(s errand.SystemSettings) settingsBody {
	seconds := func(d time.Duration) int64 { return int64(d / time.Second) }
	return settingsBody{
		PickupAfter:               seconds(s.Deadlines.PickupAfter),
		DeliveryAfter:             seconds(s.Deadlines.DeliveryAfter),
		VerificationAfter:         seconds(s.Deadlines.VerificationAfter),
		VerificationReminderAfter: seconds(s.Deadlines.VerificationReminderAfter),
		WithdrawalRestriction: withdrawalRestrictionBody{
			Count:    int64(s.WithdrawalRestriction.Count),
			Window:   seconds(s.WithdrawalRestriction.Window),
			Duration: seconds(s.WithdrawalRestriction.Duration),
		},
		CancellationFlag: cancellationFlagBody{
			Count:  int64(s.CancellationFlag.Count),
			Window: seconds(s.CancellationFlag.Window),
		},
		ReservationTimeout:   seconds(s.ReservationTimeout),
		EditTimeout:          seconds(s.EditTimeout),
		OutboxConfirmTimeout: seconds(s.OutboxConfirmTimeout),
	}
}

func (h Handler) getSettings(response http.ResponseWriter, request *http.Request) {
	if _, ok := adminID(response, request); !ok {
		return
	}
	settings, err := h.settings.Get(request.Context())
	if err != nil {
		h.fail(response, request, "get settings", "", err)
		return
	}
	writeJSON(response, http.StatusOK, settingsResponse(settings))
}

func (h Handler) putSettings(response http.ResponseWriter, request *http.Request) {
	admin, ok := adminID(response, request)
	if !ok {
		return
	}
	settings, ok := decodeSettings(response, request)
	if !ok {
		return
	}
	stored, err := h.settings.Put(request.Context(), admin, settings)
	if err != nil {
		h.fail(response, request, "put settings", "", err)
		return
	}
	writeJSON(response, http.StatusOK, settingsResponse(stored))
}

type withdrawalRestrictionRequest struct {
	Count    *int64 `json:"count"`
	Window   *int64 `json:"window"`
	Duration *int64 `json:"duration"`
}

type cancellationFlagRequest struct {
	Count  *int64 `json:"count"`
	Window *int64 `json:"window"`
}

type settingsRequest struct {
	PickupAfter               *int64                        `json:"pickupAfter"`
	DeliveryAfter             *int64                        `json:"deliveryAfter"`
	VerificationAfter         *int64                        `json:"verificationAfter"`
	VerificationReminderAfter *int64                        `json:"verificationReminderAfter"`
	WithdrawalRestriction     *withdrawalRestrictionRequest `json:"withdrawalRestriction"`
	CancellationFlag          *cancellationFlagRequest      `json:"cancellationFlag"`
	ReservationTimeout        *int64                        `json:"reservationTimeout"`
	EditTimeout               *int64                        `json:"editTimeout"`
	OutboxConfirmTimeout      *int64                        `json:"outboxConfirmTimeout"`
}

// decodeSettings checks the body's shape: every field present, an integer
// and convertible. Whether the values make sense (positive, the reminder
// before auto-completion) is errand.SystemSettings.Validate's job in the
// service, so every semantic refusal is audited.
func decodeSettings(response http.ResponseWriter, request *http.Request) (errand.SystemSettings, bool) {
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()

	var body settingsRequest
	if err := decoder.Decode(&body); err != nil {
		var typeErr *json.UnmarshalTypeError
		var sizeErr *http.MaxBytesError
		switch {
		case errors.Is(err, io.EOF):
			writeError(response, apperror.InvalidArgument, "request body is required")
		case errors.As(err, &sizeErr):
			writeError(response, apperror.InvalidArgument, "request body is too large")
		case errors.As(err, &typeErr) && typeErr.Field != "":
			message := "must be an integer"
			if typeErr.Field == "withdrawalRestriction" || typeErr.Field == "cancellationFlag" {
				message = "must be an object"
			}
			writeValidation(response, apperror.Field{Field: typeErr.Field, Message: message})
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			field := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
			writeValidation(response, apperror.Field{Field: field, Message: "is not allowed"})
		default:
			writeError(response, apperror.InvalidArgument, "request body must be a JSON object")
		}
		return errand.SystemSettings{}, false
	}
	if decoder.More() {
		writeError(response, apperror.InvalidArgument, "request body must be a single JSON object")
		return errand.SystemSettings{}, false
	}

	var fields []apperror.Field
	seconds := func(name string, value *int64) time.Duration {
		switch {
		case value == nil:
			fields = append(fields, apperror.Field{Field: name, Message: "is required"})
		case *value > maxSettingSeconds:
			fields = append(fields, apperror.Field{Field: name, Message: "is too large"})
		default:
			return time.Duration(*value) * time.Second
		}
		return 0
	}
	count := func(name string, value *int64) int {
		switch {
		case value == nil:
			fields = append(fields, apperror.Field{Field: name, Message: "is required"})
		case *value > maxSettingCount || *value < math.MinInt32:
			fields = append(fields, apperror.Field{Field: name, Message: "is too large"})
		default:
			return int(*value)
		}
		return 0
	}

	var settings errand.SystemSettings
	settings.Deadlines.PickupAfter = seconds("pickupAfter", body.PickupAfter)
	settings.Deadlines.DeliveryAfter = seconds("deliveryAfter", body.DeliveryAfter)
	settings.Deadlines.VerificationAfter = seconds("verificationAfter", body.VerificationAfter)
	settings.Deadlines.VerificationReminderAfter = seconds("verificationReminderAfter", body.VerificationReminderAfter)
	if w := body.WithdrawalRestriction; w == nil {
		fields = append(fields, apperror.Field{Field: "withdrawalRestriction", Message: "is required"})
	} else {
		settings.WithdrawalRestriction.Count = count("withdrawalRestriction.count", w.Count)
		settings.WithdrawalRestriction.Window = seconds("withdrawalRestriction.window", w.Window)
		settings.WithdrawalRestriction.Duration = seconds("withdrawalRestriction.duration", w.Duration)
	}
	if c := body.CancellationFlag; c == nil {
		fields = append(fields, apperror.Field{Field: "cancellationFlag", Message: "is required"})
	} else {
		settings.CancellationFlag.Count = count("cancellationFlag.count", c.Count)
		settings.CancellationFlag.Window = seconds("cancellationFlag.window", c.Window)
	}
	settings.ReservationTimeout = seconds("reservationTimeout", body.ReservationTimeout)
	settings.EditTimeout = seconds("editTimeout", body.EditTimeout)
	settings.OutboxConfirmTimeout = seconds("outboxConfirmTimeout", body.OutboxConfirmTimeout)
	if len(fields) > 0 {
		writeValidation(response, fields...)
		return errand.SystemSettings{}, false
	}
	return settings, true
}

// ---------------------------------------------------------------------------
// GET /admin/errand-events
// ---------------------------------------------------------------------------

type transitionPageResponse struct {
	Items      []transitionResponse `json:"items"`
	NextCursor string               `json:"nextCursor,omitempty"`
}

// transitionResponse is TransitionRecord. courier_id is not part of it.
type transitionResponse struct {
	ID            int64            `json:"id"`
	ErrandID      string           `json:"errandId"`
	FromStatus    errand.Status    `json:"fromStatus,omitempty"`
	ToStatus      errand.Status    `json:"toStatus"`
	Event         errand.Event     `json:"event"`
	ActorType     errand.ActorType `json:"actorType"`
	ActorID       string           `json:"actorId,omitempty"`
	Reason        string           `json:"reason,omitempty"`
	ErrandVersion int              `json:"errandVersion"`
	At            time.Time        `json:"at"`
}

func (h Handler) listEvents(response http.ResponseWriter, request *http.Request) {
	if _, ok := adminID(response, request); !ok {
		return
	}
	query, ok := parseEventQuery(response, request)
	if !ok {
		return
	}
	page, err := h.events.List(request.Context(), query)
	if err != nil {
		h.fail(response, request, "list events", "", err)
		return
	}
	body := transitionPageResponse{Items: make([]transitionResponse, 0, len(page.Items))}
	for _, t := range page.Items {
		body.Items = append(body.Items, transitionResponse{
			ID:            t.ID,
			ErrandID:      t.ErrandID,
			FromStatus:    t.FromStatus,
			ToStatus:      t.ToStatus,
			Event:         t.Event,
			ActorType:     t.ActorType,
			ActorID:       t.ActorID,
			Reason:        t.Reason,
			ErrandVersion: t.ErrandVersion,
			At:            t.At,
		})
	}
	if page.Next != nil {
		body.NextCursor = encodeEventCursor(*page.Next, query)
	}
	writeJSON(response, http.StatusOK, body)
}

func parseEventQuery(response http.ResponseWriter, request *http.Request) (EventQuery, bool) {
	parameters := request.URL.Query()
	query := EventQuery{Limit: DefaultPageSize}
	invalidArgument := func(message string) (EventQuery, bool) {
		writeError(response, apperror.InvalidArgument, message)
		return EventQuery{}, false
	}
	if parameters.Has("userId") {
		query.UserID = parameters.Get("userId")
		if query.UserID == "" || len(query.UserID) > maxUserIDLength {
			return invalidArgument("userId must be 1 to 255 characters")
		}
	}
	if parameters.Has("errandId") {
		if !uuidPattern.MatchString(parameters.Get("errandId")) {
			return invalidArgument("errandId must be a UUID")
		}
		query.ErrandID = strings.ToLower(parameters.Get("errandId"))
	}
	if parameters.Has("type") {
		query.Type = errand.Status(parameters.Get("type"))
		if !query.Type.Valid() {
			return invalidArgument("type must be an errand status")
		}
	}
	for _, bound := range []struct {
		name   string
		target *time.Time
	}{{"from", &query.From}, {"to", &query.To}} {
		if !parameters.Has(bound.name) {
			continue
		}
		value, err := time.Parse(time.RFC3339Nano, parameters.Get(bound.name))
		if err != nil {
			return invalidArgument(bound.name + " must be an RFC 3339 date-time")
		}
		*bound.target = value
	}
	if !query.From.IsZero() && !query.To.IsZero() && !query.From.Before(query.To) {
		return invalidArgument("from must be before to")
	}
	if parameters.Has("limit") {
		limit, err := strconv.Atoi(parameters.Get("limit"))
		if err != nil || limit < 1 || limit > MaxPageSize {
			return invalidArgument("limit must be between 1 and 100")
		}
		query.Limit = limit
	}
	if parameters.Has("cursor") {
		before, ok := decodeEventCursor(parameters.Get("cursor"), query)
		if !ok {
			return invalidArgument("cursor is invalid")
		}
		query.Before = &before
	}
	return query, true
}

// ---------------------------------------------------------------------------
// GET /admin/flags
// ---------------------------------------------------------------------------

type flagListResponse struct {
	Items []flagResponse `json:"items"`
}

type flagResponse struct {
	UserID    string    `json:"userId"`
	Kind      FlagKind  `json:"kind"`
	Count     int       `json:"count"`
	Threshold int       `json:"threshold"`
	Window    int64     `json:"window"`
	Since     time.Time `json:"since"`
	Summary   string    `json:"summary"`
}

func (h Handler) listFlags(response http.ResponseWriter, request *http.Request) {
	if _, ok := adminID(response, request); !ok {
		return
	}
	flags, err := h.flags.List(request.Context())
	if err != nil {
		h.fail(response, request, "list flags", "", err)
		return
	}
	body := flagListResponse{Items: make([]flagResponse, 0, len(flags))}
	for _, f := range flags {
		body.Items = append(body.Items, flagResponse{
			UserID:    f.UserID,
			Kind:      f.Kind,
			Count:     f.Count,
			Threshold: f.Threshold,
			Window:    int64(f.Window / time.Second),
			Since:     f.Since,
			Summary:   f.Summary,
		})
	}
	writeJSON(response, http.StatusOK, body)
}
