package service

import (
	"dropoutbox/internal/model"
	"dropoutbox/internal/repository"
)

type InventoryService struct {
	repo *repository.InventoryRepository
}

func NewInventoryService(repo *repository.InventoryRepository) *InventoryService {
	return &InventoryService{repo: repo}
}

func (s *InventoryService) List() ([]model.Inventory, error) {
	return s.repo.List()
}
