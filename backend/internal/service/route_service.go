package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"fiber-otdr-fault-localization/backend/internal/constants"
	"fiber-otdr-fault-localization/backend/internal/dto"
	"fiber-otdr-fault-localization/backend/internal/model"
	"fiber-otdr-fault-localization/backend/internal/repository"
)

type RouteService struct{ store *repository.Store }

func NewRouteService(store *repository.Store) *RouteService { return &RouteService{store} }

func (s *RouteService) Create(request dto.CreateRouteRequest, actor Actor) (model.FiberRoute, error) {
	status := request.RouteStatus
	if status == "" {
		status = model.RouteActive
	}
	route := model.FiberRoute{RouteCode: strings.ToUpper(strings.TrimSpace(request.RouteCode)), Name: strings.TrimSpace(request.Name), LengthM: request.LengthM, RefractiveIndex: request.RefractiveIndex, LaunchConnector: strings.TrimSpace(request.LaunchConnector), RouteStatus: status}
	err := s.store.Transaction(func(tx *repository.Store) error {
		if _, err := tx.Routes.GetByCode(route.RouteCode); err == nil {
			return conflict("route code already exists", err)
		} else if !errors.Is(err, repository.ErrNotFound) {
			return err
		}
		if err := tx.Routes.Create(&route); err != nil {
			return err
		}
		return tx.Audits.Create(audit(actor, "route.created", "FiberRoute", route.ID, &route.ID, "{}", snapshot(route)))
	})
	if err != nil {
		var appErr *AppError
		if errors.As(err, &appErr) {
			return route, err
		}
		return route, internal("create route failed", err)
	}
	return route, nil
}

func (s *RouteService) List(query dto.RouteQuery) ([]model.FiberRoute, dto.Pagination, error) {
	normalizePage(&query.Page, &query.PageSize)
	items, total, err := s.store.Routes.List(query)
	if err != nil {
		return nil, dto.Pagination{}, internal("list routes failed", err)
	}
	return items, dto.Pagination{Page: query.Page, PageSize: query.PageSize, Total: total}, nil
}

func (s *RouteService) Get(id uint) (model.FiberRoute, []model.TraceCapture, error) {
	route, err := s.store.Routes.Get(id)
	if errors.Is(err, repository.ErrNotFound) {
		return route, nil, notFound("route")
	}
	if err != nil {
		return route, nil, internal("get route failed", err)
	}
	traces, _, err := s.store.Traces.List(dto.TraceQuery{RouteID: &id, Page: 1, PageSize: 100})
	if err != nil {
		return route, nil, internal("list route traces failed", err)
	}
	return route, traces, nil
}

func (s *RouteService) Update(id uint, request dto.UpdateRouteRequest, actor Actor) (model.FiberRoute, error) {
	before, err := s.store.Routes.Get(id)
	if errors.Is(err, repository.ErrNotFound) {
		return before, notFound("route")
	}
	if err != nil {
		return before, internal("get route failed", err)
	}
	beforeSnapshot := snapshot(before)
	after := before
	if request.Name != nil {
		after.Name = strings.TrimSpace(*request.Name)
	}
	if request.LengthM != nil {
		after.LengthM = *request.LengthM
	}
	if request.RefractiveIndex != nil {
		after.RefractiveIndex = *request.RefractiveIndex
	}
	if request.LaunchConnector != nil {
		after.LaunchConnector = strings.TrimSpace(*request.LaunchConnector)
	}
	if request.RouteStatus != nil {
		target := *request.RouteStatus
		if target == model.RouteRetired && before.RouteStatus != model.RouteRetired {
			return before, conflict("retire a route through the dedicated retire endpoint", nil)
		}
		if before.RouteStatus == model.RouteRetired && target != model.RouteRetired {
			return before, conflict("retired routes cannot return to service", nil)
		}
		after.RouteStatus = target
	}
	err = s.store.Transaction(func(tx *repository.Store) error {
		if err := tx.Routes.Update(&after); err != nil {
			return err
		}
		return tx.Audits.Create(audit(actor, "route.updated", "FiberRoute", id, &id, beforeSnapshot, snapshot(after)))
	})
	if err != nil {
		return after, internal("update route failed", err)
	}
	return after, nil
}

