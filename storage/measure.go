package storage

import (
	"github.com/nknorg/tuna/util"
	"path/filepath"
	"time"
)

const (
	Expired          = 7 * 24 * time.Hour
	FavoriteFileName = "favorite_node.json"
	BadFileName      = "bad_node.json"
)

type FavoriteNode struct {
	*Index
	IP           string  `json:"ip"`
	Address      string  `json:"address"`
	Delay        float32 `json:"delay"`
	MinBandwidth float32 `json:"minBandwidth"`
	MaxBandwidth float32 `json:"maxBandwidth"`
	Expired      int64   `json:"expired"`
}

type BadNode struct {
	*Index
	IP      string `json:"ip"`
	Subnet  int32  `json:"subnet"`
	Address string `json:"address"`
	Expired int32  `json:"expired"`
}

type MeasureStorage struct {
	path             string
	favoriteFilePath string
	badFilePath      string
	expire           time.Duration

	FavoriteNodes *Storage
	BadNodes      *Storage
}

func LoadMeasureStorage(path string) *MeasureStorage {
	return &MeasureStorage{
		path:             path,
		favoriteFilePath: filepath.Join(path, FavoriteFileName),
		badFilePath:      filepath.Join(path, BadFileName),
		expire:           Expired,
	}
}

func (s *MeasureStorage) Load() error {
	isExists := util.Exists(s.favoriteFilePath)
	if !isExists {
		err := util.WriteJSON(s.favoriteFilePath, []*FavoriteNode{})
		if err != nil {
			return err
		}
	}
	var favoriteData []*FavoriteNode
	err := util.ReadJSON(s.favoriteFilePath, &favoriteData)
	if err != nil {
		return err
	}

	values := make([]*Value, 0, len(favoriteData))
	for _, f := range favoriteData {
		values = append(values, &Value{
			Index: f.Index,
			Data:  f,
		})
	}
	s.FavoriteNodes = NewStorage(values...)

	isExists = util.Exists(s.badFilePath)
	if !isExists {
		err := util.WriteJSON(s.badFilePath, []*BadNode{})
		if err != nil {
			return err
		}
	}
	var badData []*BadNode
	err = util.ReadJSON(s.badFilePath, &badData)
	if err != nil {
		return err
	}

	values = make([]*Value, 0, len(badData))
	for _, f := range badData {
		values = append(values, &Value{
			Index: f.Index,
			Data:  f,
		})
	}
	s.BadNodes = NewStorage(values...)

	s.ClearExpired()
	return nil
}

func (s *MeasureStorage) ClearExpired() {
	//favoriteNodes := make([]*Storage, 0, len(s.FavoriteNodes.Values))
	//for _, f := range s.FavoriteNodes.Values {
	//	if time.Now().Unix() < f.Data.(*FavoriteNode).Expired {
	//		favoriteNodes = append(favoriteNodes, f)
	//	}
	//}
}

func (s *MeasureStorage) SaveFavoriteNodes() error {
	err := util.WriteJSON(s.favoriteFilePath, s.FavoriteNodes.Values.ToJson())
	if err != nil {
		return err
	}
	return nil
}

func (s *MeasureStorage) SaveBadNodes() error {
	err := util.WriteJSON(s.badFilePath, s.BadNodes.Values.ToJson())
	if err != nil {
		return err
	}
	return nil
}
