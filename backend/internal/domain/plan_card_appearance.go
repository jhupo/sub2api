package domain

// PlanCardAppearance belongs to the mutable product catalog, not purchased entitlements.
type PlanCardAppearance struct {
	Theme  string `json:"theme"`
	Motion string `json:"motion"`
}