func (s *RouteService) SetBaseline(routeID, traceID uint, actor Actor) (model.FiberRoute, error) {
	route, err := s.store.Routes.Get(routeID)
	if errors.Is(err, repository.ErrNotFound) {
		return route, notFound("route")
	}
	if err != nil {
		return route, internal("get route failed", err)
	}
	if route.RouteStatus == model.RouteRetired {
		return route, conflict("retired routes cannot change baselines", nil)
	}
	belongs, err := s.store.Traces.BelongsToRoute(traceID, routeID)
	if err != nil {
		return route, internal("validate baseline trace failed", err)
	}
	if !belongs {
		return route, invalid("baseline trace must belong to the route", nil)
	}
	before := snapshot(map[string]any{"baseline_trace_id": route.BaselineTraceID})
	err = s.store.Transaction(func(tx *repository.Store) error {
		if err := tx.Routes.SetBaseline(routeID, traceID); err != nil {
			return err
		}
		return tx.Audits.Create(audit(actor, "route.baseline_changed", "FiberRoute", routeID, &routeID, before, snapshot(map[string]any{"baseline_trace_id": traceID})))
	})
	if err != nil {
		return route, internal("set baseline failed", err)
	}
	route.BaselineTraceID = &traceID
	return route, nil
}

// Retire seals a route against new traces, baselines and cases. The status
// change and its audit entry commit in one transaction; a route that still
// has analyzing cases is rejected without any state change.
func (s *RouteService) Retire(id uint, actor Actor) (model.FiberRoute, error) {
	route, err := s.store.Routes.Get(id)
	if errors.Is(err, repository.ErrNotFound) {
		return route, notFound("route")
	}
	if err != nil {
		return route, internal("get route failed", err)
	}
	if route.RouteStatus == model.RouteRetired {
		return route, conflict("route is already retired", nil)
	}
	before := snapshot(map[string]any{"route_status": route.RouteStatus})
	err = s.store.Transaction(func(tx *repository.Store) error {
		analyzing, err := tx.Cases.CountByRouteAndStatus(id, constants.CaseAnalyzing)
		if err != nil {
			return err
		}
		if analyzing > 0 {
			return conflict(fmt.Sprintf("route still has %d analyzing case(s)", analyzing), nil)
		}
		changed, err := tx.Routes.Retire(id)
		if err != nil {
			return err
		}
		if !changed {
			return conflict("route status changed while retiring", nil)
		}
		return tx.Audits.Create(audit(actor, "route.retired", "FiberRoute", id, &id, before, snapshot(map[string]any{"route_status": model.RouteRetired})))
	})
	if err != nil {
		var appErr *AppError
		if errors.As(err, &appErr) {
			return route, err
		}
		return route, internal("retire route failed", err)
	}
	route.RouteStatus = model.RouteRetired
	return route, nil
}

func normalizePage(page, size *int) {
	if *page < 1 {
		*page = 1
	}
	if *size < 1 {
		*size = 20
	}
	if *size > 100 {
		*size = 100
	}
}
func snapshot(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("{\"summary_error\":%q}", err.Error())
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return string(encoded)
	}
	scrubSnapshot(decoded)
	clean, err := json.Marshal(decoded)
	if err != nil {
		return string(encoded)
	}
	return string(clean)
}

func scrubSnapshot(value any) {
	const redacted = "[REDACTED]"
	sensitive := func(key string) bool {
		key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
		for _, token := range []string{"password", "passwd", "token", "authorization", "secret", "credential", "private_key", "apikey", "api_key"} {
			if strings.Contains(key, token) {
				return true
			}
		}
		return false
	}
	var walk func(any)
	walk = func(node any) {
		switch current := node.(type) {
		case map[string]any:
			for key, child := range current {
				if sensitive(key) {
					current[key] = redacted
					continue
				}
				walk(child)
			}
		case []any:
			for _, child := range current {
				walk(child)
			}
		}
	}
	walk(value)
}
func audit(actor Actor, action, resource string, resourceID uint, routeID *uint, before, after string) *model.AuditLog {
	return &model.AuditLog{ActorID: actor.ID, ActorName: actor.Username, Action: action, ResourceType: resource, ResourceID: resourceID, RouteID: routeID, RequestID: actor.RequestID, Before: before, After: after}
}
