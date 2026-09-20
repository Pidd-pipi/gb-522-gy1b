package service

import (
	"errors"
	"fmt"
	"strings"
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

func newRetireTestStore(t *testing.T) *repository.Store {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.FiberRoute{}, &model.TraceCapture{}, &model.EventMarker{}, &model.LocalizationCase{}, &model.AuditLog{}); err != nil {
		t.Fatal(err)
	}
	return repository.NewStore(db)
}

func retireActor() Actor {
	return Actor{ID: 7, Username: "reviewer", Role: constants.RoleReviewer, RequestID: "retire-test-request"}
}

func seedRoute(t *testing.T, store *repository.Store, code, status string) model.FiberRoute {
	t.Helper()
	route := model.FiberRoute{RouteCode: code, Name: "退役门禁测试线路", LengthM: 12000, RefractiveIndex: 1.468, LaunchConnector: "SC/APC", RouteStatus: status}
	if err := store.DB.Create(&route).Error; err != nil {
		t.Fatal(err)
	}
	return route
}

func seedTrace(t *testing.T, store *repository.Store, routeID uint) model.TraceCapture {
	t.Helper()
	trace := model.TraceCapture{RouteID: routeID, WavelengthNM: 1550, PulseWidthNS: 100, SampleIntervalNS: 100, RawPointsJSON: datatypes.JSON([]byte("[-20,-20.1]")), NoiseFloorDB: -40, DenoiseWindow: 5, PeakThresholdDB: 0.8, MergeWindow: 3, CapturedAt: time.Now(), UploadedBy: 1}
	if err := store.DB.Create(&trace).Error; err != nil {
		t.Fatal(err)
	}
	return trace
}

func seedCase(t *testing.T, store *repository.Store, routeID uint, status constants.CaseStatus) model.LocalizationCase {
	t.Helper()
	item := model.LocalizationCase{RouteID: routeID, BaselineTraceID: 1, CurrentTraceID: 2, CaseStatus: status, ParametersJSON: datatypes.JSON([]byte(`{"distance_tolerance_m":25,"loss_increase_db":0.5}`)), DifferencesJSON: datatypes.JSON([]byte("[]")), Version: 1, CreatedBy: 1}
	if err := store.DB.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	return item
}

func expectConflict(t *testing.T, err error) {
	t.Helper()
	var appErr *AppError
	if !errors.As(err, &appErr) || appErr.Code != CodeConflict {
		t.Fatalf("expected STATE_CONFLICT, got %v", err)
	}
}

