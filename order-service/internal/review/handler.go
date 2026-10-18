package review

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

const (
	// DefaultPageSize and MaxPageSize are the Limit parameter's default and
	// maximum in openapi/common.yaml.
	DefaultPageSize = 25
	MaxPageSize     = 100

	// maxReasonLength is ReviewResolution.reason's maxLength.
	maxReasonLength = 500
	// maxBodyBytes bounds the resolve body: an outcome, a reason of at most
	// 500 characters (up to 4 bytes each) and a version.
	maxBodyBytes = 8 << 10
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Handler serves the review routes to admins.
type Handler struct {
	reviews Reviews
	guard   func(http.Handler) http.Handler
	logger  *slog.Logger
}

func NewHandler(reviews Reviews, guard *auth.Middleware, logger *slog.Logger) Handler {
	if reviews == nil {
		panic("review: reviews is required")
	}
	if guard == nil {
		panic("review: auth middleware is required")
	}
	if logger == nil {
		panic("review: logger is required")
	}
	return Handler{reviews: reviews, guard: guard.RequireReview, logger: logger}
}

// RegisterRoutes registers the review routes; each passes through
// auth.Middleware.RequireReview (a user token with errands:review) first.
func (h Handler) RegisterRoutes(router *http.ServeMux) {
	router.Handle("GET /admin/reviews", h.guard(http.HandlerFunc(h.list)))
	router.Handle("POST /admin/reviews/{errandId}/resolve", h.guard(http.HandlerFunc(h.resolve)))
}

type pageResponse struct {
	Items      []adminErrandResponse `json:"items"`
	NextCursor string                `json:"nextCursor,omitempty"`
}

// adminErrandResponse is AdminErrandView: ErrandFields plus viewerRole,
// requesterId, courierId and review. It has no contact person.
type adminErrandResponse struct {
	ErrandID             string              `json:"errandId"`
	Version              int                 `json:"version"`
	Status               errand.Status       `json:"status"`
	SupplierID           string              `json:"supplierId"`
	SupplierVersionID    string              `json:"supplierVersionId"`
	Pickup               pickupResponse      `json:"pickup"`
	DeliveryBuilding     buildingResponse    `json:"deliveryBuilding"`
	DeliveryDetail       string              `json:"deliveryDetail"`
	Items                json.RawMessage     `json:"items"`
	Notes                string              `json:"notes"`
	Reward               int                 `json:"reward"`
	CreatedAt            time.Time           `json:"createdAt"`
	UpdatedAt            time.Time           `json:"updatedAt"`
	AcceptanceDeadline   time.Time           `json:"acceptanceDeadline"`
	PickupDeadline       *time.Time          `json:"pickupDeadline,omitempty"`
	DeliveryDeadline     *time.Time          `json:"deliveryDeadline,omitempty"`
	DeliveredAt          *time.Time          `json:"deliveredAt,omitempty"`
	VerificationDeadline *time.Time          `json:"verificationDeadline,omitempty"`
	ReviewReason         errand.ReviewReason `json:"reviewReason,omitempty"`
	ViewerRole           string              `json:"viewerRole"`
	RequesterID          string              `json:"requesterId"`
	CourierID            string              `json:"courierId,omitempty"`
	Review               reviewResponse      `json:"review"`
}

type pickupResponse struct {
	Name                string  `json:"name"`
	Building            string  `json:"building"`
	Floor               string  `json:"floor"`
	LocationDescription string  `json:"locationDescription"`
	Latitude            float64 `json:"latitude"`
	Longitude           float64 `json:"longitude"`
}

// buildingResponse is DeliveryBuilding: only the id and name of the stored
// snapshot.
type buildingResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type reviewResponse struct {
	Reason           errand.ReviewReason `json:"reason"`
	ReporterID       string              `json:"reporterId,omitempty"`
	Note             string              `json:"note,omitempty"`
	CreatedAt        time.Time           `json:"createdAt"`
	Resolution       errand.Status       `json:"resolution,omitempty"`
	ResolverID       string              `json:"resolverId,omitempty"`
	ResolutionReason string              `json:"resolutionReason,omitempty"`
	ResolvedAt       *time.Time          `json:"resolvedAt,omitempty"`
}

func (h Handler) list(response http.ResponseWriter, request *http.Request) {
	if _, ok := adminID(response, request); !ok {
		return
	}
	query, ok := parseListQuery(response, request)
	if !ok {
		return
	}
	page, err := h.reviews.List(request.Context(), query)
	if err != nil {
		h.fail(response, request, "list", "", err)
		return
	}
	body := pageResponse{Items: make([]adminErrandResponse, 0, len(page.Items))}
	for _, item := range page.Items {
		body.Items = append(body.Items, adminErrandBody(item))
	}
	if page.Next > 0 {
		body.NextCursor = encodeCursor(page.Next, query.Reason)
	}
	writeJSON(response, http.StatusOK, body)
}

func (h Handler) resolve(response http.ResponseWriter, request *http.Request) {
	admin, ok := adminID(response, request)
	if !ok {
		return
	}
	errandID := request.PathValue("errandId")
	if !uuidPattern.MatchString(errandID) {
		writeError(response, apperror.InvalidArgument, "errandId must be a UUID")
		return
	}
	errandID = strings.ToLower(errandID)
	resolution, ok := decodeResolution(response, request)
	if !ok {
		return
	}
	resolved, err := h.reviews.Resolve(request.Context(), admin, errandID, resolution)
	if err != nil {
		h.fail(response, request, "resolve", errandID, err)
		return
	}
	writeJSON(response, http.StatusOK, adminErrandBody(resolved))
}

// adminID returns the admin's sub from the principal RequireReview stored.
func adminID(response http.ResponseWriter, request *http.Request) (string, bool) {
	principal, found := auth.PrincipalFromContext(request.Context())
	if !found || principal.Subject == "" {
		writeError(response, apperror.Unauthenticated, "authentication is required")
		return "", false
	}
	return principal.Subject, true
}

func parseListQuery(response http.ResponseWriter, request *http.Request) (ListQuery, bool) {
	parameters := request.URL.Query()
	query := ListQuery{Limit: DefaultPageSize}
	if parameters.Has("reason") {
		query.Reason = errand.ReviewReason(parameters.Get("reason"))
		if !validReviewReason(query.Reason) {
			writeError(response, apperror.InvalidArgument, "reason must be COURIER_ISSUE, REQUESTER_DISPUTE or DELIVERY_OVERDUE")
			return ListQuery{}, false
		}
	}
	if parameters.Has("limit") {
		limit, err := strconv.Atoi(parameters.Get("limit"))
		if err != nil || limit < 1 || limit > MaxPageSize {
			writeError(response, apperror.InvalidArgument, "limit must be between 1 and 100")
			return ListQuery{}, false
		}
		query.Limit = limit
	}
	if parameters.Has("cursor") {
		after, ok := decodeCursor(parameters.Get("cursor"), query.Reason)
		if !ok {
			writeError(response, apperror.InvalidArgument, "cursor is invalid")
			return ListQuery{}, false
		}
		query.After = after
	}
	return query, true
}

func validReviewReason(reason errand.ReviewReason) bool {
	switch reason {
	case errand.ReviewCourierIssue, errand.ReviewRequesterDispute, errand.ReviewDeliveryOverdue:
		return true
	default:
		return false
	}
}

type resolveRequest struct {
	Outcome         *string `json:"outcome"`
	Reason          *string `json:"reason"`
	ExpectedVersion *int    `json:"expectedVersion"`
}

// decodeResolution validates ReviewResolution and reports every invalid
// field at once (NFR2.2.1). A body that is not a JSON object is
// INVALID_ARGUMENT.
func decodeResolution(response http.ResponseWriter, request *http.Request) (Resolution, bool) {
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()

	var body resolveRequest
	if err := decoder.Decode(&body); err != nil {
		var typeErr *json.UnmarshalTypeError
		var sizeErr *http.MaxBytesError
		switch {
		case errors.Is(err, io.EOF):
			writeError(response, apperror.InvalidArgument, "request body is required")
		case errors.As(err, &sizeErr):
			writeError(response, apperror.InvalidArgument, "request body is too large")
		case errors.As(err, &typeErr) && typeErr.Field == "expectedVersion":
			writeValidation(response, apperror.Field{Field: "expectedVersion", Message: "must be a positive integer"})
		case errors.As(err, &typeErr) && (typeErr.Field == "outcome" || typeErr.Field == "reason"):
			writeValidation(response, apperror.Field{Field: typeErr.Field, Message: "must be a string"})
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			field := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
			writeValidation(response, apperror.Field{Field: field, Message: "is not allowed"})
		default:
			writeError(response, apperror.InvalidArgument, "request body must be a JSON object")
		}
		return Resolution{}, false
	}
	if decoder.More() {
		writeError(response, apperror.InvalidArgument, "request body must be a single JSON object")
		return Resolution{}, false
	}

	var fields []apperror.Field
	var resolution Resolution
	switch {
	case body.Outcome == nil:
		fields = append(fields, apperror.Field{Field: "outcome", Message: "is required"})
	case Outcome(*body.Outcome) != OutcomeCompleted && Outcome(*body.Outcome) != OutcomeRefunded:
		fields = append(fields, apperror.Field{Field: "outcome", Message: "must be COMPLETED or REFUNDED"})
	default:
		resolution.Outcome = Outcome(*body.Outcome)
	}
	switch {
	case body.Reason == nil:
		fields = append(fields, apperror.Field{Field: "reason", Message: "is required"})
	case strings.TrimSpace(*body.Reason) == "":
		fields = append(fields, apperror.Field{Field: "reason", Message: "must not be blank"})
	case utf8.RuneCountInString(*body.Reason) > maxReasonLength:
		fields = append(fields, apperror.Field{Field: "reason", Message: "must be at most 500 characters"})
	default:
		resolution.Reason = *body.Reason
	}
	switch {
	case body.ExpectedVersion == nil:
		fields = append(fields, apperror.Field{Field: "expectedVersion", Message: "is required"})
	case *body.ExpectedVersion < 1:
		fields = append(fields, apperror.Field{Field: "expectedVersion", Message: "must be a positive integer"})
	default:
		resolution.ExpectedVersion = *body.ExpectedVersion
	}
	if len(fields) > 0 {
		writeValidation(response, fields...)
		return Resolution{}, false
	}
	return resolution, true
}

func (h Handler) fail(response http.ResponseWriter, request *http.Request, operation, errandID string, err error) {
	var appErr *apperror.Error
	if errors.As(err, &appErr) {
		if appErr.Code == apperror.ValidationFailed {
			writeValidation(response, appErr.Fields...)
			return
		}
		writeError(response, appErr.Code, appErr.Message)
		return
	}
	attributes := []any{slog.String("operation", operation), slog.Any("error", err)}
	if errandID != "" {
		attributes = append(attributes, slog.String("errand_id", errandID))
	}
	h.logger.ErrorContext(request.Context(), "review request failed", attributes...)
	writeError(response, apperror.Internal, "an unexpected internal error occurred")
}

func adminErrandBody(item AdminErrand) adminErrandResponse {
	e := item.Errand
	review := item.Review
	return adminErrandResponse{
		ErrandID:          e.ID,
		Version:           e.Version,
		Status:            e.Status,
		SupplierID:        e.Details.SupplierID,
		SupplierVersionID: e.Details.SupplierVersionID,
		Pickup: pickupResponse{
			Name:                item.Pickup.Name,
			Building:            item.Pickup.Building,
			Floor:               item.Pickup.Floor,
			LocationDescription: item.Pickup.LocationDescription,
			Latitude:            item.Pickup.Latitude,
			Longitude:           item.Pickup.Longitude,
		},
		DeliveryBuilding:     deliveryBuilding(e.Details),
		DeliveryDetail:       e.Details.DeliveryDetail,
		Items:                e.Details.Items,
		Notes:                e.Details.Notes,
		Reward:               e.Details.Reward,
		CreatedAt:            e.CreatedAt,
		UpdatedAt:            e.UpdatedAt,
		AcceptanceDeadline:   e.Details.AcceptanceDeadline,
		PickupDeadline:       e.PickupDeadline,
		DeliveryDeadline:     e.DeliveryDeadline,
		DeliveredAt:          e.DeliveredAt,
		VerificationDeadline: e.VerificationDeadline,
		ReviewReason:         e.ReviewReason,
		ViewerRole:           string(errand.ActorAdmin),
		RequesterID:          e.RequesterID,
		CourierID:            e.CourierID,
		Review: reviewResponse{
			Reason:           review.Reason,
			ReporterID:       review.ReporterID,
			Note:             review.Note,
			CreatedAt:        review.CreatedAt,
			Resolution:       review.Resolution,
			ResolverID:       review.ResolverID,
			ResolutionReason: review.ResolutionReason,
			ResolvedAt:       review.ResolvedAt,
		},
	}
}

// deliveryBuilding projects the stored building snapshot onto
// DeliveryBuilding, which allows only id and name.
func deliveryBuilding(d errand.Details) buildingResponse {
	var snapshot struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(d.DeliveryBuildingSnapshot, &snapshot)
	return buildingResponse{ID: d.DeliveryBuildingID, Name: snapshot.Name}
}

func writeJSON(response http.ResponseWriter, status int, body any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(body)
}

func writeError(response http.ResponseWriter, code apperror.Code, message string) {
	auth.WriteError(response, code, message)
}

func writeValidation(response http.ResponseWriter, fields ...apperror.Field) {
	writeJSON(response, apperror.HTTPStatus(apperror.ValidationFailed), apperror.Error{
		Code:    apperror.ValidationFailed,
		Message: "request body is invalid",
		Fields:  fields,
	})
}
