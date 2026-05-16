package service

import (
	"dropoutbox/internal/model"
	"dropoutbox/internal/repository"
)

type ReplicaService struct {
	repo *repository.ReplicaRepository
}

func NewReplicaService(repo *repository.ReplicaRepository) *ReplicaService {
	return &ReplicaService{repo: repo}
}

func (s *ReplicaService) List() ([]model.Replica, error) {
	return s.repo.List()
}
