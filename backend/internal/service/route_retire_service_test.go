package service

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"fiber-otdr-fault-localization/backend/internal/constants"
	"fiber-otdr-fault-localization/backend/internal/dto"
	"fiber-otdr-fault-localization/backend/internal/model"
	"fiber-otdr-fault-localization/backend/internal/repository"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func retirementStore(t *testing.T) *repository.Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:route-retire-test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.MigrateAndSeed(db); err != nil {
		t.Fatal(err)
	}
	return repository.NewStore(db)
}

func retirementActor() Actor {
	return Actor{ID: 2, Username: "reviewer", Role: constants.RoleReviewer, RequestID: "req-retire-test"}
}

func createTestRoute(t *testing.T, store *repository.Store, status constants.RouteStatus) model.FiberRoute {
	t.Helper()
	route := model.FiberRoute{RouteCode: "RETIRE-" + string(status) + "-" + t.Name(), Name: "退役门禁线路", LengthM: 20000, RefractiveIndex: 1.468, LaunchConnector: "SC/APC", RouteStatus: status}
	if err := store.Routes.Create(&route); err != nil {
		t.Fatal(err)
	}
	return route
}

func createTestTrace(t *testing.T, store *repository.Store, routeID uint) model.TraceCapture {
	t.Helper()
	points := make([]float64, 240)
	raw, _ := json.Marshal(points)
	trace := model.TraceCapture{RouteID: routeID, WavelengthNM: 1550, PulseWidthNS: 100, SampleIntervalNS: 10000, RawPointsJSON: datatypes.JSON(raw), ProcessedJSON: datatypes.JSON(raw), NoiseFloorDB: 1, CapturedAt: mustParseTime(t, "2026-09-01T00:00:00Z"), UploadedBy: 1, DenoiseWindow: 5, PeakThresholdDB: 0.8, MergeWindow: 3}
	if err := store.Traces.Create(&trace); err != nil {
		t.Fatal(err)
	}
	return trace
}

func createTestCase(t *testing.T, store *repository.Store, routeID, baselineID, currentID uint, status constants.CaseStatus) model.LocalizationCase {
	t.Helper()
	item := model.LocalizationCase{RouteID: routeID, BaselineTraceID: baselineID, CurrentTraceID: currentID, CaseStatus: status, ParametersJSON: datatypes.JSON([]byte(`{}`)), DifferencesJSON: datatypes.JSON([]byte(`[]`)), Version: 1, CreatedBy: 1}
	if err := store.Cases.Create(&item); err != nil {
		t.Fatal(err)
	}
	return item
}

