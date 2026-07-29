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

type AccessResetUserPasswordRequest struct {
	Password string `json:"password"`
}

type ApprovalReviewRequest struct {
	Comment string `json:"comment"`
}
