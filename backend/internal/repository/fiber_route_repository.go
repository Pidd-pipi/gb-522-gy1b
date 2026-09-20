package repository

import (
	"errors"
	"fmt"
	"strings"

	"fiber-otdr-fault-localization/backend/internal/constants"
	"fiber-otdr-fault-localization/backend/internal/dto"
	"fiber-otdr-fault-localization/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type FiberRouteRepository struct{ db *gorm.DB }

func (r *FiberRouteRepository) Create(route *model.FiberRoute) error {
	if err := r.db.Create(route).Error; err != nil {
		return fmt.Errorf("create fiber route: %w", err)
	}
	return nil
}

func (r *FiberRouteRepository) Get(id uint) (model.FiberRoute, error) {
	var route model.FiberRoute
	if err := r.db.First(&route, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return route, ErrNotFound
		}
		return route, fmt.Errorf("get fiber route: %w", err)
	}
	return route, nil
}

func (r *FiberRouteRepository) GetByCode(code string) (model.FiberRoute, error) {
	var route model.FiberRoute
	if err := r.db.Where("route_code = ?", code).First(&route).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return route, ErrNotFound
		}
		return route, fmt.Errorf("get fiber route by code: %w", err)
	}
	return route, nil
}

func (r *FiberRouteRepository) List(query dto.RouteQuery) ([]model.FiberRoute, int64, error) {
	db := r.db.Model(&model.FiberRoute{})
	if keyword := strings.TrimSpace(query.Keyword); keyword != "" {
		like := "%" + keyword + "%"
		db = db.Where("route_code LIKE ? OR name LIKE ?", like, like)
	}
	if query.Status != "" {
		db = db.Where("route_status = ?", query.Status)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count fiber routes: %w", err)
	}
	var routes []model.FiberRoute
	if err := db.Order("created_at DESC").Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize).Find(&routes).Error; err != nil {
		return nil, 0, fmt.Errorf("list fiber routes: %w", err)
	}
	return routes, total, nil
}

func (r *FiberRouteRepository) Update(route *model.FiberRoute) error {
	result := r.db.Model(&model.FiberRoute{}).Where("id = ?", route.ID).Updates(map[string]any{"name": route.Name, "length_m": route.LengthM, "refractive_index": route.RefractiveIndex, "launch_connector": route.LaunchConnector, "route_status": route.RouteStatus})
	if result.Error != nil {
		return fmt.Errorf("update fiber route: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *FiberRouteRepository) SetBaseline(routeID, traceID uint) error {
	result := r.db.Model(&model.FiberRoute{}).Where("id = ?", routeID).Update("baseline_trace_id", traceID)
	if result.Error != nil {
		return fmt.Errorf("set route baseline: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// LockForUpdate serializes mutating actions (retirement, trace import, case
// analysis) on one route so the retired-state seal cannot be bypassed by a
// concurrent transaction. FOR UPDATE is unavailable on SQLite, where the
// connection is serialized anyway.
func (r *FiberRouteRepository) LockForUpdate(routeID uint) error {
	query := r.db.Model(&model.FiberRoute{}).Where("id = ?", routeID)
	if r.db.Dialector.Name() == "sqlite" {
		query = query.Select("id")
	} else {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.First(&model.FiberRoute{}).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("lock fiber route: %w", err)
	}
	return nil
}

// Retire performs the guarded status flip. The NOT ('retired') predicate turns
// duplicate retirement into RowsAffected == 0 instead of a second audit row.
func (r *FiberRouteRepository) Retire(routeID uint) error {
	result := r.db.Model(&model.FiberRoute{}).
		Where("id = ? AND route_status <> ?", routeID, string(constants.RouteRetired)).
		Update("route_status", string(constants.RouteRetired))
	if result.Error != nil {
		return fmt.Errorf("retire fiber route: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *FiberRouteRepository) CountCasesByStatus(routeID uint, statuses ...constants.CaseStatus) (int64, error) {
	if len(statuses) == 0 {
		return 0, nil
	}
	var count int64
	if err := r.db.Model(&model.LocalizationCase{}).
		Where("route_id = ? AND case_status IN ?", routeID, statuses).
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count route cases by status: %w", err)
	}
	return count, nil
}