func TestRetireSucceedsAndWritesAtomicAudit(t *testing.T) {
	store := retirementStore(t)
	route := createTestRoute(t, store, constants.RouteActive)
	routes := NewRouteService(store)

	got, err := routes.Retire(route.ID, retirementActor())
	if err != nil {
		t.Fatalf("retire failed: %v", err)
	}
	if got.RouteStatus != constants.RouteRetired {
		t.Fatalf("expected retired status, got %s", got.RouteStatus)
	}
	reloaded, err := store.Routes.Get(route.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.RouteStatus != constants.RouteRetired {
		t.Fatalf("retirement did not persist, status=%s", reloaded.RouteStatus)
	}
	_, total, err := store.Audits.List(dto.AuditQuery{RouteID: &route.ID, Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("expected exactly one retirement audit entry, got %d", total)
	}
}

func TestRetireRejectedWhenCaseAnalyzing(t *testing.T) {
	store := retirementStore(t)
	route := createTestRoute(t, store, constants.RouteActive)
	baseline := createTestTrace(t, store, route.ID)
	current := createTestTrace(t, store, route.ID)
	createTestCase(t, store, route.ID, baseline.ID, current.ID, constants.CaseAnalyzing)
	routes := NewRouteService(store)

	if _, err := routes.Retire(route.ID, retirementActor()); err == nil {
		t.Fatal("retirement must be rejected while a case is analyzing")
	} else {
		var appErr *AppError
		if !errors.As(err, &appErr) || appErr.Code != CodeConflict {
			t.Fatalf("expected STATE_CONFLICT, got %v", err)
		}
	}
	reloaded, err := store.Routes.Get(route.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.RouteStatus != constants.RouteActive {
		t.Fatalf("route status must remain unchanged, got %s", reloaded.RouteStatus)
	}
	_, total, err := store.Audits.List(dto.AuditQuery{RouteID: &route.ID, Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Fatalf("rejected retirement must not write audit rows, got %d", total)
	}
}

func TestRetiredRouteSealsMutations(t *testing.T) {
	store := retirementStore(t)
	route := createTestRoute(t, store, constants.RouteActive)
	baseline := createTestTrace(t, store, route.ID)
	current := createTestTrace(t, store, route.ID)
	pending := createTestCase(t, store, route.ID, baseline.ID, current.ID, constants.CasePendingReview)
	routes := NewRouteService(store)
	traces := NewTraceService(store, 20000)
	cases := NewCaseService(store)
	actor := retirementActor()

	if _, err := routes.Retire(route.ID, actor); err != nil {
		t.Fatalf("retire failed: %v", err)
	}

	// Trace import is sealed.
	_, err := traces.Import(dto.ImportTraceRequest{RouteID: route.ID, WavelengthNM: 1550, PulseWidthNS: 100, SampleIntervalNS: 10000, CapturedAt: mustParseTime(t, "2026-09-02T00:00:00Z"), Points: sealedPoints()}, actor)
	if !assertConflict(t, err, "trace import") {
		return
	}
	// Baseline changes are sealed.
	if _, err := routes.SetBaseline(route.ID, current.ID, actor); !assertConflict(t, err, "set baseline") {
		return
	}
	// New cases are sealed.
	_, err = cases.Create(dto.CreateCaseRequest{RouteID: route.ID, BaselineTraceID: baseline.ID, CurrentTraceID: current.ID}, actor)
	if !assertConflict(t, err, "create case") {
		return
	}
	// Re-analysis must fail even if a draft case exists.
	draft := createTestCase(t, store, route.ID, baseline.ID, current.ID, constants.CaseDraft)
	if _, err := cases.Analyze(draft.ID, dto.AnalyzeCaseRequest{}, actor); !assertConflict(t, err, "re-analyze draft") {
		return
	}
	// Re-retirement is idempotent-conflict.
	if _, err := routes.Retire(route.ID, actor); !assertConflict(t, err, "duplicate retire") {
		return
	}
	// Reopening via the generic update is sealed.
	maintenance := string(constants.RouteMaintenance)
	if _, err := routes.Update(route.ID, dto.UpdateRouteRequest{RouteStatus: &maintenance}, actor); !assertConflict(t, err, "reopen retired route") {
		return
	}
	// But an existing pending-review case can still be confirmed.
	conclusion := "封存前完成的人工复核结论"
	distance := 1234.0
	uncertainty := 25.0
	confirmed, err := cases.Confirm(pending.ID, dto.ConfirmCaseRequest{Version: pending.Version, Conclusion: conclusion, EstimatedDistanceM: distance, UncertaintyM: uncertainty}, actor)
	if err != nil {
		t.Fatalf("confirm on retired route must still work: %v", err)
	}
	if confirmed.CaseStatus != constants.CaseConfirmed {
		t.Fatalf("expected confirmed status, got %s", confirmed.CaseStatus)
	}
	// And the confirmed case can still be closed.
	closed, err := cases.Close(confirmed.ID, dto.CloseCaseRequest{Version: confirmed.Version}, actor)
	if err != nil {
		t.Fatalf("close on retired route must still work: %v", err)
	}
	if closed.CaseStatus != constants.CaseClosed {
		t.Fatalf("expected closed status, got %s", closed.CaseStatus)
	}
}

func TestMaintenanceFlowStillWorks(t *testing.T) {
	store := retirementStore(t)
	route := createTestRoute(t, store, constants.RouteActive)
	routes := NewRouteService(store)
	actor := Actor{ID: 1, Username: "analyst", Role: constants.RoleAnalyst, RequestID: "req-maintenance-test"}

	maintenance := string(constants.RouteMaintenance)
	updated, err := routes.Update(route.ID, dto.UpdateRouteRequest{RouteStatus: &maintenance}, actor)
	if err != nil {
		t.Fatalf("switching to maintenance failed: %v", err)
	}
	if updated.RouteStatus != constants.RouteMaintenance {
		t.Fatalf("expected maintenance, got %s", updated.RouteStatus)
	}
	active := string(constants.RouteActive)
	updated, err = routes.Update(route.ID, dto.UpdateRouteRequest{RouteStatus: &active}, actor)
	if err != nil {
		t.Fatalf("switching back to active failed: %v", err)
	}
	if updated.RouteStatus != constants.RouteActive {
		t.Fatalf("expected active, got %s", updated.RouteStatus)
	}
}

func assertConflict(t *testing.T, err error, action string) bool {
	t.Helper()
	if err == nil {
		t.Errorf("%s must be rejected on a retired route", action)
		return false
	}
	var appErr *AppError
	if !errors.As(err, &appErr) || appErr.Code != CodeConflict {
		t.Errorf("%s must return STATE_CONFLICT, got %v", action, err)
		return false
	}
	return true
}

func sealedPoints() []float64 {
	points := make([]float64, 240)
	for i := range points {
		points[i] = 20
	}
	return points
}
