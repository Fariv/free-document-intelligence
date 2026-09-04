package main

import (
	"fmt"
	"sync"
	"time"
)

type MemoryStore struct {
	tasks map[string]*TaskResult
	mu    sync.RWMutex
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		tasks: make(map[string]*TaskResult),
	}
}

func (ms *MemoryStore) CreateTask(id string) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	now := time.Now()
	ms.tasks[id] = &TaskResult{
		Status:    "processing",
		CreatedAt: now,
		ExpiresAt: now.Add(2 * time.Minute), // Default 2 minutes for testing
	}
	return nil
}

func (ms *MemoryStore) GetTask(id string) (*TaskResult, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	task, exists := ms.tasks[id]
	if !exists {
		return nil, fmt.Errorf("task with id %s not found", id)
	}

	// Check if task has expired
	if time.Now().After(task.ExpiresAt) {
		return nil, fmt.Errorf("task with id %s has expired", id)
	}

	return task, nil
}

func (ms *MemoryStore) UpdateTask(id string, status string, data interface{}, errMsg string) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	task, exists := ms.tasks[id]
	if !exists {
		return fmt.Errorf("task with id %s not found", id)
	}
	task.Status = status
	task.Data = data
	task.Error = errMsg
	return nil
}

// StartCleanupWorker removes expired tasks periodically
func (ms *MemoryStore) StartCleanupWorker(ttl time.Duration) {
	go func() {
		ticker := time.NewTicker(ttl / 2) // Cleanup every half of TTL
		defer ticker.Stop()

		for range ticker.C {
			ms.mu.Lock()
			now := time.Now()
			for id, task := range ms.tasks {
				if now.After(task.ExpiresAt) {
					delete(ms.tasks, id)
					fmt.Printf("[Cleanup] Removed expired task: %s\n", id)
				}
			}
			ms.mu.Unlock()
		}
	}()
}
