package input

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	TOTPCode string `json:"totpCode"`
}

type UpdateUserRequest struct {
	Username string `json:"username"`
}

type ResetPasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	Password        string `json:"password"`
}

type AccessCreateUserRequest struct {
	Username  string   `json:"username"`
	Password  string   `json:"password"`
	RoleCodes []string `json:"roleCodes"`
	IsAdmin   bool     `json:"isAdmin"`
}

type AccessAssignRolesRequest struct {
	RoleCodes []string `json:"roleCodes"`
}

type AccessRoleRequest struct {
	Key             string   `json:"key"`
	Code            string   `json:"code"`
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	PermissionCodes []string `json:"permissionCodes"`
	Permissions     []string `json:"permissions"`
}

type AccessMenuRequest struct {
	Key             string   `json:"key"`
	ParentKey       string   `json:"parentKey"`
	Type            string   `json:"type"`
	Name            string   `json:"name"`
	NameEn          string   `json:"nameEn"`
	TargetType      string   `json:"targetType"`
	TargetKey       string   `json:"targetKey"`
	IconKey         string   `json:"iconKey"`
	Sort            int      `json:"sort"`
	Enabled         *bool    `json:"enabled"`
	SuperAdminOnly  bool     `json:"superAdminOnly"`
	FeatureKey      string   `json:"featureKey"`
	PermissionCodes []string `json:"permissionCodes"`
	Permissions     []string `json:"permissions"`
}

type AccessMenuStatusRequest struct {
	Enabled *bool `json:"enabled"`
}

type AccessResetUserPasswordRequest struct {
	Password string `json:"password"`
}

type ApprovalReviewRequest struct {
	Comment string `json:"comment"`
}
