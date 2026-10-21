package review

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// Outcome is how an admin resolves a review (F6.1.3).
type Outcome string

const (
	// OutcomeCompleted pays the courier (credit.transfer).
	OutcomeCompleted Outcome = "COMPLETED"
	// OutcomeRefunded returns the reservation to the requester
	// (credit.release).
	OutcomeRefunded Outcome = "REFUNDED"
)

func (o Outcome) event() (errand.Event, bool) {
	switch o {
	case OutcomeCompleted:
		return errand.EventAdminComplete, true
	case OutcomeRefunded:
		return errand.EventAdminRefund, true
	default:
		return "", false
	}
}

// Resolution is a validated resolve request.
type Resolution struct {
	Outcome         Outcome
	Reason          string
	ExpectedVersion int
}

// Review is an errand_reviews row. Empty strings and a nil ResolvedAt stand
// for NULL columns; Resolution is empty while the review is open.
type Review struct {
	Reason           errand.ReviewReason
	ReporterID       string
	Note             string
	CreatedAt        time.Time
	Resolution       errand.Status
	ResolverID       string
	ResolutionReason string
	ResolvedAt       *time.Time
}

// AdminErrand is an errand as an admin sees it in review (AdminErrandView).
type AdminErrand struct {
	Errand errand.Errand
	// Pickup is the errand's stored supplier version.
	Pickup errand.SupplierVersion
	Review Review
}

// ListQuery selects a page of open reviews. Reason, when non-empty, keeps
// only reviews with that reason. After is the last review ID of the previous
// page, 0 for the first page.
type ListQuery struct {
	Reason errand.ReviewReason
	After  int64
	Limit  int
}

// Page is one page of open reviews. Next is 0 on the last page; otherwise it
// is passed back as ListQuery.After.
type Page struct {
	Items []AdminErrand
	Next  int64
}

// Reviews is what the HTTP handler needs. adminID is the admin's sub. A
// refused request returns an *apperror.Error; any other error is an
// internal failure.
type Reviews interface {
	List(ctx context.Context, query ListQuery) (Page, error)
	Resolve(ctx context.Context, adminID, errandID string, resolution Resolution) (AdminErrand, error)
}

// OpenReviewLister reads open errand_reviews rows; *opsrepo.Repository
// implements it. Review rows are written by errand.Store with the
// transition, never by this package.
type OpenReviewLister interface {
	ListOpenReviews(ctx context.Context, afterID int64, limit int) ([]opsrepo.OpenReview, error)
}

// AuditWriter records admin actions; *opsrepo.Repository implements it.
type AuditWriter interface {
	InsertAudit(ctx context.Context, record opsrepo.AuditRecord) (opsrepo.AuditEntry, error)
}

// Audit log vocabulary for review actions.
const (
	AuditTargetType     = "errand"
	AuditActionComplete = "errand_review.complete"
	AuditActionRefund   = "errand_review.refund"
)

func (o Outcome) auditAction() string {
	if o == OutcomeCompleted {
		return AuditActionComplete
	}
	return AuditActionRefund
}

// Dependencies are the Service's collaborators; all are required.
type Dependencies struct {
	Store     errand.Store
	Reviews   OpenReviewLister
	Audit     AuditWriter
	Suppliers errand.SupplierGate
	Clock     errand.Clock
	Logger    *slog.Logger
}

// Service implements Reviews over the errand ports and opsrepo.
type Service struct {
	store     errand.Store
	reviews   OpenReviewLister
	audit     AuditWriter
	suppliers errand.SupplierGate
	clock     errand.Clock
	logger    *slog.Logger
}

var _ Reviews = (*Service)(nil)

func NewService(deps Dependencies) *Service {
	switch {
	case deps.Store == nil:
		panic("review: store is required")
	case deps.Reviews == nil:
		panic("review: review lister is required")
	case deps.Audit == nil:
		panic("review: audit writer is required")
	case deps.Suppliers == nil:
		panic("review: supplier gate is required")
	case deps.Clock == nil:
		panic("review: clock is required")
	case deps.Logger == nil:
		panic("review: logger is required")
	}
	return &Service{
		store:     deps.Store,
		reviews:   deps.Reviews,
		audit:     deps.Audit,
		suppliers: deps.Suppliers,
		clock:     deps.Clock,
		logger:    deps.Logger,
	}
}

