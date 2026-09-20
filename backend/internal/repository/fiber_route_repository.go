package repository

import (
	"errors"
	"fmt"
	"strings"

	"fiber-otdr-fault-localization/backend/internal/dto"
	"fiber-otdr-fault-localization/backend/internal/model"
	"gorm.io/gorm"
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

// Retire flips a non-retired route to retired. The predicate keeps concurrent
// retirements from overwriting each other; the boolean reports the transition.
func (r *FiberRouteRepository) Retire(id uint) (bool, error) {
	result := r.db.Model(&model.FiberRoute{}).Where("id = ? AND route_status <> ?", id, model.RouteRetired).Update("route_status", model.RouteRetired)
	if result.Error != nil {
		return false, fmt.Errorf("retire fiber route: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}
