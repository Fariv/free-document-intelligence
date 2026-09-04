package main

import "time"

type TaskResult struct {
	Status    string      `json:"status"`
	Data      interface{} `json:"analyzeResult,omitempty"`
	Error     string      `json:"error,omitempty"`
	CreatedAt time.Time   `json:"createdAt"`
	ExpiresAt time.Time   `json:"expiresAt"`
}

type TaskRepository interface {
	CreateTask(id string) error
	GetTask(id string) (*TaskResult, error)
	UpdateTask(id string, status string, data interface{}, errMsg string) error
	StartCleanupWorker(ttl time.Duration)
}
