package access

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"oneinstack/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const RoleSuperAdmin = "super_admin"

const (
	MenuFeatureTerminal = "terminal"
	MenuFeatureBastion  = "bastion"
)

var roleKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
var menuKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,95}$`)
var iconKeyPattern = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)

var (
	ErrRoleNotFound                  = errors.New("role not found")
	ErrRoleExists                    = errors.New("role already exists")
	ErrRoleBuiltin                   = errors.New("built-in role cannot be changed")
	ErrRoleInUse                     = errors.New("role is assigned to users")
	ErrRoleKeyInvalid                = errors.New("role key is invalid")
	ErrRoleNameRequired              = errors.New("role name is required")
	ErrRoleDescriptionInvalid        = errors.New("role description is invalid")
	ErrPermissionNotFound            = errors.New("permission is not registered")
	ErrMenuNotFound                  = errors.New("menu not found")
	ErrMenuExists                    = errors.New("menu already exists")
	ErrMenuBuiltin                   = errors.New("built-in menu cannot be changed")
	ErrMenuKeyInvalid                = errors.New("menu key is invalid")
	ErrMenuTypeInvalid               = errors.New("menu type is invalid")
	ErrMenuParentInvalid             = errors.New("menu parent is invalid")
	ErrMenuCycle                     = errors.New("menu hierarchy contains a cycle")
	ErrMenuTargetInvalid             = errors.New("menu target is not registered")
	ErrMenuPermissionRequired        = errors.New("menu permission is required")
	ErrMenuHasChildren               = errors.New("menu has child nodes")
	ErrMenuFeatureInvalid            = errors.New("menu feature is invalid")
	ErrMenuIconInvalid               = errors.New("menu icon is invalid")
	ErrCannotDemoteCurrentSuperAdmin = errors.New("cannot remove super administrator from current user")
)

type PermissionSummary struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Module      string `json:"module,omitempty"`
	Action      string `json:"action,omitempty"`
}

// MenuNode is the stable backend contract for both the administrative menu
// catalog and the current user's filtered menu tree.
type MenuNode struct {
	ID             uint64              `json:"id"`
	Key            string              `json:"key"`
	ParentKey      string              `json:"parentKey,omitempty"`
	Type           string              `json:"type"`
	Name           string              `json:"name"`
	NameEn         string              `json:"nameEn,omitempty"`
	TargetType     string              `json:"targetType,omitempty"`
	TargetKey      string              `json:"targetKey,omitempty"`
	IconKey        string              `json:"iconKey,omitempty"`
	Sort           int                 `json:"sort"`
	Enabled        bool                `json:"enabled"`
	Builtin        bool                `json:"builtin"`
	SuperAdminOnly bool                `json:"superAdminOnly,omitempty"`
	FeatureKey     string              `json:"featureKey,omitempty"`
	Permissions    []PermissionSummary `json:"permissions,omitempty"`
	Children       []MenuNode          `json:"children,omitempty"`
}

type RoleInput struct {
	Key             string
	Code            string
	Name            string
	Description     string
	PermissionCodes []string
}

type RoleDetail struct {
	RoleSummary
	MenuTree []MenuNode `json:"menuTree"`
}

type MenuInput struct {
	Key             string
	ParentKey       string
	Type            string
	Name            string
	NameEn          string
	TargetType      string
	TargetKey       string
	IconKey         string
	Sort            int
	Enabled         bool
	SuperAdminOnly  bool
	FeatureKey      string
	PermissionCodes []string
}

func permissionAction(code string) string {
	parts := strings.Split(strings.TrimSpace(code), ".")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func permissionSummary(permission models.Permission) PermissionSummary {
	return PermissionSummary{
		Code:        permission.Code,
		Name:        permission.Name,
		Description: permission.Description,
		Module:      permission.Module,
		Action:      permissionAction(permission.Code),
	}
}

func knownPermissionCodes() map[string]struct{} {
	result := make(map[string]struct{}, len(builtinPermissions()))
	for _, permission := range builtinPermissions() {
		result[permission.Code] = struct{}{}
	}
	return result
}

func normalizePermissionCodes(codes []string) []string {
	result := make([]string, 0, len(codes))
	seen := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		if _, exists := seen[code]; exists {
			continue
		}
		seen[code] = struct{}{}
		result = append(result, code)
	}
	sort.Strings(result)
	return result
}

func validatePermissionCodes(codes []string) ([]string, error) {
	normalized := normalizePermissionCodes(codes)
	known := knownPermissionCodes()
	for _, code := range normalized {
		if _, ok := known[code]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrPermissionNotFound, code)
		}
	}
	return normalized, nil
}

func roleKey(input RoleInput) (string, error) {
	key := strings.TrimSpace(input.Key)
	code := strings.TrimSpace(input.Code)
	if key != "" && code != "" && key != code {
		return "", ErrRoleKeyInvalid
	}
	if key == "" {
		key = code
	}
	if key == "" || !roleKeyPattern.MatchString(key) {
		return "", ErrRoleKeyInvalid
	}
	return key, nil
}

func validateRoleInput(input RoleInput) (string, string, error) {
	key, err := roleKey(input)
	if err != nil {
		return "", "", err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return "", "", ErrRoleNameRequired
	}
	if len([]rune(name)) > 64 {
		return "", "", ErrRoleNameRequired
	}
	if len([]rune(strings.TrimSpace(input.Description))) > 255 {
		return "", "", ErrRoleDescriptionInvalid
	}
	return key, name, nil
}

func (service *Service) ListPermissions() ([]PermissionSummary, error) {
	if service.db == nil {
		return nil, errors.New("database is not initialized")
	}
	var permissions []models.Permission
	if err := service.db.Order("module ASC, code ASC").Find(&permissions).Error; err != nil {
		return nil, err
	}
	result := make([]PermissionSummary, 0, len(permissions))
	for _, permission := range permissions {
		result = append(result, permissionSummary(permission))
	}
	return result, nil
}

func (service *Service) GetRole(code string) (*RoleDetail, error) {
	if service.db == nil {
		return nil, errors.New("database is not initialized")
	}
	code = strings.TrimSpace(code)
	if !roleKeyPattern.MatchString(code) {
		return nil, ErrRoleKeyInvalid
	}
	var role models.Role
	if err := service.db.Where("code = ?", code).First(&role).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRoleNotFound
		}
		return nil, err
	}
	permissions, permissionSet, err := service.rolePermissions(role.ID)
	if err != nil {
		return nil, err
	}
	menus, err := service.loadMenuTree()
	if err != nil {
		return nil, err
	}
	return &RoleDetail{
		RoleSummary: RoleSummary{
			Code:        role.Code,
			Key:         role.Code,
			Name:        role.Name,
			Description: role.Description,
			Builtin:     role.Builtin,
			Permissions: permissions,
		},
		MenuTree: filterMenuTree(menus, permissionSet, role.Code == RoleSuperAdmin),
	}, nil
}

func (service *Service) CreateRole(input RoleInput) (*RoleDetail, error) {
	if service.db == nil {
		return nil, errors.New("database is not initialized")
	}
	key, name, err := validateRoleInput(input)
	if err != nil {
		return nil, err
	}
	if key == RoleSuperAdmin {
		return nil, ErrRoleBuiltin
	}
	permissions, err := validatePermissionCodes(input.PermissionCodes)
	if err != nil {
		return nil, err
	}
	if err := service.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&models.Role{}).Where("code = ?", key).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrRoleExists
		}
		role := &models.Role{Code: key, Name: name, Description: strings.TrimSpace(input.Description), Builtin: false}
		if err := tx.Create(role).Error; err != nil {
			return err
		}
		return replaceRolePermissions(tx, role.ID, permissions)
	}); err != nil {
		return nil, err
	}
	return service.GetRole(key)
}

func (service *Service) UpdateRole(code string, input RoleInput) (*RoleDetail, error) {
	if service.db == nil {
		return nil, errors.New("database is not initialized")
	}
	code = strings.TrimSpace(code)
	if code == "" || !roleKeyPattern.MatchString(code) {
		return nil, ErrRoleKeyInvalid
	}
	if code == RoleSuperAdmin {
		return nil, ErrRoleBuiltin
	}
	var existing models.Role
	if err := service.db.Where("code = ?", code).First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRoleNotFound
		}
		return nil, err
	}
	if existing.Builtin {
		return nil, ErrRoleBuiltin
	}
	if input.Key != "" && strings.TrimSpace(input.Key) != code {
		return nil, ErrRoleKeyInvalid
	}
	if input.Code != "" && strings.TrimSpace(input.Code) != code {
		return nil, ErrRoleKeyInvalid
	}
	_, name, err := validateRoleInput(RoleInput{Key: code, Name: input.Name, Description: input.Description})
	if err != nil {
		return nil, err
	}
	permissions, err := validatePermissionCodes(input.PermissionCodes)
	if err != nil {
		return nil, err
	}
	if err := service.db.Transaction(func(tx *gorm.DB) error {
		var role models.Role
		if err := tx.Where("code = ?", code).First(&role).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRoleNotFound
			}
			return err
		}
		if role.Builtin {
			return ErrRoleBuiltin
		}
		if err := tx.Model(&role).Updates(map[string]any{
			"name":        name,
			"description": strings.TrimSpace(input.Description),
		}).Error; err != nil {
			return err
		}
		return replaceRolePermissions(tx, role.ID, permissions)
	}); err != nil {
		return nil, err
	}
	return service.GetRole(code)
}

func (service *Service) DeleteRole(code string) error {
	if service.db == nil {
		return errors.New("database is not initialized")
	}
	code = strings.TrimSpace(code)
	if !roleKeyPattern.MatchString(code) {
		return ErrRoleKeyInvalid
	}
	return service.db.Transaction(func(tx *gorm.DB) error {
		var role models.Role
		if err := tx.Where("code = ?", code).First(&role).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRoleNotFound
			}
			return err
		}
		if role.Builtin || role.Code == RoleSuperAdmin {
			return ErrRoleBuiltin
		}
		var assigned int64
		if err := tx.Model(&models.UserRole{}).Where("role_id = ?", role.ID).Count(&assigned).Error; err != nil {
			return err
		}
		if assigned > 0 {
			return ErrRoleInUse
		}
		if err := tx.Where("role_id = ?", role.ID).Delete(&models.RolePermission{}).Error; err != nil {
			return err
		}
		return tx.Delete(&role).Error
	})
}

func replaceRolePermissions(tx *gorm.DB, roleID uint64, codes []string) error {
	if err := tx.Where("role_id = ?", roleID).Delete(&models.RolePermission{}).Error; err != nil {
		return err
	}
	if len(codes) == 0 {
		return nil
	}
	var permissions []models.Permission
	if err := tx.Where("code IN ?", codes).Find(&permissions).Error; err != nil {
		return err
	}
	if len(permissions) != len(codes) {
		return ErrPermissionNotFound
	}
	for _, permission := range permissions {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.RolePermission{
			RoleID: roleID, PermissionID: permission.ID, CreatedAt: time.Now().UTC(),
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) rolePermissions(roleID uint64) ([]string, map[string]struct{}, error) {
	var permissions []models.Permission
	if err := service.db.Table("role_permissions AS rp").
		Select("p.id", "p.code", "p.name", "p.description", "p.module").
		Joins("JOIN permissions p ON p.id = rp.permission_id").
		Where("rp.role_id = ?", roleID).
		Order("p.code ASC").Find(&permissions).Error; err != nil {
		return nil, nil, err
	}
	set := make(map[string]struct{}, len(permissions))
	codes := make([]string, 0, len(permissions))
	for _, permission := range permissions {
		set[permission.Code] = struct{}{}
		codes = append(codes, permission.Code)
	}
	return codes, set, nil
}

func (service *Service) ListMenus() ([]MenuNode, error) {
	if service.db == nil {
		return nil, errors.New("database is not initialized")
	}
	return service.loadMenuTree()
}

func (service *Service) CreateMenu(input MenuInput) (*MenuNode, error) {
	if service.db == nil {
		return nil, errors.New("database is not initialized")
	}
	key := strings.TrimSpace(input.Key)
	if !menuKeyPattern.MatchString(key) {
		return nil, ErrMenuKeyInvalid
	}
	var createdID uint64
	if err := service.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&models.Menu{}).Where("key = ?", key).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrMenuExists
		}
		permissionCodes, err := validateMenuInput(tx, input, key, 0)
		if err != nil {
			return err
		}
		menu := &models.Menu{
			Key: key, Type: strings.TrimSpace(input.Type), Name: strings.TrimSpace(input.Name),
			NameEn: strings.TrimSpace(input.NameEn), TargetType: strings.TrimSpace(input.TargetType),
			TargetKey: strings.TrimSpace(input.TargetKey), IconKey: strings.TrimSpace(input.IconKey),
			Sort: input.Sort, Enabled: input.Enabled, Builtin: false,
			SuperAdminOnly: input.SuperAdminOnly, FeatureKey: strings.TrimSpace(input.FeatureKey),
		}
		if parentKey := strings.TrimSpace(input.ParentKey); parentKey != "" {
			var parent models.Menu
			if err := tx.Where("key = ?", parentKey).First(&parent).Error; err != nil {
				return ErrMenuParentInvalid
			}
			menu.ParentID = &parent.ID
		}
		if err := tx.Create(menu).Error; err != nil {
			return err
		}
		createdID = menu.ID
		return replaceMenuPermissions(tx, menu.ID, permissionCodes)
	}); err != nil {
		return nil, err
	}
	nodes, err := service.loadMenuTree()
	if err != nil {
		return nil, err
	}
	return findMenuNode(nodes, createdID), nil
}

func (service *Service) UpdateMenu(key string, input MenuInput) (*MenuNode, error) {
	if service.db == nil {
		return nil, errors.New("database is not initialized")
	}
	key = strings.TrimSpace(key)
	if !menuKeyPattern.MatchString(key) {
		return nil, ErrMenuKeyInvalid
	}
	if input.Key != "" && strings.TrimSpace(input.Key) != key {
		return nil, ErrMenuKeyInvalid
	}
	var updatedID uint64
	if err := service.db.Transaction(func(tx *gorm.DB) error {
		var menu models.Menu
		if err := tx.Where("key = ?", key).First(&menu).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrMenuNotFound
			}
			return err
		}
		if menu.Builtin {
			return ErrMenuBuiltin
		}
		permissionCodes, err := validateMenuInput(tx, input, key, menu.ID)
		if err != nil {
			return err
		}
		var parentID *uint64
		if parentKey := strings.TrimSpace(input.ParentKey); parentKey != "" {
			var parent models.Menu
			if err := tx.Where("key = ?", parentKey).First(&parent).Error; err != nil {
				return ErrMenuParentInvalid
			}
			parentID = &parent.ID
		}
		if err := tx.Model(&menu).Updates(map[string]any{
			"parent_id":        parentID,
			"type":             strings.TrimSpace(input.Type),
			"name":             strings.TrimSpace(input.Name),
			"name_en":          strings.TrimSpace(input.NameEn),
			"target_type":      strings.TrimSpace(input.TargetType),
			"target_key":       strings.TrimSpace(input.TargetKey),
			"icon_key":         strings.TrimSpace(input.IconKey),
			"sort":             input.Sort,
			"enabled":          input.Enabled,
			"super_admin_only": input.SuperAdminOnly,
			"feature_key":      strings.TrimSpace(input.FeatureKey),
		}).Error; err != nil {
			return err
		}
		updatedID = menu.ID
		return replaceMenuPermissions(tx, menu.ID, permissionCodes)
	}); err != nil {
		return nil, err
	}
	nodes, err := service.loadMenuTree()
	if err != nil {
		return nil, err
	}
	return findMenuNode(nodes, updatedID), nil
}

func (service *Service) DeleteMenu(key string) error {
	if service.db == nil {
		return errors.New("database is not initialized")
	}
	key = strings.TrimSpace(key)
	if !menuKeyPattern.MatchString(key) {
		return ErrMenuKeyInvalid
	}
	return service.db.Transaction(func(tx *gorm.DB) error {
		var menu models.Menu
		if err := tx.Where("key = ?", key).First(&menu).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrMenuNotFound
			}
			return err
		}
		if menu.Builtin {
			return ErrMenuBuiltin
		}
		var children int64
		if err := tx.Model(&models.Menu{}).Where("parent_id = ?", menu.ID).Count(&children).Error; err != nil {
			return err
		}
		if children > 0 {
			return ErrMenuHasChildren
		}
		if err := tx.Where("menu_id = ?", menu.ID).Delete(&models.MenuPermission{}).Error; err != nil {
			return err
		}
		return tx.Delete(&menu).Error
	})
}

func validateMenuInput(tx *gorm.DB, input MenuInput, key string, currentID uint64) ([]string, error) {
	typeName := strings.TrimSpace(input.Type)
	if typeName != models.MenuTypeDirectory && typeName != models.MenuTypePage && typeName != models.MenuTypeButton {
		return nil, ErrMenuTypeInvalid
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || len([]rune(name)) > 96 || len([]rune(strings.TrimSpace(input.NameEn))) > 96 {
		return nil, ErrMenuNameInvalid
	}
	iconKey := strings.TrimSpace(input.IconKey)
	if iconKey != "" && !iconKeyPattern.MatchString(iconKey) {
		return nil, ErrMenuIconInvalid
	}
	featureKey := strings.TrimSpace(input.FeatureKey)
	if featureKey != "" && featureKey != MenuFeatureTerminal && featureKey != MenuFeatureBastion {
		return nil, ErrMenuFeatureInvalid
	}
	permissionCodes, err := validatePermissionCodes(input.PermissionCodes)
	if err != nil {
		return nil, err
	}
	if typeName == models.MenuTypeDirectory {
		if strings.TrimSpace(input.TargetType) != "" || strings.TrimSpace(input.TargetKey) != "" {
			return nil, ErrMenuTargetInvalid
		}
	} else {
		if len(permissionCodes) == 0 && !input.SuperAdminOnly {
			return nil, ErrMenuPermissionRequired
		}
		if typeName == models.MenuTypePage && strings.TrimSpace(input.TargetType) == models.MenuTargetRoute && !registeredRouteTarget(strings.TrimSpace(input.TargetKey)) {
			return nil, ErrMenuTargetInvalid
		}
		if typeName == models.MenuTypeButton && strings.TrimSpace(input.TargetType) == models.MenuTargetAction && !registeredActionTarget(strings.TrimSpace(input.TargetKey)) {
			return nil, ErrMenuTargetInvalid
		}
		if (typeName == models.MenuTypePage && strings.TrimSpace(input.TargetType) != models.MenuTargetRoute) ||
			(typeName == models.MenuTypeButton && strings.TrimSpace(input.TargetType) != models.MenuTargetAction) ||
			strings.TrimSpace(input.TargetKey) == "" {
			return nil, ErrMenuTargetInvalid
		}
	}
	parentKey := strings.TrimSpace(input.ParentKey)
	if parentKey == "" {
		if typeName == models.MenuTypeButton {
			return nil, ErrMenuParentInvalid
		}
		return permissionCodes, nil
	}
	var parent models.Menu
	if err := tx.Where("key = ?", parentKey).First(&parent).Error; err != nil {
		return nil, ErrMenuParentInvalid
	}
	if parent.ID == currentID || parent.Type == models.MenuTypeButton {
		return nil, ErrMenuParentInvalid
	}
	if currentID != 0 {
		parents := make(map[uint64]*uint64)
		var menus []models.Menu
		if err := tx.Select("id", "parent_id").Find(&menus).Error; err != nil {
			return nil, err
		}
		for _, menu := range menus {
			parents[menu.ID] = menu.ParentID
		}
		parents[currentID] = &parent.ID
		seen := map[uint64]struct{}{currentID: {}}
		for id := parent.ID; id != 0; {
			if _, exists := seen[id]; exists {
				return nil, ErrMenuCycle
			}
			seen[id] = struct{}{}
			parentID := parents[id]
			if parentID == nil {
				break
			}
			id = *parentID
		}
	}
	return permissionCodes, nil
}

var ErrMenuNameInvalid = errors.New("menu name is invalid")

func replaceMenuPermissions(tx *gorm.DB, menuID uint64, codes []string) error {
	if err := tx.Where("menu_id = ?", menuID).Delete(&models.MenuPermission{}).Error; err != nil {
		return err
	}
	if len(codes) == 0 {
		return nil
	}
	var permissions []models.Permission
	if err := tx.Where("code IN ?", codes).Find(&permissions).Error; err != nil {
		return err
	}
	if len(permissions) != len(codes) {
		return ErrPermissionNotFound
	}
	for _, permission := range permissions {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.MenuPermission{
			MenuID: menuID, PermissionID: permission.ID, CreatedAt: time.Now().UTC(),
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) loadMenuTree() ([]MenuNode, error) {
	var menus []models.Menu
	if err := service.db.Order("sort ASC, key ASC").Find(&menus).Error; err != nil {
		return nil, err
	}
	type permissionRow struct {
		MenuID       uint64
		PermissionID uint64
		Code         string
		Name         string
		Description  string
		Module       string
	}
	var rows []permissionRow
	if err := service.db.Table("menu_permissions AS mp").
		Select("mp.menu_id", "p.id AS permission_id", "p.code", "p.name", "p.description", "p.module").
		Joins("JOIN permissions p ON p.id = mp.permission_id").
		Order("mp.menu_id ASC, p.code ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	permissionsByMenu := make(map[uint64][]PermissionSummary)
	for _, row := range rows {
		permissionsByMenu[row.MenuID] = append(permissionsByMenu[row.MenuID], PermissionSummary{
			Code: row.Code, Name: row.Name, Description: row.Description, Module: row.Module, Action: permissionAction(row.Code),
		})
	}
	return buildMenuTree(menus, permissionsByMenu), nil
}

func buildMenuTree(menus []models.Menu, permissionsByMenu map[uint64][]PermissionSummary) []MenuNode {
	nodes := make(map[uint64]*MenuNode, len(menus))
	for _, menu := range menus {
		parentKey := ""
		if menu.ParentID != nil {
			for _, candidate := range menus {
				if candidate.ID == *menu.ParentID {
					parentKey = candidate.Key
					break
				}
			}
		}
		copyPermissions := append([]PermissionSummary(nil), permissionsByMenu[menu.ID]...)
		nodes[menu.ID] = &MenuNode{
			ID: menu.ID, Key: menu.Key, ParentKey: parentKey, Type: menu.Type,
			Name: menu.Name, NameEn: menu.NameEn, TargetType: menu.TargetType, TargetKey: menu.TargetKey,
			IconKey: menu.IconKey, Sort: menu.Sort, Enabled: menu.Enabled, Builtin: menu.Builtin,
			SuperAdminOnly: menu.SuperAdminOnly, FeatureKey: menu.FeatureKey, Permissions: copyPermissions,
		}
	}
	children := make(map[uint64][]*MenuNode)
	roots := make([]*MenuNode, 0)
	for _, menu := range menus {
		node := nodes[menu.ID]
		if menu.ParentID == nil {
			roots = append(roots, node)
			continue
		}
		if _, exists := nodes[*menu.ParentID]; !exists {
			roots = append(roots, node)
			continue
		}
		children[*menu.ParentID] = append(children[*menu.ParentID], node)
	}
	var attach func(*MenuNode)
	attach = func(node *MenuNode) {
		items := children[node.ID]
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].Sort != items[j].Sort {
				return items[i].Sort < items[j].Sort
			}
			return items[i].Key < items[j].Key
		})
		for _, item := range items {
			attach(item)
			node.Children = append(node.Children, *item)
		}
	}
	sort.SliceStable(roots, func(i, j int) bool {
		if roots[i].Sort != roots[j].Sort {
			return roots[i].Sort < roots[j].Sort
		}
		return roots[i].Key < roots[j].Key
	})
	result := make([]MenuNode, 0, len(roots))
	for _, root := range roots {
		attach(root)
		result = append(result, *root)
	}
	return result
}

func filterMenuTree(nodes []MenuNode, permissionSet map[string]struct{}, superAdmin bool) []MenuNode {
	result := make([]MenuNode, 0, len(nodes))
	for _, node := range nodes {
		if !node.Enabled {
			continue
		}
		if node.SuperAdminOnly && !superAdmin {
			continue
		}
		children := filterMenuTree(node.Children, permissionSet, superAdmin)
		allowed := superAdmin || (!node.SuperAdminOnly && hasMenuPermission(node, permissionSet))
		if node.Type == models.MenuTypeDirectory {
			if len(children) == 0 && !allowed {
				continue
			}
			node.Children = children
			result = append(result, node)
			continue
		}
		if !allowed {
			continue
		}
		node.Children = children
		result = append(result, node)
	}
	return result
}

func hasMenuPermission(node MenuNode, permissionSet map[string]struct{}) bool {
	for _, permission := range node.Permissions {
		if _, ok := permissionSet[permission.Code]; ok {
			return true
		}
	}
	return false
}

// FilterMenuTreeByFeatures applies runtime feature switches after the access
// service has evaluated role permissions. It keeps environment capability out
// of the database and mirrors the existing terminal/bastion menu semantics.
func FilterMenuTreeByFeatures(nodes []MenuNode, terminalEnabled, bastionEnabled bool) []MenuNode {
	result := make([]MenuNode, 0, len(nodes))
	for _, node := range nodes {
		if node.FeatureKey == MenuFeatureTerminal && !terminalEnabled {
			continue
		}
		if node.FeatureKey == MenuFeatureBastion && !bastionEnabled {
			continue
		}
		node.Children = FilterMenuTreeByFeatures(node.Children, terminalEnabled, bastionEnabled)
		if node.Type == models.MenuTypeDirectory && len(node.Children) == 0 && len(node.Permissions) == 0 {
			continue
		}
		result = append(result, node)
	}
	return result
}

func findMenuNode(nodes []MenuNode, id uint64) *MenuNode {
	for index := range nodes {
		if nodes[index].ID == id {
			return &nodes[index]
		}
		if found := findMenuNode(nodes[index].Children, id); found != nil {
			return found
		}
	}
	return nil
}

var builtinRouteTargets = map[string]struct{}{
	"/home": {}, "/website": {}, "/database": {}, "/software": {}, "/container": {}, "/file": {},
	"/terminal": {}, "/task": {}, "/monitor": {}, "/bastion": {}, "/runtime-log": {}, "/security": {},
	"/certificate": {}, "/approval-center": {}, "/log": {}, "/config-snapshots": {},
	"/system-management": {}, "/user-management": {}, "/setting": {},
}

var builtinStaticActionPermissions = map[string]string{
	"website.delete":              PermissionWebsiteWrite,
	"website.restore":             PermissionWebsiteWrite,
	"database.restore":            PermissionDatabaseWrite,
	"database.connection.delete":  PermissionDatabaseWrite,
	"audit.export":                PermissionAuditExport,
	"audit.verify":                PermissionAuditVerify,
	"database.credential.reveal":  PermissionDatabaseWrite,
	"certificate.issue":           PermissionWebsiteWrite,
	"file.create":                 PermissionFileCreate,
	"file.edit":                   PermissionFileEdit,
	"file.move":                   PermissionFileMove,
	"file.delete":                 PermissionFileDelete,
	"file.modify":                 PermissionFileModify,
	"file.archive":                PermissionFileArchive,
	"file.share.create":           PermissionFileShare,
	"file.share.revoke":           PermissionFileShare,
	"container.terminal":          PermissionContainerTerminal,
	"container.force_action":      PermissionContainerForceAction,
	"container.dangerous.cleanup": PermissionContainerDangerousCleanup,
}

func registeredRouteTarget(target string) bool {
	_, ok := builtinRouteTargets[target]
	return ok
}

func registeredActionTarget(target string) bool {
	if _, ok := builtinStaticActionPermissions[target]; ok {
		return true
	}
	_, ok := operationPermissions[target]
	return ok
}

type builtinMenuDefinition struct {
	Key            string
	ParentKey      string
	Type           string
	Name           string
	NameEn         string
	TargetType     string
	TargetKey      string
	IconKey        string
	Sort           int
	SuperAdminOnly bool
	FeatureKey     string
	Permissions    []string
}

func builtinMenuDefinitions() []builtinMenuDefinition {
	return []builtinMenuDefinition{
		{Key: "dashboard", Type: models.MenuTypePage, Name: "面板概览", NameEn: "Dashboard", TargetType: models.MenuTargetRoute, TargetKey: "/home", IconKey: "dashboard", Sort: 10, Permissions: []string{PermissionDashboardRead}},
		{Key: "website", Type: models.MenuTypePage, Name: "网站", NameEn: "Websites", TargetType: models.MenuTargetRoute, TargetKey: "/website", IconKey: "website", Sort: 20, Permissions: []string{PermissionWebsiteRead, PermissionWebsiteWrite, PermissionWebsiteApproval}},
		{Key: "database", Type: models.MenuTypePage, Name: "数据库", NameEn: "Databases", TargetType: models.MenuTargetRoute, TargetKey: "/database", IconKey: "database", Sort: 30, Permissions: []string{PermissionDatabaseRead, PermissionDatabaseWrite, PermissionDatabaseApproval}},
		{Key: "software", Type: models.MenuTypePage, Name: "软件商店", NameEn: "Software store", TargetType: models.MenuTargetRoute, TargetKey: "/software", IconKey: "software-store", Sort: 40, Permissions: []string{PermissionSoftwareRead, PermissionSoftwareWrite, PermissionServiceRead, PermissionServiceWrite}},
		{Key: "container", Type: models.MenuTypePage, Name: "容器", NameEn: "Containers", TargetType: models.MenuTargetRoute, TargetKey: "/container", IconKey: "container-management", Sort: 50, Permissions: []string{PermissionContainerRead, PermissionContainerWrite, PermissionContainerDelete, PermissionContainerTerminal, PermissionContainerLogsRead, PermissionContainerImageWrite, PermissionContainerNetworkWrite, PermissionContainerVolumeWrite, PermissionContainerComposeWrite, PermissionContainerRegistryWrite, PermissionContainerConfigWrite, PermissionContainerRuntimeInstall, PermissionContainerDangerousCleanup, PermissionContainerForceAction}},
		{Key: "file", Type: models.MenuTypePage, Name: "文件", NameEn: "Files", TargetType: models.MenuTargetRoute, TargetKey: "/file", IconKey: "file", Sort: 60, Permissions: []string{PermissionFileRead, PermissionFileWrite, PermissionFileCreate, PermissionFileEdit, PermissionFileMove, PermissionFileDelete, PermissionFileModify, PermissionFileArchive, PermissionFileShare}},
		{Key: "terminal", Type: models.MenuTypePage, Name: "服务器终端", NameEn: "Secure terminal", TargetType: models.MenuTargetRoute, TargetKey: "/terminal", IconKey: "terminal", Sort: 70, FeatureKey: MenuFeatureTerminal, Permissions: []string{PermissionTerminalAccess}},
		{Key: "operations", Type: models.MenuTypeDirectory, Name: "运维", NameEn: "Operations", IconKey: "operations", Sort: 80},
		{Key: "monitoring", ParentKey: "operations", Type: models.MenuTypePage, Name: "监控", NameEn: "Monitoring", TargetType: models.MenuTargetRoute, TargetKey: "/monitor", IconKey: "monitoring", Sort: 10, Permissions: []string{PermissionMonitoringRead, PermissionMonitoringWrite}},
		{Key: "bastion", ParentKey: "operations", Type: models.MenuTypePage, Name: "堡垒机", NameEn: "Bastion", TargetType: models.MenuTargetRoute, TargetKey: "/bastion", IconKey: "bastion", Sort: 20, FeatureKey: MenuFeatureBastion, Permissions: []string{PermissionBastionRead, PermissionBastionWrite}},
		{Key: "runtimeLog", ParentKey: "operations", Type: models.MenuTypePage, Name: "运行日志", NameEn: "Runtime logs", TargetType: models.MenuTargetRoute, TargetKey: "/runtime-log", IconKey: "runtime-log", Sort: 30, Permissions: []string{PermissionRuntimeLogRead}},
		{Key: "securityGroup", Type: models.MenuTypeDirectory, Name: "安全与审计", NameEn: "Security and audit", IconKey: "security-audit", Sort: 90},
		{Key: "security", ParentKey: "securityGroup", Type: models.MenuTypePage, Name: "安全", NameEn: "Security", TargetType: models.MenuTargetRoute, TargetKey: "/security", IconKey: "security", Sort: 10, Permissions: []string{PermissionSecurityRead, PermissionSecurityWrite}},
		{Key: "certificate", ParentKey: "securityGroup", Type: models.MenuTypePage, Name: "证书", NameEn: "Certificates", TargetType: models.MenuTargetRoute, TargetKey: "/certificate", IconKey: "certificate", Sort: 20, Permissions: []string{PermissionCertificateRead, PermissionCertificateWrite}},
		{Key: "approval", ParentKey: "securityGroup", Type: models.MenuTypePage, Name: "审批中心", NameEn: "Approval center", TargetType: models.MenuTargetRoute, TargetKey: "/approval-center", IconKey: "approval-center", Sort: 30, Permissions: []string{PermissionApprovalRead}},
		{Key: "audit", ParentKey: "securityGroup", Type: models.MenuTypePage, Name: "审计日志", NameEn: "Audit logs", TargetType: models.MenuTargetRoute, TargetKey: "/log", IconKey: "audit-log", Sort: 40, Permissions: []string{PermissionAuditRead}},
		{Key: "configSnapshots", ParentKey: "securityGroup", Type: models.MenuTypePage, Name: "配置快照", NameEn: "Config snapshots", TargetType: models.MenuTargetRoute, TargetKey: "/config-snapshots", IconKey: "config-snapshots", Sort: 50, Permissions: []string{PermissionConfigSnapshotRead, PermissionConfigSnapshotWrite}},
		{Key: "cron", Type: models.MenuTypePage, Name: "计划任务", NameEn: "Scheduled tasks", TargetType: models.MenuTargetRoute, TargetKey: "/task", IconKey: "scheduled-tasks", Sort: 100, Permissions: []string{PermissionCronRead, PermissionCronWrite}},
		{Key: "systemGroup", Type: models.MenuTypeDirectory, Name: "系统设置", NameEn: "System settings", IconKey: "system-settings", Sort: 110},
		{Key: "systemManagement", ParentKey: "systemGroup", Type: models.MenuTypePage, Name: "系统管理", NameEn: "System management", TargetType: models.MenuTargetRoute, TargetKey: "/system-management", IconKey: "system-management", Sort: 10, Permissions: []string{PermissionSystemRead, PermissionSystemWrite}},
		{Key: "userManagement", ParentKey: "systemGroup", Type: models.MenuTypePage, Name: "用户管理", NameEn: "User management", TargetType: models.MenuTargetRoute, TargetKey: "/user-management", IconKey: "user-management", Sort: 20, SuperAdminOnly: true},
		{Key: "panelSettings", ParentKey: "systemGroup", Type: models.MenuTypePage, Name: "面板设置", NameEn: "Panel settings", TargetType: models.MenuTargetRoute, TargetKey: "/setting", IconKey: "panel-settings", Sort: 30, SuperAdminOnly: true},
	}
}

func buttonParentKey(action string) string {
	switch {
	case strings.HasPrefix(action, "website."):
		return "website"
	case strings.HasPrefix(action, "database."):
		return "database"
	case strings.HasPrefix(action, "certificate."):
		return "certificate"
	case strings.HasPrefix(action, "audit."):
		return "audit"
	case strings.HasPrefix(action, "software."):
		return "software"
	case strings.HasPrefix(action, "firewall."), strings.HasPrefix(action, "fail2ban."):
		return "security"
	case strings.HasPrefix(action, "panel."):
		return "systemManagement"
	case strings.HasPrefix(action, "container."):
		return "container"
	case strings.HasPrefix(action, "file."):
		return "file"
	default:
		return ""
	}
}

func seedBuiltinMenus(tx *gorm.DB, permissionIDByCode map[string]uint64) error {
	definitions := builtinMenuDefinitions()
	allActions := make(map[string]string, len(operationPermissions)+len(builtinStaticActionPermissions))
	for action, permission := range operationPermissions {
		allActions[action] = permission
	}
	for action, permission := range builtinStaticActionPermissions {
		allActions[action] = permission
	}
	actions := make([]string, 0, len(allActions))
	for action := range allActions {
		if buttonParentKey(action) != "" {
			actions = append(actions, action)
		}
	}
	sort.Strings(actions)
	for _, action := range actions {
		definitions = append(definitions, builtinMenuDefinition{
			Key: "button." + action, ParentKey: buttonParentKey(action), Type: models.MenuTypeButton,
			Name: action, NameEn: action, TargetType: models.MenuTargetAction, TargetKey: action,
			Sort: 1000, Permissions: []string{allActions[action]},
		})
	}

	idByKey := make(map[string]uint64, len(definitions))
	for _, definition := range definitions {
		parentID := (*uint64)(nil)
		if definition.ParentKey != "" {
			id, ok := idByKey[definition.ParentKey]
			if !ok {
				var parent models.Menu
				if err := tx.Where("key = ?", definition.ParentKey).First(&parent).Error; err != nil {
					return err
				}
				id = parent.ID
			}
			parentID = &id
		}
		menu := models.Menu{
			Key: definition.Key, ParentID: parentID, Type: definition.Type, Name: definition.Name,
			NameEn: definition.NameEn, TargetType: definition.TargetType, TargetKey: definition.TargetKey,
			IconKey: definition.IconKey, Sort: definition.Sort, Enabled: true, Builtin: true,
			SuperAdminOnly: definition.SuperAdminOnly, FeatureKey: definition.FeatureKey,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"parent_id", "type", "name", "name_en", "target_type", "target_key", "icon_key",
				"sort", "enabled", "builtin", "super_admin_only", "feature_key", "updated_at",
			}),
		}).Create(&menu).Error; err != nil {
			return err
		}
		var stored models.Menu
		if err := tx.Where("key = ?", definition.Key).First(&stored).Error; err != nil {
			return err
		}
		idByKey[definition.Key] = stored.ID
		permissionCodes, err := validatePermissionCodes(definition.Permissions)
		if err != nil {
			return err
		}
		for _, code := range permissionCodes {
			permissionID, ok := permissionIDByCode[code]
			if !ok || permissionID == 0 {
				return ErrPermissionNotFound
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.MenuPermission{
				MenuID: stored.ID, PermissionID: permissionID, CreatedAt: time.Now().UTC(),
			}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("menu_id = ? AND permission_id NOT IN ?", stored.ID, permissionIDs(permissionCodes, permissionIDByCode)).Delete(&models.MenuPermission{}).Error; err != nil && len(permissionCodes) > 0 {
			return err
		}
		if len(permissionCodes) == 0 {
			if err := tx.Where("menu_id = ?", stored.ID).Delete(&models.MenuPermission{}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func permissionIDs(codes []string, byCode map[string]uint64) []uint64 {
	ids := make([]uint64, 0, len(codes))
	for _, code := range codes {
		if id := byCode[code]; id != 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

func normalizeRoleCodes(roleCodes []string, isAdminAlias bool) ([]string, error) {
	seen := make(map[string]struct{}, len(roleCodes)+1)
	result := make([]string, 0, len(roleCodes)+1)
	for _, code := range roleCodes {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		if _, exists := seen[code]; exists {
			continue
		}
		seen[code] = struct{}{}
		result = append(result, code)
	}
	if isAdminAlias {
		if _, exists := seen[RoleSuperAdmin]; !exists {
			result = append(result, RoleSuperAdmin)
		}
	}
	sort.Strings(result)
	return result, nil
}

func migrateLegacyAdministrators(tx *gorm.DB) error {
	var role models.Role
	if err := tx.Where("code = ?", RoleSuperAdmin).First(&role).Error; err != nil {
		return err
	}
	var users []models.User
	if err := tx.Where("is_admin = ?", true).Find(&users).Error; err != nil {
		return err
	}
	for _, user := range users {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.UserRole{
			UserID: user.ID, RoleID: role.ID, AssignedAt: time.Now().UTC(),
		}).Error; err != nil {
			return err
		}
	}
	// is_admin remains a compatibility projection; role membership is the
	// authorization source of truth after this transaction.
	return tx.Exec(`UPDATE users SET is_admin = CASE WHEN EXISTS (
		SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id
		WHERE ur.user_id = users.id AND r.code = ?
	) THEN 1 ELSE 0 END`, RoleSuperAdmin).Error
}
