package storage

import (
	"github.com/nknorg/tuna/util"
	"sync"
)

type Storage struct {
	sync.RWMutex
	data map[string]interface{}
}

func NewStorage() *Storage {
	return &Storage{
		data: make(map[string]interface{}),
	}
}

func (s *Storage) GetData() map[string]interface{} {
	s.RLock()
	defer s.RUnlock()
	return util.DeepCopyMap(s.data)
}

func (s *Storage) Get(key string) (interface{}, bool) {
	s.RLock()
	defer s.RUnlock()
	v, ok := s.data[key]
	return v, ok
}

func (s *Storage) Add(key string, val interface{}) {
	s.Lock()
	defer s.Unlock()
	s.data[key] = val
}

func (s *Storage) Delete(key string) {
	s.Lock()
	defer s.Unlock()
	delete(s.data, key)
}

func (s *Storage) Len() int {
	s.RLock()
	defer s.RUnlock()
	return len(s.data)
}
