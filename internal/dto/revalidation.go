package dto

type DeploymentResponse struct {
	Deployments []Deployment `json:"deployments"`
}
type Deployment struct {
	URL string `json:"url"`
}

type RevalidationData struct {
	Products []int `json:"products"`
	Hero     bool  `json:"hero"`
	Archive  int   `json:"archive"`
	// Profile asks a team shop to redraw its profile (X18); left out of the JSON when false, so the
	// grbpwr storefront sees the body it always saw.
	Profile bool `json:"profile,omitempty"`
}

type RevalidationResponse struct {
	Success bool `json:"success"`
}