// List returns up to query.Limit open reviews after query.After, oldest
// first. opsrepo has no reason filter, so List reads pages of open reviews
// and keeps the matching ones; the open set is the REVIEW_REQUIRED errands,
// which is small. A review resolved between the two reads (its errand has
// left REVIEW_REQUIRED) is skipped.
func (s *Service) List(ctx context.Context, query ListQuery) (Page, error) {
	if query.Limit < 1 || query.Limit > MaxPageSize {
		return Page{}, &apperror.Error{Code: apperror.InvalidArgument, Message: fmt.Sprintf("limit must be between 1 and %d", MaxPageSize)}
	}
	// One row more than the page shows whether another page exists.
	batchSize := query.Limit + 1
	versions := map[string]errand.SupplierVersion{}
	page := Page{Items: make([]AdminErrand, 0, query.Limit)}
	var last int64
	for after := query.After; ; {
		batch, err := s.reviews.ListOpenReviews(ctx, after, batchSize)
		if err != nil {
			return Page{}, fmt.Errorf("list open reviews: %w", err)
		}
		for _, open := range batch {
			after = open.ID
			if query.Reason != "" && open.Reason != query.Reason {
				continue
			}
			if len(page.Items) == query.Limit {
				page.Next = last
				return page, nil
			}
			item, ok, err := s.listItem(ctx, open, versions)
			if err != nil {
				return Page{}, err
			}
			if ok {
				page.Items = append(page.Items, item)
				last = open.ID
			}
		}
		if len(batch) < batchSize {
			return page, nil
		}
	}
}

func (s *Service) listItem(ctx context.Context, open opsrepo.OpenReview, versions map[string]errand.SupplierVersion) (AdminErrand, bool, error) {
	current, err := s.store.Get(ctx, open.ErrandID)
	if err != nil {
		return AdminErrand{}, false, fmt.Errorf("read errand %s: %w", open.ErrandID, err)
	}
	if current.Status != errand.StatusReviewRequired {
		return AdminErrand{}, false, nil
	}
	versionID := current.Details.SupplierVersionID
	pickup, ok := versions[versionID]
	if !ok {
		if pickup, err = s.pickup(ctx, current); err != nil {
			return AdminErrand{}, false, err
		}
		versions[versionID] = pickup
	}
	return AdminErrand{Errand: current, Pickup: pickup, Review: openReview(open)}, true, nil
}

// Resolve moves a REVIEW_REQUIRED errand to COMPLETED or REFUNDED through
// errand.Next and Store.Apply with the ADMIN actor, then audits it. Every
// read that can fail runs before Apply, so a refused or failed request
// changes nothing.
func (s *Service) Resolve(ctx context.Context, adminID, errandID string, resolution Resolution) (AdminErrand, error) {
	event, ok := resolution.Outcome.event()
	if !ok {
		return AdminErrand{}, validationError(apperror.Field{Field: "outcome", Message: "must be COMPLETED or REFUNDED"})
	}
	action := resolution.Outcome.auditAction()

	current, err := s.store.Get(ctx, errandID)
	if err != nil {
		return AdminErrand{}, s.refuse(ctx, adminID, action, errandID, err)
	}
	actor := errand.Actor{Type: errand.ActorAdmin, ID: adminID}
	transition, err := errand.Next(current, event, actor, errand.EventInput{Reason: resolution.Reason}, s.clock.Now())
	if err != nil {
		return AdminErrand{}, s.refuse(ctx, adminID, action, errandID, err)
	}
	// Next sets the compare-and-swap from the errand it was given; the
	// admin's view is expectedVersion (F3.3.7).
	if current.Version != resolution.ExpectedVersion {
		changed := &apperror.Error{Code: apperror.ErrandChanged, Message: "the errand has changed since it was shown; reload it"}
		return AdminErrand{}, s.refuse(ctx, adminID, action, errandID, changed)
	}
	open, err := s.findOpenReview(ctx, errandID)
	if err != nil {
		return AdminErrand{}, s.refuse(ctx, adminID, action, errandID, err)
	}
	pickup, err := s.pickup(ctx, current)
	if err != nil {
		return AdminErrand{}, s.refuse(ctx, adminID, action, errandID, err)
	}

	stored, err := s.store.Apply(ctx, transition)
	if err != nil {
		return AdminErrand{}, s.refuse(ctx, adminID, action, errandID, err)
	}
	if _, err := s.audit.InsertAudit(ctx, opsrepo.AuditRecord{
		ActorID:    adminID,
		Action:     action,
		TargetType: AuditTargetType,
		TargetID:   errandID,
		Reason:     resolution.Reason,
		Outcome:    opsrepo.AuditSucceeded,
	}); err != nil {
		s.logger.ErrorContext(ctx, "review resolution audit failed",
			slog.String("errand_id", errandID),
			slog.String("action", action),
			slog.Any("error", err))
	}
	s.logger.InfoContext(ctx, "review resolved",
		slog.String("errand_id", errandID),
		slog.String("from", string(transition.From)),
		slog.String("to", string(stored.Status)),
		slog.Int("version", stored.Version))

	review := openReview(open)
	review.Resolution = stored.Status
	review.ResolverID = adminID
	review.ResolutionReason = resolution.Reason
	resolvedAt := transition.At
	review.ResolvedAt = &resolvedAt
	return AdminErrand{Errand: stored, Pickup: pickup, Review: review}, nil
}

