package dto

type DeploymentResponse struct {
	Deployments []Deployment `json:"deployments"`
}
type Deployment struct {
	URL string `json:"url"`
}

type RevalidationProduct struct {
	ID int `json:"id"`
}

type RevalidationHero struct {
	Changed bool `json:"changed"`
}

type RevalidationArchive struct {
	ID string `json:"id"`
}

type RevalidationData struct {
	Product RevalidationProduct `json:"product"`
	Hero    RevalidationHero    `json:"hero"`
	Archive RevalidationArchive `json:"archive"`
}

type RevalidationResponse struct {
	Success bool `json:"success"`
}
