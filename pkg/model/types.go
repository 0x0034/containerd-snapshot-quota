package model

import "time"

// ContainerInfo holds metadata about a container.
type ContainerInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	Upperdir     string `json:"upperdir"`
	Workdir      string `json:"workdir"`
	Image        string `json:"image"`
	ImageTag     string `json:"imageTag"`
	PodName      string `json:"podName"`
	PodNamespace string `json:"podNamespace"`
	MatchedRule  string `json:"matchedRule"`
	Done         bool   `json:"done"`
}

// QuotaStatus indicates the current state of a quota assignment.
type QuotaStatus string

const (
	QuotaStatusPending QuotaStatus = "pending"
	QuotaStatusSucceed QuotaStatus = "succeed"
	QuotaStatusFailed  QuotaStatus = "failed"
)

// Quota represents a quota assignment for a container.
type Quota struct {
	ID        string        `json:"ID"`
	ProjectID uint32        `json:"ProjectID"`
	Status    QuotaStatus   `json:"Status"`
	Message   string        `json:"Message"`
	Size      string        `json:"Size"`
	CInfo     ContainerInfo `json:"CInfo"`
	CreatedAt time.Time     `json:"CreatedAt"`
}

// QuotaRequest is the input for setting a quota.
type QuotaRequest struct {
	ContainerID string
	Upperdir    string
	Workdir     string
	Size        string
	CInfo       ContainerInfo
}
