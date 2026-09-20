package constants

type CaseStatus string

const (
	CaseDraft         CaseStatus = "draft"
	CaseAnalyzing     CaseStatus = "analyzing"
	CasePendingReview CaseStatus = "pending_review"
	CaseConfirmed     CaseStatus = "confirmed"
	CaseClosed        CaseStatus = "closed"
)

var transitions = map[CaseStatus]map[CaseStatus]bool{
	CaseDraft:         {CaseAnalyzing: true},
	CaseAnalyzing:     {CaseDraft: true, CasePendingReview: true},
	CasePendingReview: {CaseConfirmed: true},
	CaseConfirmed:     {CaseClosed: true},
	CaseClosed:        {},
}

func (s CaseStatus) Valid() bool {
	_, ok := transitions[s]
	return ok
}

func CanTransition(from, to CaseStatus) bool { return transitions[from][to] }

const (
	RoleAnalyst  = "analyst"
	RoleReviewer = "reviewer"
	RoleAdmin    = "admin"
)

func ValidRole(role string) bool {
	return role == RoleAnalyst || role == RoleReviewer || role == RoleAdmin
}

// RouteStatus tracks the maintenance lifecycle of a fiber route. Retired is a
// terminal seal enforced by the service layer: it can only be reached through
// the dedicated retire endpoint and blocks imports, baselines, new cases and
// re-analysis.
type RouteStatus string

const (
	RouteActive      RouteStatus = "active"
	RouteMaintenance RouteStatus = "maintenance"
	RouteRetired     RouteStatus = "retired"
)

func (s RouteStatus) Valid() bool {
	switch s {
	case RouteActive, RouteMaintenance, RouteRetired:
		return true
	default:
		return false
	}
}
