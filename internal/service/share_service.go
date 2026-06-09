package service

import (
	"replica/internal/model"
	"replica/internal/repository"
)

type ShareService struct {
	repo *repository.ShareRepository
}

func NewShareService(repo *repository.ShareRepository) *ShareService {
	return &ShareService{repo: repo}
}

func (s *ShareService) List() ([]model.Share, error) {
	return s.repo.List()
}
