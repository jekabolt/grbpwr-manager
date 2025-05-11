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
}

type RevalidationResponse struct {
	Success bool `json:"success"`
}