func TestRetireRouteRejectsAnalyzingCases(t *testing.T) {
	store := newRetireTestStore(t)
	route := seedRoute(t, store, "RT-ANALYZING", model.RouteActive)
	seedCase(t, store, route.ID, constants.CaseAnalyzing)
	seedCase(t, store, route.ID, constants.CasePendingReview)

	_, err := NewRouteService(store).Retire(route.ID, retireActor())
	expectConflict(t, err)

	reloaded, getErr := store.Routes.Get(route.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if reloaded.RouteStatus != model.RouteActive {
		t.Fatalf("rejected retirement must leave the status untouched, got %s", reloaded.RouteStatus)
	}
	var audits int64
	if err := store.DB.Model(&model.AuditLog{}).Where("action = ?", "route.retired").Count(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if audits != 0 {
		t.Fatalf("rejected retirement must not write audit entries, got %d", audits)
	}
}

func TestRetireRouteWritesAuditAtomically(t *testing.T) {
	store := newRetireTestStore(t)
	route := seedRoute(t, store, "RT-RETIRE", model.RouteMaintenance)
	seedCase(t, store, route.ID, constants.CasePendingReview)
	service := NewRouteService(store)

	retired, err := service.Retire(route.ID, retireActor())
	if err != nil {
		t.Fatalf("retire failed: %v", err)
	}
	if retired.RouteStatus != model.RouteRetired {
		t.Fatalf("expected retired status, got %s", retired.RouteStatus)
	}
	reloaded, err := store.Routes.Get(route.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.RouteStatus != model.RouteRetired {
		t.Fatalf("retired status must be readable after reload, got %s", reloaded.RouteStatus)
	}
	var entry model.AuditLog
	if err := store.DB.Where("action = ? AND resource_id = ? AND route_id = ?", "route.retired", route.ID, route.ID).First(&entry).Error; err != nil {
		t.Fatalf("retirement audit missing: %v", err)
	}
	if !strings.Contains(entry.Before, "maintenance") || !strings.Contains(entry.After, "retired") {
		t.Fatalf("audit must capture the status transition, before=%s after=%s", entry.Before, entry.After)
	}
	if _, err := service.Retire(route.ID, retireActor()); err == nil {
		t.Fatal("retiring an already retired route must be rejected")
	} else {
		expectConflict(t, err)
	}
}

func TestRetiredRouteBlocksNewWork(t *testing.T) {
	store := newRetireTestStore(t)
	route := seedRoute(t, store, "RT-SEALED", model.RouteActive)
	baseline := seedTrace(t, store, route.ID)
	current := seedTrace(t, store, route.ID)
	routeService := NewRouteService(store)
	traceService := NewTraceService(store, 20000)
	caseService := NewCaseService(store)
	if _, err := routeService.Retire(route.ID, retireActor()); err != nil {
		t.Fatal(err)
	}

	points := make([]float64, 20)
	for i := range points {
		points[i] = -20 - float64(i)*0.01
	}
	_, err := traceService.Import(dto.ImportTraceRequest{RouteID: route.ID, WavelengthNM: 1550, PulseWidthNS: 100, SampleIntervalNS: 100, Points: points, CapturedAt: time.Now()}, retireActor())
	expectConflict(t, err)

	_, err = routeService.SetBaseline(route.ID, baseline.ID, retireActor())
	expectConflict(t, err)

	_, err = caseService.Create(dto.CreateCaseRequest{RouteID: route.ID, BaselineTraceID: baseline.ID, CurrentTraceID: current.ID}, retireActor())
	expectConflict(t, err)

	draft := seedCase(t, store, route.ID, constants.CaseDraft)
	_, err = caseService.Analyze(draft.ID, dto.AnalyzeCaseRequest{}, retireActor())
	expectConflict(t, err)
	reloaded, getErr := store.Cases.Get(draft.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if reloaded.CaseStatus != constants.CaseDraft {
		t.Fatalf("rejected re-analysis must keep the case in draft, got %s", reloaded.CaseStatus)
	}

	pending := seedCase(t, store, route.ID, constants.CasePendingReview)
	confirmed, err := caseService.Confirm(pending.ID, dto.ConfirmCaseRequest{Conclusion: "已知损耗点，与退役前基线一致", EstimatedDistanceM: 1024.5, UncertaintyM: 12, Version: pending.Version}, retireActor())
	if err != nil {
		t.Fatalf("pending review cases must stay confirmable on retired routes: %v", err)
	}
	if confirmed.CaseStatus != constants.CaseConfirmed {
		t.Fatalf("expected confirmed case, got %s", confirmed.CaseStatus)
	}
	closed, err := caseService.Close(confirmed.ID, dto.CloseCaseRequest{Version: confirmed.Version}, retireActor())
	if err != nil {
		t.Fatalf("confirmed cases must stay closable on retired routes: %v", err)
	}
	if closed.CaseStatus != constants.CaseClosed || closed.ClosedAt == nil {
		t.Fatalf("expected closed case with timestamp, got %s", closed.CaseStatus)
	}
}

func TestRouteStatusFlowKeepsMaintenanceAndSealsRetirement(t *testing.T) {
	store := newRetireTestStore(t)
	route := seedRoute(t, store, "RT-FLOW", model.RouteActive)
	service := NewRouteService(store)
	maintenance := model.RouteMaintenance

	updated, err := service.Update(route.ID, dto.UpdateRouteRequest{RouteStatus: &maintenance}, retireActor())
	if err != nil {
		t.Fatalf("maintenance transition must stay available: %v", err)
	}
	if updated.RouteStatus != model.RouteMaintenance {
		t.Fatalf("expected maintenance status, got %s", updated.RouteStatus)
	}

	retired := model.RouteRetired
	if _, err := service.Update(route.ID, dto.UpdateRouteRequest{RouteStatus: &retired}, retireActor()); err == nil {
		t.Fatal("retirement must go through the dedicated retire endpoint")
	} else {
		expectConflict(t, err)
	}

	if _, err := service.Retire(route.ID, retireActor()); err != nil {
		t.Fatal(err)
	}
	active := model.RouteActive
	if _, err := service.Update(route.ID, dto.UpdateRouteRequest{RouteStatus: &active}, retireActor()); err == nil {
		t.Fatal("retired routes must not return to service")
	} else {
		expectConflict(t, err)
	}

	name := fmt.Sprintf("退役线路档案-%d", route.ID)
	renamed, err := service.Update(route.ID, dto.UpdateRouteRequest{Name: &name}, retireActor())
	if err != nil {
		t.Fatalf("metadata edits must stay available on retired routes: %v", err)
	}
	if renamed.RouteStatus != model.RouteRetired || renamed.Name != name {
		t.Fatalf("unexpected retired route update: status=%s name=%s", renamed.RouteStatus, renamed.Name)
	}
}