// findOpenReview returns the errand's open review. opsrepo lists open
// reviews only in ID order, so it pages through them. None means the review
// was resolved since Next saw REVIEW_REQUIRED.
func (s *Service) findOpenReview(ctx context.Context, errandID string) (opsrepo.OpenReview, error) {
	for after := int64(0); ; {
		batch, err := s.reviews.ListOpenReviews(ctx, after, opsrepo.MaxPageSize)
		if err != nil {
			return opsrepo.OpenReview{}, fmt.Errorf("list open reviews: %w", err)
		}
		for _, open := range batch {
			if open.ErrandID == errandID {
				return open, nil
			}
			after = open.ID
		}
		if len(batch) < opsrepo.MaxPageSize {
			return opsrepo.OpenReview{}, &apperror.Error{Code: apperror.ErrandUnavailable, Message: "the review has already been resolved"}
		}
	}
}

// pickup reads the errand's stored supplier version. Versions are immutable
// and readable after deletion (F2.4.3), so a missing one is a data bug, not
// a client error.
func (s *Service) pickup(ctx context.Context, e errand.Errand) (errand.SupplierVersion, error) {
	version, err := s.suppliers.GetVersion(ctx, e.Details.SupplierVersionID)
	if err == nil {
		return version, nil
	}
	var appErr *apperror.Error
	if errors.As(err, &appErr) && appErr.Code == apperror.DependencyUnavailable {
		return errand.SupplierVersion{}, err
	}
	return errand.SupplierVersion{}, fmt.Errorf("read supplier version of errand %s: %w", e.ID, err)
}

// refuse audits a refused resolution as REJECTED, with the error code as
// reason, and returns err. Internal failures are returned as they are. A
// failed rejection audit is logged and does not change the answer.
func (s *Service) refuse(ctx context.Context, adminID, action, errandID string, err error) error {
	var appErr *apperror.Error
	if !errors.As(err, &appErr) {
		return err
	}
	if _, auditErr := s.audit.InsertAudit(ctx, opsrepo.AuditRecord{
		ActorID:    adminID,
		Action:     action,
		TargetType: AuditTargetType,
		TargetID:   errandID,
		Reason:     string(appErr.Code),
		Outcome:    opsrepo.AuditRejected,
	}); auditErr != nil {
		s.logger.ErrorContext(ctx, "review rejection audit failed",
			slog.String("errand_id", errandID),
			slog.String("action", action),
			slog.String("code", string(appErr.Code)),
			slog.Any("error", auditErr))
	}
	return err
}

func openReview(open opsrepo.OpenReview) Review {
	return Review{
		Reason:     open.Reason,
		ReporterID: open.ReporterID,
		Note:       open.Note,
		CreatedAt:  open.CreatedAt,
	}
}

func validationError(fields ...apperror.Field) error {
	return &apperror.Error{Code: apperror.ValidationFailed, Message: "request body is invalid", Fields: fields}
}
